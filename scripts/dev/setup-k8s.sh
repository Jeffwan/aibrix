#!/usr/bin/env bash
# Setup B: minio + metadata pod + Kubernetes cluster.
# Assumes you already have a working cluster (kind/minikube/EKS/etc.) and kubectl
# pointing at it. Deploys aibrix-system namespace with redis, minio, fake-vllm,
# metadata-service. Then port-forwards :8090 so you can hit the API locally.
#
# Usage:
#   ./scripts/dev/setup-k8s.sh             # apply + wait + port-forward
#   ./scripts/dev/setup-k8s.sh teardown    # delete namespace
#   ./scripts/dev/setup-k8s.sh status      # show resources

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
NS="aibrix-system"
RUN_DIR="${ROOT}/.run"
PF_PID="${RUN_DIR}/portforward.pid"
mkdir -p "${RUN_DIR}"

require() { command -v "$1" >/dev/null || { echo "[fatal] missing $1"; exit 1; }; }
require kubectl

case "${1:-up}" in
  teardown)
    [ -f "${PF_PID}" ] && { kill "$(cat "${PF_PID}")" 2>/dev/null || true; rm -f "${PF_PID}"; }
    kubectl delete namespace "${NS}" --ignore-not-found --wait=false
    echo "[teardown] namespace ${NS} deletion requested"
    exit 0
    ;;
  status)
    kubectl -n "${NS}" get pods,svc,jobs,cm
    exit 0
    ;;
  up) ;;
  *) echo "usage: $0 [up|teardown|status]"; exit 1 ;;
esac

echo "[1/6] cluster reachable?"
kubectl cluster-info >/dev/null

echo "[2/6] apply redis (from config/metadata/redis.yaml)"
# Shipping redis lives at config/metadata/redis.yaml but assumes the namespace
# already exists. Create it first then apply.
kubectl create namespace "${NS}" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "${ROOT}/config/metadata/redis.yaml"

echo "[3/6] apply minio + bucket job"
kubectl apply -f "${ROOT}/scripts/dev/k8s/minio.yaml"

echo "[4/6] apply fake vllm (in-cluster)"
kubectl apply -f "${ROOT}/scripts/dev/k8s/fake-vllm.yaml"

echo "[5/6] apply metadata service (config/metadata via kustomize, with S3 patch)"
# Enable the S3 patch by uncommenting and apply via kustomize.
TMP_OVERLAY="$(mktemp -d)"
cat > "${TMP_OVERLAY}/kustomization.yaml" <<EOF
namespace: ${NS}
resources:
  - ${ROOT}/config/metadata/metadata.yaml
  - ${ROOT}/config/metadata/redis.yaml
configMapGenerator:
  - name: metadata-config
    files:
      - ${ROOT}/config/metadata/job_template_patch.yaml
patches:
  - path: ${ROOT}/config/metadata/s3-env-patch.yaml
EOF
kubectl apply -k "${TMP_OVERLAY}" || {
  echo "[warn] kustomize apply failed; falling back to plain apply (no S3 patch)"
  kubectl apply -f "${ROOT}/config/metadata/metadata.yaml"
}

echo "[6/6] wait for rollouts"
for d in redis-master minio fake-vllm metadata-service; do
  kubectl -n "${NS}" rollout status "deploy/${d}" --timeout=180s || true
done

echo ""
kubectl -n "${NS}" get pods -o wide
echo ""

# Port-forward metadata to localhost:8090
[ -f "${PF_PID}" ] && kill "$(cat "${PF_PID}")" 2>/dev/null || true
kubectl -n "${NS}" port-forward svc/metadata-service 8090:8090 \
    >"${RUN_DIR}/logs/portforward.log" 2>&1 &
echo $! > "${PF_PID}"
sleep 1

echo "[ok] metadata at http://127.0.0.1:8090  (pid $(cat "${PF_PID}"))"
echo "[ok] minio  console http://127.0.0.1:9001 (run: kubectl -n ${NS} port-forward svc/minio 9001:9001)"
echo "[ok] tail logs:  kubectl -n ${NS} logs -f deploy/metadata-service"
