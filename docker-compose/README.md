# AIBrix Docker-Compose - Single-Node Deployment

Simplified AIBrix deployment for single-node setups without Kubernetes complexity.

## What's Included

- **Gateway Router** - Smart routing with P/D disaggregation
- **KVCache Offloading** - Distributed cache management
- **P/D Disaggregation** - Separate prefill/decode engines
- **Metadata Service** - Model and file management
- **Redis** - State storage

## Quick Start

```bash
# 1. Configure
cp .env.example .env
vim .env  # Set MODEL_DIR and GPU assignments

# 2. Start
docker-compose up -d

# 3. Test
curl http://localhost/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.1-8B-Instruct",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

## Architecture

```
Client → Envoy (80) → Gateway (50052) → Prefill (8000) / Decode (8001)
                            ↓
                       Redis + Metadata (8090)
```

## Configuration (.env)

```bash
# Required: Point to your model files
MODEL_DIR=/path/to/models
MODEL_NAME=meta-llama/Llama-3.1-8B-Instruct

# GPU assignment (2 GPUs recommended)
PREFILL_GPU=0  # Compute-heavy
DECODE_GPU=1   # Memory-heavy

# Transfer backend: tcp, nixl, mooncake
TRANSFER_BACKEND=tcp

# Optional: KVCache offloading
ENABLE_KVCACHE=false
```

## Services

| Service | Port | Purpose |
|---------|------|---------|
| proxy | 80 | HTTP entry point |
| gateway | 50052 | gRPC routing |
| prefill-engine | 8000 | Initial token generation |
| decode-engine | 8001 | Token generation |
| metadata-service | 8090 | Model/file API |
| redis | 6379 | State storage |

## Common Tasks

**View logs:**
```bash
docker-compose logs -f gateway prefill-engine decode-engine
```

**Check health:**
```bash
docker-compose ps
curl http://localhost/health
```

**Scale decode engines:**
```bash
docker-compose up -d --scale decode-engine=3
```

**Stop:**
```bash
docker-compose down
```

## Troubleshooting

**GPU not found:**
```bash
nvidia-smi
docker-compose exec prefill-engine nvidia-smi
```

**Model not loading:**
```bash
docker-compose exec prefill-engine ls -la /models
```

**Connection errors:**
```bash
docker-compose logs gateway
docker-compose exec gateway ping prefill-engine
```

## vs Kubernetes

| Feature | Docker-Compose | Kubernetes |
|---------|----------------|------------|
| Setup | Simple | Complex |
| Multi-node | No | Yes |
| Auto-scaling | No | Yes |
| Best for | Dev/test, single-node | Production |

For production multi-node deployments, use the Helm chart: `helm install aibrix /path/to/aibrix/dist/chart`
