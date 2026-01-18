# vLLM-Omni Analysis within AIBrix

**Repository Context:** vllm-omni capabilities integrated into AIBrix
**Related:** https://github.com/vllm-project/vllm-omni

## What is vLLM-Omni?

vLLM-Omni extends standard vLLM for "easy, fast, and cheap omni-modality model serving."

### Standard vLLM vs vLLM-Omni

| Feature | Standard vLLM | vLLM-Omni |
|---------|--------------|-----------|
| Modalities | Text-only | Text, Image, Video, Audio |
| Generation | Autoregressive only | Autoregressive + Non-autoregressive |
| Output Types | Text tokens | Text, Images, Videos |
| Parallelism | TP, PP | TP, PP, DP, Expert parallelism |
| Architecture | Single model | Encoder-Decoder pipelines |

### Key Extensions

1. **Multi-modal Processing:** Handles text, image, video, and audio simultaneously
2. **Non-autoregressive Models:** Supports Diffusion Transformers (DiT) and parallel generation
3. **Heterogeneous Outputs:** Enables diverse generation from multiple modalities
4. **Advanced Parallelism:** Tensor, pipeline, data, and expert parallelism support
5. **OmniConnector:** Disaggregated inference support

## AIBrix Integration Architecture

### High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Client Applications                          │
│         (OpenAI SDK, curl, Python requests, etc.)              │
└──────────────────┬──────────────────────────────────────────────┘
                   │
        ┌──────────▼──────────┐
        │  Envoy Gateway      │
        │  (Port: 80/443)     │
        └──────────┬──────────┘
                   │
    ┌──────────────▼──────────────────┐
    │   AIBrix Gateway Plugin          │
    │  - Request Router               │
    │  - Rate Limiting (Redis)        │
    │  - Routing Algorithms           │
    │  - Metrics Collection           │
    └──────────────┬───────────────────┘
                   │
        ┌──────────▼──────────────────────────┐
        │    Model Serving Pods               │
        │  ┌─────────────────────────────┐    │
        │  │ vLLM-Omni Container         │    │
        │  │ ┌─────────────────────┐     │    │
        │  │ │ Multimodal Pipeline │     │    │
        │  │ │ - Text Encoder      │     │    │
        │  │ │ - Image Encoder     │     │    │
        │  │ │ - Audio Encoder     │     │    │
        │  │ │ - Video Processor   │     │    │
        │  │ │ - LLM Backbone      │     │    │
        │  │ │ - Token Generator   │     │    │
        │  │ └─────────────────────┘     │    │
        │  └─────────────────────────────┘    │
        └─────────────────────────────────────┘
```

### Control Plane Components

#### 1. Model Adapter (LoRA) Controller

**File:** `python/aibrix/aibrix/openapi/engine/vllm.py`

```python
class VLLMEngine(InferenceEngine):
    async def load_lora_adapter(
        self, request: LoadLoraAdapterRequest
    ) -> Union[ErrorResponse, str]:
        """Load LoRA adapter dynamically."""

    async def unload_lora_adapter(
        self, request: UnloadLoraAdapterRequest
    ) -> Union[ErrorResponse, str]:
        """Unload LoRA adapter."""

    async def list_models(self) -> Union[ErrorResponse, str]:
        """List available models including LoRA adapters."""
```

**Key Features:**
- Multi-LoRA-per-pod deployments
- Retry logic for adapter loading
- LoRA request validation
- Adapter lifecycle management

#### 2. Model Router Controller

**File:** `pkg/controller/modelrouter/modelrouter_controller.go`

```go
// Supported model paths for routing
var supportedPaths = []string{
    "/v1/completions",
    "/v1/chat/completions",
    "/v1/embeddings",
    "/v1/rerank",
    "/generate",
    "/generatevideo",
    "/v1/audio/transcriptions",
    "/v1/audio/translations",
}
```

**Responsibilities:**
- Creates HTTPRoute resources for model traffic
- Supports multiple workload types: Deployment, RayClusterFleet, LeaderWorkerSet
- Routes inference requests to appropriate pods

#### 3. LLM-Specific Autoscaler

**File:** `pkg/controller/podautoscaler/`

- Real-time, second-level scaling
- KV cache utilization metrics
- Inference-aware metrics for dynamic allocation

### Data Plane: Request Processing

#### Gateway External Processor

**File:** `pkg/plugins/gateway/gateway.go`

```go
type Server struct {
    redisClient  redis.UniversalClient
    ratelimiter  ratelimiter.RateLimiter
    client       client.Client
    gatewayapi   gatewayapi_client.Interface
    cache        cache.Cache
    metricsServer *metrics.Server
}

// Stream-based request processing
func (s *Server) Process(stream extproc.ExternalProcessor_ProcessServer) error {
    for {
        req, err := stream.Recv()
        switch v := req.Request.(type) {
        case *extproc.ProcessingRequest_RequestHeaders:
            // Extract user, model, routing strategy
        case *extproc.ProcessingRequest_RequestBody:
            // Parse request, select target pod
        case *extproc.ProcessingRequest_ResponseHeaders:
            // Validate response headers
        case *extproc.ProcessingRequest_ResponseBody:
            // Update metrics
        }
    }
}
```

### Supported Input Modalities

#### 1. Image Processing

**File:** `samples/multimodality/vllm/send_file_base64.py`

```python
{
    "type": "image_url",
    "image_url": {
        "url": "data:image/jpeg;base64,..."  # Base64
        # or "url": "https://..."            # Remote URL
        # or "url": "file://..."             # Local file
    }
}
```

**Environment:** `VLLM_IMAGE_FETCH_TIMEOUT=600`

#### 2. Video Processing

```python
{
    "type": "video_url",
    "video_url": {
        "url": "https://example.com/video.mp4"
    }
}
```

**Environment:** `VLLM_VIDEO_FETCH_TIMEOUT=1200`

#### 3. Audio Processing

**Endpoints:**
- `/v1/audio/transcriptions`
- `/v1/audio/translations`

**Environment:** `VLLM_AUDIO_FETCH_TIMEOUT=1200`

### Supported Output Modalities

#### 1. Text Generation

```
POST /v1/completions
POST /v1/chat/completions
```

#### 2. Image Generation (xDiT Integration)

**Directory:** `samples/multimodality/xDiT/`

```
POST /v1/images/generations
```

**Supported Models:**
- Stable Diffusion 3 (SD-3)
- HunyuanDiT
- Custom DiT models

#### 3. Video Generation

```
POST /v1/video/generations
```

**Supported Models:**
- CogVideoX-2b
- HunyuanVideo

### Supported Models

| Type | Models | Deployment Config |
|------|--------|------------------|
| Vision-Language | Qwen2.5-VL-7B, LLaVA-7B | `samples/multimodality/vllm/qwen-vl.yaml` |
| Audio | Qwen-Audio | `samples/multimodality/vllm/qwen-audio.yaml` |
| Omnimodal | Qwen-Omni | Full multimodal support |
| Image Gen | SD-3, HunyuanDiT | `samples/multimodality/xDiT/` |
| Video Gen | CogVideoX, HunyuanVideo | `samples/multimodality/xDiT/` |

### Deployment Configuration

**File:** `samples/multimodality/vllm/qwen-vl.yaml`

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    model.aibrix.ai/name: qwen-vl
    model.aibrix.ai/port: "8000"
    model.aibrix.ai/engine: "vllm"
spec:
  template:
    spec:
      containers:
      - name: vllm
        image: vllm/vllm-openai:latest
        args:
        - --model=$(MODEL_PATH)
        - --enable-prefix-caching
        - --allowed-local-media-path=/data
        env:
        - name: VLLM_IMAGE_FETCH_TIMEOUT
          value: "600"
        - name: VLLM_VIDEO_FETCH_TIMEOUT
          value: "1200"
```

### Disaggregated Inference

**File:** `samples/disaggregation/vllm/disagg_proxy_server.py`

AIBrix supports **prefill-decode separation**:

```python
# Prefill phase
prefill_response = await forward_request(
    prefill_endpoint,
    request={
        "model": model,
        "prompt": prompt,
        "max_tokens": 1,  # Just generate KV cache
    }
)

# Decode phase with KV transfer
decode_response = await forward_request(
    decode_endpoint,
    request={
        "model": model,
        "prompt": prompt,
        "max_tokens": max_tokens,
        "kv_transfer_params": {
            "do_remote_decode": True,
            "remote_host": prefill_host,
            "remote_port": prefill_port,
        }
    }
)
```

**Benefits:**
- Optimize GPU utilization (prefill is compute-bound, decode is memory-bound)
- Better batching opportunities
- Independent scaling of prefill/decode workers

## API Reference

### OpenAI-Compatible Endpoints

```
# Text Generation
POST /v1/completions
POST /v1/chat/completions

# Embeddings
POST /v1/embeddings
POST /v1/rerank

# Audio
POST /v1/audio/transcriptions
POST /v1/audio/translations

# Generation
POST /v1/images/generations
POST /v1/video/generations
```

### vLLM-Specific Endpoints

```
# Tokenization
POST /tokenize
POST /detokenize

# LoRA Management
POST /v1/load_lora_adapter
POST /v1/unload_lora_adapter

# Metrics & Health
GET /load
GET /metrics
GET /health
GET /ready
```

### Protocol Definitions

**File:** `python/aibrix/aibrix/openapi/protocol.py`

```python
class LoadLoraAdapterRequest(BaseModel):
    lora_name: str
    lora_path: str

class LoadLoraAdapterRuntimeRequest(BaseModel):
    lora_name: str
    artifact_url: str  # s3://, gs://, huggingface://
    credentials_secret: Optional[str]
    local_dir: Optional[str]
```

## Routing Algorithms

**Directory:** `pkg/plugins/gateway/algorithms/`

| Algorithm | File | Use Case |
|-----------|------|----------|
| LeastLoad | `least_load.go` | General load balancing |
| LeastLatency | `least_latency.go` | Latency-sensitive |
| LeastKVCache | `least_kv_cache.go` | Memory optimization |
| PrefixCache | `prefix_cache.go` | Prompt reuse |
| PDDisaggregation | `pd_disaggregation.go` | Prefill/decode separation |
| SessionAffinity | `simple_session_affinity.go` | Stateful sessions |

## Current Limitations

### 1. Monolithic Pipeline

Current vLLM-omni runs the entire multimodal pipeline in a single process:
- Encoder + LLM + (optional) Generator all in one pod
- Cannot scale components independently
- Resource allocation is coarse-grained

### 2. No Pipeline-Aware Scheduling

- Gateway doesn't understand request flow through pipeline stages
- Batching decisions don't consider pipeline structure
- No cross-stage optimization

### 3. Communication Overhead

- All data flows through gateway
- No direct GPU-to-GPU communication between pipeline stages
- Serialization/deserialization overhead

## Opportunities from Cornserve

### What vLLM-Omni Could Gain from Cornserve:

1. **Model Fission**
   - Split encoder, LLM, generator into separate containers
   - Independent scaling per component
   - Cost optimization

2. **Sidecar P2P Communication**
   - Direct tensor transfer between pipeline stages
   - Avoid gateway bottleneck for intermediate data
   - Reduced latency

3. **Profile-Driven Optimization**
   - Empirical profiling of each pipeline stage
   - Informed batching decisions
   - SLO-aware scheduling

4. **DAG-Aware Execution**
   - Understand request flow through pipeline
   - Optimize cross-stage batching
   - Better resource utilization

## Integration Feasibility

**Can vLLM-Omni be implemented with Cornserve patterns?**

**Yes, with modifications:**

1. **Container-based Fission**
   - Instead of process-based model fission, use pods
   - Each pipeline stage as a separate deployment
   - Kubernetes for orchestration

2. **Network-based P2P**
   - Replace shared-memory sidecar with network-based transfer
   - Consider RDMA for high-performance
   - Service mesh for routing

3. **CRD-based Task Definition**
   - Define pipeline as Kubernetes CRD
   - Controller manages stage deployments
   - Operator handles scaling and routing

4. **Integration Points in AIBrix**
   - New `PipelineTask` CRD for DAG definition
   - Extended `PodAutoscaler` for stage-level scaling
   - New routing algorithm for pipeline awareness
