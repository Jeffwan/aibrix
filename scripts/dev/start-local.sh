#!/usr/bin/env bash
# Setup A: pure local processes for testing the Aibrix metadata server.
# No Docker, no Kubernetes. Requires: redis-server on PATH, Python deps installed.
#
# Usage:
#   ./scripts/dev/start-local.sh         # start all
#   ./scripts/dev/start-local.sh stop    # kill all
#   ./scripts/dev/start-local.sh status  # show pids & ports

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RUN_DIR="${ROOT}/.run"
LOG_DIR="${RUN_DIR}/logs"
PID_REDIS="${RUN_DIR}/redis.pid"
PID_VLLM="${RUN_DIR}/fake_vllm.pid"
PID_META="${RUN_DIR}/metadata.pid"
mkdir -p "${RUN_DIR}" "${LOG_DIR}" "${ROOT}/.storage"

REDIS_PORT="${REDIS_PORT:-6379}"
FAKE_VLLM_PORT="${FAKE_VLLM_PORT:-8000}"
METADATA_PORT="${METADATA_PORT:-8090}"

stop_all() {
  for f in "${PID_META}" "${PID_VLLM}" "${PID_REDIS}"; do
    [ -f "$f" ] || continue
    pid="$(cat "$f")" || true
    if [ -n "${pid}" ] && kill -0 "${pid}" 2>/dev/null; then
      kill "${pid}" || true
    fi
    rm -f "$f"
  done
  echo "[stop] all stopped"
}

status() {
  for name in redis fake_vllm metadata; do
    f="${RUN_DIR}/${name}.pid"
    if [ -f "$f" ] && kill -0 "$(cat "$f")" 2>/dev/null; then
      echo "  ${name}: running (pid $(cat "$f"))"
    else
      echo "  ${name}: not running"
    fi
  done
  echo "  redis    -> 127.0.0.1:${REDIS_PORT}"
  echo "  fake llm -> http://127.0.0.1:${FAKE_VLLM_PORT}"
  echo "  metadata -> http://127.0.0.1:${METADATA_PORT}"
  echo "  logs     -> ${LOG_DIR}/"
}

case "${1:-start}" in
  stop)   stop_all; exit 0 ;;
  status) status; exit 0 ;;
  start)  ;;
  *) echo "usage: $0 [start|stop|status]"; exit 1 ;;
esac

# 1. Redis (system process, no docker)
if ! command -v redis-server >/dev/null; then
  echo "[fatal] redis-server not on PATH. Install: 'apt install redis-server' or 'brew install redis'"
  exit 1
fi
if [ -f "${PID_REDIS}" ] && kill -0 "$(cat "${PID_REDIS}")" 2>/dev/null; then
  echo "[redis] already running (pid $(cat "${PID_REDIS}"))"
else
  redis-server --port "${REDIS_PORT}" --daemonize no \
      --logfile "${LOG_DIR}/redis.log" --dir "${RUN_DIR}" \
      >/dev/null 2>&1 &
  echo $! > "${PID_REDIS}"
  echo "[redis] started on :${REDIS_PORT} (pid $(cat "${PID_REDIS}"))"
fi

# Wait for redis ready
for _ in $(seq 1 20); do
  redis-cli -p "${REDIS_PORT}" ping 2>/dev/null | grep -q PONG && break
  sleep 0.2
done

# 2. Fake inference (only if not already there)
if [ -f "${PID_VLLM}" ] && kill -0 "$(cat "${PID_VLLM}")" 2>/dev/null; then
  echo "[fake_vllm] already running (pid $(cat "${PID_VLLM}"))"
else
  python "${ROOT}/scripts/dev/fake_inference.py" --port "${FAKE_VLLM_PORT}" \
      >"${LOG_DIR}/fake_vllm.log" 2>&1 &
  echo $! > "${PID_VLLM}"
  echo "[fake_vllm] started on :${FAKE_VLLM_PORT} (pid $(cat "${PID_VLLM}"))"
fi

# Wait for fake vllm ready
for _ in $(seq 1 30); do
  curl -sf "http://127.0.0.1:${FAKE_VLLM_PORT}/health" >/dev/null && break
  sleep 0.2
done

# 3. Metadata server
if [ -f "${PID_META}" ] && kill -0 "$(cat "${PID_META}")" 2>/dev/null; then
  echo "[metadata] already running (pid $(cat "${PID_META}"))"
else
  cd "${ROOT}/python/aibrix"
  REDIS_HOST=127.0.0.1 \
  REDIS_PORT="${REDIS_PORT}" \
  STORAGE_TYPE=local \
  LOCAL_STORAGE_PATH="${ROOT}/.storage" \
  INFERENCE_ENGINE_ENDPOINT="http://127.0.0.1:${FAKE_VLLM_PORT}" \
  AIBRIX_BATCH_JOB_STORE_ENABLED=0 \
    python "${ROOT}/scripts/dev/run_metadata.py" --port "${METADATA_PORT}" \
      >"${LOG_DIR}/metadata.log" 2>&1 &
  echo $! > "${PID_META}"
  echo "[metadata] started on :${METADATA_PORT} (pid $(cat "${PID_META}"))"
fi

# Wait for metadata ready
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:${METADATA_PORT}/readyz" >/dev/null && break
  sleep 0.25
done

echo ""
status
echo ""
echo "[ok] tail logs with:  tail -F ${LOG_DIR}/*.log"
echo "[ok] try:             curl -s localhost:${METADATA_PORT}/readyz"
