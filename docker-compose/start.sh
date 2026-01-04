#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "==============================================="
echo "AIBrix Docker-Compose Deployment"
echo "==============================================="
echo ""

# Check if .env exists
if [ ! -f .env ]; then
    echo -e "${YELLOW}Warning: .env file not found${NC}"
    echo "Creating from .env.example..."
    cp .env.example .env
    echo -e "${RED}Please edit .env file with your configuration before proceeding${NC}"
    echo "Required settings:"
    echo "  - MODEL_DIR: Path to your model files"
    echo "  - MODEL_NAME: Model identifier"
    echo "  - PREFILL_GPU: GPU ID for prefill engine"
    echo "  - DECODE_GPU: GPU ID for decode engine"
    exit 1
fi

# Source environment variables
source .env

# Check prerequisites
echo "Checking prerequisites..."

# Check Docker
if ! command -v docker &> /dev/null; then
    echo -e "${RED}Error: Docker is not installed${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} Docker found"

# Check Docker Compose
if ! docker compose version &> /dev/null; then
    echo -e "${RED}Error: Docker Compose is not installed${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} Docker Compose found"

# Check NVIDIA runtime
if ! docker run --rm --gpus all nvidia/cuda:11.8.0-base-ubuntu22.04 nvidia-smi &> /dev/null; then
    echo -e "${RED}Error: NVIDIA Docker runtime not available${NC}"
    echo "Please install nvidia-container-toolkit"
    exit 1
fi
echo -e "${GREEN}✓${NC} NVIDIA runtime available"

# Check model directory
if [ -z "$MODEL_DIR" ]; then
    echo -e "${RED}Error: MODEL_DIR not set in .env${NC}"
    exit 1
fi

if [ ! -d "$MODEL_DIR" ]; then
    echo -e "${YELLOW}Warning: Model directory not found: $MODEL_DIR${NC}"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
else
    echo -e "${GREEN}✓${NC} Model directory found: $MODEL_DIR"
fi

# Display configuration
echo ""
echo "Configuration:"
echo "  Model: $MODEL_NAME"
echo "  Model Directory: $MODEL_DIR"
echo "  Prefill GPU: $PREFILL_GPU"
echo "  Decode GPU: $DECODE_GPU"
echo "  Transfer Backend: $TRANSFER_BACKEND"
echo "  KVCache Offload: $ENABLE_KVCACHE"
echo ""

# Confirm before starting
read -p "Start AIBrix services? (Y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Nn]$ ]]; then
    exit 0
fi

# Pull latest images
echo ""
echo "Pulling Docker images..."
docker compose pull

# Start services
echo ""
echo "Starting services..."
docker compose up -d

# Wait for services to be healthy
echo ""
echo "Waiting for services to be healthy..."
sleep 5

# Check service health
echo ""
echo "Service Status:"
docker compose ps

# Wait for gateway to be ready
echo ""
echo "Waiting for gateway to be ready..."
timeout=60
elapsed=0
while [ $elapsed -lt $timeout ]; do
    if docker compose exec -T gateway grpc_health_probe -addr=:50052 &> /dev/null 2>&1; then
        echo -e "${GREEN}✓${NC} Gateway is ready"
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
    echo -n "."
done
echo ""

if [ $elapsed -ge $timeout ]; then
    echo -e "${YELLOW}Warning: Gateway health check timed out${NC}"
fi

# Display endpoints
echo ""
echo "==============================================="
echo -e "${GREEN}AIBrix is running!${NC}"
echo "==============================================="
echo ""
echo "Endpoints:"
echo "  HTTP API:      http://localhost/v1/chat/completions"
echo "  Metadata API:  http://localhost/v1/models"
echo "  Metrics:       http://localhost:8080/metrics"
echo "  Envoy Admin:   http://localhost:9901/"
echo ""
echo "Test with:"
echo "  curl http://localhost/v1/chat/completions \\"
echo "    -H \"Content-Type: application/json\" \\"
echo "    -d '{\"model\": \"$MODEL_NAME\", \"messages\": [{\"role\": \"user\", \"content\": \"Hello!\"}]}'"
echo ""
echo "View logs:"
echo "  docker compose logs -f"
echo ""
echo "Stop services:"
echo "  docker compose down"
echo ""
