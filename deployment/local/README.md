# AIBrix Gateway - Local Mode

Run the AIBrix gateway (Envoy + gateway-plugin) as two bare processes on your machine. No Docker, no Kubernetes - just native binaries with static config to discover vLLM engine instances directly.

This is ideal for:
- Local development and debugging of routing algorithms
- Single-node testing without container overhead
- Quick validation of gateway behavior

## Architecture

```
                                 ┌────────────────────────┐
  curl :10080                    │    gateway-plugin      │
       │                         │    (gRPC :50052)       │
       ▼                         │                        │
┌─────────────┐  ext_proc gRPC   │  --standalone          │
│   Envoy     │ ───────────────► │  --endpoints-config    │
│  (:10080)   │                  │                        │
│             │ ◄─ target-pod ── │  selects best backend  │
│  ORIGINAL   │    header        └────────────────────────┘
│  _DST       │
│  cluster    │ ──── route to ──► vLLM engine(s)
└─────────────┘    selected IP    (e.g., 127.0.0.1:8000)
```

Envoy receives HTTP requests and forwards headers/body to the gateway-plugin via ext_proc. The plugin extracts the model name, looks up available backends from `endpoints.yaml`, selects the best one using the configured routing algorithm, and returns the target address via the `target-pod` header. Envoy then routes the request to that address using an `ORIGINAL_DST` cluster.

## Prerequisites

### 1. Build the gateway-plugin binary

```bash
make build-gateway-plugins-nozmq
```

This produces `bin/gateway-plugins` - a pure Go binary (no ZMQ/CGO dependencies).

### 2. Install Envoy

**macOS:**
```bash
brew install envoy
```

**Linux (Ubuntu/Debian):**
```bash
# See https://www.envoyproxy.io/docs/envoy/latest/start/install
```

### 3. Start your vLLM engine

```bash
# Example: run vLLM on port 8000
python -m vllm.entrypoints.openai.api_server \
    --model Qwen/Qwen2.5-1.5B-Instruct \
    --port 8000
```

### 4. (Optional) Redis

Redis is **not required** in local mode. Without Redis, rate limiting is disabled but routing works normally.

If you want rate limiting:
```bash
redis-server
```

## Quick Start

```bash
cd deployment/local

# Edit endpoints to match your vLLM setup
vim configs/endpoints.yaml

# Start
./run-local.sh

# Test
curl http://localhost:10080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen2.5-1.5B-Instruct",
    "messages": [{"role": "user", "content": "Hello"}]
  }'

# Stop
./stop-local.sh
```

## Configuration

### Endpoints (`configs/endpoints.yaml`)

Define your vLLM backend addresses:

```yaml
# Single backend
models:
  - name: "Qwen/Qwen2.5-1.5B-Instruct"
    endpoints:
      - "127.0.0.1:8000"

# Multiple backends (gateway routes across them)
models:
  - name: "Qwen/Qwen2.5-1.5B-Instruct"
    endpoints:
      - "192.168.1.10:8000"
      - "192.168.1.11:8000"

# P/D disaggregated serving
models:
  - name: "Qwen/Qwen2.5-72B"
    rolesets:
      - name: default
        prefill:
          - "192.168.1.10:8000"
        decode:
          - "192.168.1.11:8000"
```

### Routing Algorithm

Set via environment variable before starting:

```bash
ROUTING_ALGORITHM=round_robin ./run-local.sh
```

Available algorithms: `random`, `round_robin`, `least_request`, `prefix_cache_aware`, etc.

### Custom configs

```bash
./run-local.sh -e /path/to/my-endpoints.yaml -c /path/to/my-envoy.yaml
```

## Endpoints

| Endpoint | Port | Description |
|----------|------|-------------|
| HTTP API | 10080 | Send inference requests here |
| Envoy Admin | 9901 | Envoy admin interface (stats, config dump) |
| Gateway Metrics | 8080 | Prometheus metrics from gateway-plugin |
| Health Check | 10080/healthz | Envoy health |

## Logs

```bash
tail -f deployment/local/logs/gateway-plugin.log
tail -f deployment/local/logs/envoy.log
```

## Troubleshooting

**"no healthy upstream"** - The vLLM backend is not reachable at the address in `endpoints.yaml`. Verify the engine is running and the address is correct.

**"ext_proc gRPC error"** - The gateway-plugin is not running or crashed. Check `logs/gateway-plugin.log`.

**Envoy won't start** - Check `logs/envoy.log`. Common issue: port conflict on 10080 or 9901.

**Routing not working** - Check that the model name in your request matches exactly what's in `endpoints.yaml`.
