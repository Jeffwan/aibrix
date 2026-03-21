#!/usr/bin/env bash
# Start AIBrix gateway locally as two bare processes: Envoy + gateway-plugin.
# No Docker, no Kubernetes required.
#
# Prerequisites:
#   1. Build the gateway-plugin binary:
#      make build-gateway-plugins-nozmq
#   2. Install Envoy:
#      macOS: brew install envoy
#      Linux: see https://www.envoyproxy.io/docs/envoy/latest/start/install
#   3. (Optional) Start Redis for rate limiting:
#      redis-server
#
# Usage:
#   ./run-local.sh                          # use default configs
#   ./run-local.sh -e my-endpoints.yaml     # custom endpoints config

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PID_FILE="${SCRIPT_DIR}/.pids"
LOG_DIR="${SCRIPT_DIR}/logs"

# Defaults
GATEWAY_BINARY="${PROJECT_ROOT}/bin/gateway-plugins"
ENVOY_BINARY="envoy"
ENVOY_CONFIG="${SCRIPT_DIR}/configs/envoy.yaml"
ENDPOINTS_CONFIG="${SCRIPT_DIR}/configs/endpoints.yaml"
ROUTING_ALGORITHM="${ROUTING_ALGORITHM:-random}"

# Parse arguments
while getopts "e:c:" opt; do
    case $opt in
        e) ENDPOINTS_CONFIG="$OPTARG" ;;
        c) ENVOY_CONFIG="$OPTARG" ;;
        *) echo "Usage: $0 [-e endpoints.yaml] [-c envoy.yaml]"; exit 1 ;;
    esac
done

# Check prerequisites
echo "Checking prerequisites..."

if [ ! -f "${GATEWAY_BINARY}" ]; then
    echo "ERROR: gateway-plugins binary not found at ${GATEWAY_BINARY}"
    echo "Build it with: make build-gateway-plugins-nozmq"
    exit 1
fi

if ! command -v "${ENVOY_BINARY}" &> /dev/null; then
    echo "ERROR: envoy not found in PATH"
    echo "Install with:"
    echo "  macOS: brew install envoy"
    echo "  Linux: https://www.envoyproxy.io/docs/envoy/latest/start/install"
    exit 1
fi

if [ ! -f "${ENVOY_CONFIG}" ]; then
    echo "ERROR: Envoy config not found at ${ENVOY_CONFIG}"
    exit 1
fi

if [ ! -f "${ENDPOINTS_CONFIG}" ]; then
    echo "ERROR: Endpoints config not found at ${ENDPOINTS_CONFIG}"
    exit 1
fi

# Clean up any previous run
if [ -f "${PID_FILE}" ]; then
    echo "Stopping previous processes..."
    "${SCRIPT_DIR}/stop-local.sh" 2>/dev/null || true
fi

# Create log directory
mkdir -p "${LOG_DIR}"

echo "Starting AIBrix gateway (local mode)..."
echo "  Envoy config:     ${ENVOY_CONFIG}"
echo "  Endpoints config: ${ENDPOINTS_CONFIG}"
echo "  Routing algorithm: ${ROUTING_ALGORITHM}"
echo ""

# Start gateway-plugin
echo "Starting gateway-plugin (gRPC :50052, metrics :8080)..."
ROUTING_ALGORITHM="${ROUTING_ALGORITHM}" "${GATEWAY_BINARY}" \
    --standalone \
    --endpoints-config="${ENDPOINTS_CONFIG}" \
    --grpc-bind-address=":50052" \
    --metrics-bind-address=":8080" \
    > "${LOG_DIR}/gateway-plugin.log" 2>&1 &
GATEWAY_PID=$!
echo "  PID: ${GATEWAY_PID}"

# Wait briefly for gateway to start
sleep 1
if ! kill -0 "${GATEWAY_PID}" 2>/dev/null; then
    echo "ERROR: gateway-plugin failed to start. Check ${LOG_DIR}/gateway-plugin.log"
    cat "${LOG_DIR}/gateway-plugin.log"
    exit 1
fi

# Start Envoy
echo "Starting Envoy (HTTP :10080, admin :9901)..."
"${ENVOY_BINARY}" \
    -c "${ENVOY_CONFIG}" \
    --log-level warn \
    > "${LOG_DIR}/envoy.log" 2>&1 &
ENVOY_PID=$!
echo "  PID: ${ENVOY_PID}"

# Wait briefly for envoy to start
sleep 1
if ! kill -0 "${ENVOY_PID}" 2>/dev/null; then
    echo "ERROR: Envoy failed to start. Check ${LOG_DIR}/envoy.log"
    cat "${LOG_DIR}/envoy.log"
    # Clean up gateway
    kill "${GATEWAY_PID}" 2>/dev/null || true
    exit 1
fi

# Save PIDs
echo "${GATEWAY_PID} ${ENVOY_PID}" > "${PID_FILE}"

echo ""
echo "AIBrix gateway is running!"
echo ""
echo "Endpoints:"
echo "  HTTP API:          http://localhost:10080/v1/chat/completions"
echo "  Envoy Admin:       http://localhost:9901"
echo "  Gateway Metrics:   http://localhost:8080/metrics"
echo "  Health Check:      http://localhost:10080/healthz"
echo ""
echo "Test with:"
echo "  curl http://localhost:10080/v1/chat/completions \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\": \"Qwen/Qwen2.5-1.5B-Instruct\", \"messages\": [{\"role\": \"user\", \"content\": \"Hello\"}]}'"
echo ""
echo "Logs:"
echo "  Gateway: tail -f ${LOG_DIR}/gateway-plugin.log"
echo "  Envoy:   tail -f ${LOG_DIR}/envoy.log"
echo ""
echo "Stop with: ./stop-local.sh"
