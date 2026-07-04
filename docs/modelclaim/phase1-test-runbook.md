# ModelClaim Phase 1 Test Runbook

This runbook covers the manual validation path used for the phase-1 ModelClaim
implementation.

## 1. Fresh Clone Validation

```bash
git clone git@github.com:Jeffwan/aibrix.git /tmp/aibrix-modelclaim
cd /tmp/aibrix-modelclaim
git checkout feat/kvcached-sleep-integration-0704

go test ./pkg/controller/modelclaim ./pkg/cache ./pkg/types ./pkg/utils ./pkg/plugins/gateway

cd python/aibrix
python3 -m venv /tmp/aibrix-py
. /tmp/aibrix-py/bin/activate
pip install -U pip
pip install -e . pytest ruff httpx
ruff check \
  aibrix/openapi/protocol.py \
  aibrix/runtime/model_runtime.py \
  aibrix/runtime/model_runtime_api.py \
  aibrix/runtime/model_runtime_metrics.py \
  tests/runtime/test_model_runtime.py \
  tests/runtime/test_model_runtime_metrics.py
pytest tests/runtime/test_model_runtime.py tests/runtime/test_model_runtime_metrics.py -q
```

## 2. Lambda A10 Setup

Create or reuse a Lambda A10 instance with Docker and NVIDIA drivers. Install
AIBrix's Lambda development dependencies:

```bash
git clone git@github.com:Jeffwan/aibrix.git ~/aibrix-test/aibrix
cd ~/aibrix-test/aibrix
git checkout feat/kvcached-sleep-integration-0704

bash hack/lambda-cloud/install.sh
bash hack/lambda-cloud/verify.sh

export PATH=/usr/local/go/bin:$HOME/.local/bin:$PATH
minikube start --driver=docker --container-runtime=docker --gpus=all --cpus=8 --memory=16g
kubectl wait --for=condition=Ready node/minikube --timeout=180s
kubectl get node minikube -o json | jq '.status.allocatable["nvidia.com/gpu"]'
```

## 3. Runtime API + vLLM/kvcached

Build an AIBrix runtime image on top of the kvcached vLLM image:

```dockerfile
FROM ghcr.io/ovg-project/kvcached-vllm:latest
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
RUN python3 -m pip install --no-cache-dir --no-deps \
    structlog==24.4.0 fastapi==0.112.4 uvicorn==0.30.6 \
    prometheus-client==0.20.0 pydantic-settings==2.6.1 \
    python-multipart==0.0.20 tenacity==9.1.4
COPY . /opt/aibrix-src
RUN python3 -m pip install --no-cache-dir --no-deps -e /opt/aibrix-src/python/aibrix
```

```bash
docker build -f /tmp/Dockerfile.aibrix-runtime-dev -t aibrix/runtime:dev .

docker run -d --name aibrix-runtime-test --gpus all --network host --ipc=host \
  -v "$HOME/.cache/huggingface:/root/.cache/huggingface" \
  -e AIBRIX_MODEL_RUNTIME_MOCK=0 \
  -e ENABLE_KVCACHED=true \
  -e KVCACHED_AUTOPATCH=1 \
  -e HF_HOME=/root/.cache/huggingface \
  aibrix/runtime:dev aibrix_runtime --host 0.0.0.0 --port 8080

curl -sS http://127.0.0.1:8080/v1/runtime/models | python3 -m json.tool
```

Activate a small model:

```bash
cat >/tmp/activate-qwen.json <<'JSON'
{
  "model_name": "qwen3-0.6b",
  "artifact_url": "hf://Qwen/Qwen3-0.6B",
  "engine": "vllm",
  "ipc_name": "kvc_qwen3-0.6b",
  "engine_config": {
    "args": {
      "--max-model-len": "2048",
      "--gpu-memory-utilization": "0.45"
    }
  }
}
JSON

curl -sS -X POST http://127.0.0.1:8080/v1/runtime/models/activate \
  -H 'content-type: application/json' \
  -d @/tmp/activate-qwen.json | python3 -m json.tool

curl -sS http://127.0.0.1:8080/v1/runtime/models | python3 -m json.tool
curl -sS http://127.0.0.1:20000/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"qwen3-0.6b","messages":[{"role":"user","content":"Say hi in one short sentence."}],"max_tokens":16}' \
  | python3 -m json.tool
ls -l /dev/shm | grep kvc
```

Deactivate:

```bash
curl -sS -X POST http://127.0.0.1:8080/v1/runtime/models/deactivate \
  -H 'content-type: application/json' \
  -d '{"model_name":"qwen3-0.6b","mode":"stop"}' | python3 -m json.tool
docker rm -f aibrix-runtime-test
```

Expected:

- vLLM starts with `--enable-sleep-mode`.
- kvcached autopatch logs are present.
- `/v1/runtime/models` reports `ready: true`, a normalized IPC name, and non-zero
  KV capacity once the engine is up.
- OpenAI-compatible chat completion succeeds on the returned port.

## 4. Controller + ModelClaim on minikube

Build controller and a lightweight mock runtime into the minikube Docker daemon:

```bash
eval "$(minikube docker-env)"
IMAGE_TAG=dev AIBRIX_CONTAINER_REGISTRY_NAMESPACE=aibrix make docker-build-controller-manager
docker tag aibrix/controller-manager:dev aibrix/controller-manager:nightly
docker build -f build/container/Dockerfile.python -t aibrix/runtime:dev .
```

Install CRDs and the AIBrix controller. If Envoy Gateway CRDs are not installed,
`kubectl apply -k config/default` may report gateway resource mapping errors
after creating the controller resources; for ModelClaim controller testing, wait
only on the controller deployment:

```bash
kubectl apply -k config/crd --server-side
kubectl apply -k config/default || true
kubectl -n aibrix-system rollout status deployment/aibrix-controller-manager --timeout=180s
kubectl -n aibrix-system logs deploy/aibrix-controller-manager --tail=100 | grep model-claim
```

Create a mock runtime pool:

```bash
kubectl apply -f - <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: warm-runtime-pool-b300
  namespace: default
  labels:
    pool.aibrix.ai/name: b300-pool-a
    pool.aibrix.ai/enabled: "true"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: warm-runtime-pool-b300
  template:
    metadata:
      labels:
        app: warm-runtime-pool-b300
        pool.aibrix.ai/name: b300-pool-a
        pool.aibrix.ai/enabled: "true"
    spec:
      containers:
      - name: aibrix-runtime
        image: aibrix/runtime:dev
        imagePullPolicy: IfNotPresent
        ports:
        - name: runtime
          containerPort: 8080
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 3
          periodSeconds: 5
        env:
        - name: AIBRIX_MODEL_RUNTIME_MOCK
          value: "1"
        - name: ENABLE_KVCACHED
          value: "false"
YAML

kubectl rollout status deployment/warm-runtime-pool-b300 --timeout=180s
POD=$(kubectl get pod -l app=warm-runtime-pool-b300 -o jsonpath='{.items[0].metadata.name}')
kubectl exec "$POD" -- python -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080/healthz").read().decode())'
```

Create and verify a ModelClaim:

```bash
kubectl apply -f - <<'YAML'
apiVersion: model.aibrix.ai/v1alpha1
kind: ModelClaim
metadata:
  name: qwen3-0-6b
  namespace: default
spec:
  modelName: qwen3-0.6b
  podSelector:
    matchLabels:
      pool.aibrix.ai/name: b300-pool-a
  artifactURL: huggingface://Qwen/Qwen3-0.6B
  engine: vllm
  replicas: 1
  engineConfig:
    args:
      --max-model-len: "2048"
      --gpu-memory-utilization: "0.45"
YAML

kubectl get modelclaim qwen3-0-6b -o json | jq '{phase:.status.phase, desired:.status.desiredReplicas, ready:.status.readyReplicas, instances:.status.instances}'
kubectl exec "$POD" -- python -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080/v1/runtime/models").read().decode())' | python3 -m json.tool
kubectl get pod "$POD" -o json | jq '.metadata.annotations'
```

Expected:

- `status.phase` is `Active`.
- `status.readyReplicas` is `1`.
- runtime list contains `qwen3-0.6b` on port `20000`.
- pod annotation `modelclaim.aibrix.ai/qwen3-0-6b` contains
  `{"model":"qwen3-0.6b","port":20000}`.

Delete and verify cleanup:

```bash
kubectl delete modelclaim qwen3-0-6b --timeout=120s
kubectl exec "$POD" -- python -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080/v1/runtime/models").read().decode())' | python3 -m json.tool
kubectl get pod "$POD" -o json | jq '.metadata.annotations'
```

Expected:

- runtime model list is empty.
- ModelClaim routing annotation is removed.

## 5. Cleanup

```bash
kubectl delete deployment warm-runtime-pool-b300 --ignore-not-found=true
minikube delete
```
