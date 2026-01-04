# Evaluation: vLLM-Omni and Cornserve Integration into AIBrix

## Executive Summary

This document evaluates the integration of **vLLM-Omni** (the official vLLM omni-modality framework) and concepts from the **Cornserve** paper (arXiv:2512.14098) into AIBrix. Both systems address the emerging need for **any-to-any multimodal model serving** - models that accept and produce mixed modalities (text, image, audio, video).

**Key Finding:** AIBrix's existing architecture aligns well with vLLM-Omni/Cornserve patterns, particularly its:
- Prefill/Decode disaggregation via StormService
- Gateway-based intelligent routing
- Multi-modal model support samples

**Recommendation:** A phased integration approach leveraging AIBrix's existing primitives while extending them for omni-modal workloads.

---

## 1. Background: What Are We Integrating?

### 1.1 vLLM-Omni

**Source:** [github.com/vllm-project/vllm-omni](https://github.com/vllm-project/vllm-omni) | [Official Blog](https://blog.vllm.ai/2025/11/30/vllm-omni.html)

vLLM-Omni is the official vLLM project extension for omni-modality models. Key characteristics:

| Feature | Description |
|---------|-------------|
| **Input Modalities** | Text, images, audio, video |
| **Output Modalities** | Text, images, audio, video |
| **Architecture** | Fully disaggregated pipeline with dynamic resource allocation |
| **Supported Models** | Qwen-Omni, Qwen-Image, and other SOTA omni-models |
| **API** | OpenAI-compatible, Hugging Face integration |
| **Version** | v0.11.0rc (built on vLLM v0.11.0) |

**Three-Stage Pipeline:**
1. **Modality Encoders** - Vision Transformers, Whisper-style audio encoders
2. **LLM Core** - Autoregressive text generation (leverages vLLM)
3. **Modality Generators** - Diffusion Transformers, audio decoders

### 1.2 Cornserve (Paper: arXiv:2512.14098)

**Authors:** Jeff J. Ma, Jae-Won Chung, et al. (University of Michigan, Samsung)

**Repository:** [github.com/cornserve-ai/cornserve](https://github.com/cornserve-ai/cornserve)

Cornserve is a distributed serving system for any-to-any multimodal models with:
- **3.81× throughput improvement** over baselines
- **5.79× tail latency reduction**
- **Automatic model fission** determining optimal disaggregation strategies
- **~15,000 lines of Python** (~8,700 control plane, ~6,500 executor code)

**Key Innovations:**
1. **Model Fission Planning** - Automatic disaggregation strategy selection
2. **Executor Graph Abstraction** - DAG-based computation representation
3. **Cell-Based Resource Allocation** - Power-of-two GPU allocation units
4. **Request-Static Routing** - Probabilistic path assignment (3.7× higher throughput vs dynamic)

---

## 1.3 Cornserve Architecture Deep Dive (from codebase analysis)

After analyzing the [Cornserve source code](https://github.com/cornserve-ai/cornserve), here are the key architectural components:

### 1.3.1 System Components

```
┌─────────────────────────────────────────────────────────────────────────┐
│                      CORNSERVE CONTROL PLANE                             │
├─────────────────────────────────────────────────────────────────────────┤
│  Gateway (FastAPI)          │  Task Dispatcher (gRPC+HTTP)              │
│  ├─ /app/register           │  ├─ notify_task_deployment()              │
│  ├─ /app/invoke/{app_id}    │  ├─ notify_task_teardown()                │
│  ├─ /tasks/invoke           │  └─ invoke() - dispatches to executors    │
│  └─ /task/scale             │                                           │
├─────────────────────────────────────────────────────────────────────────┤
│  Resource Manager (gRPC)    │  Task Manager (gRPC per executor)         │
│  ├─ deploy_unit_task()      │  ├─ RegisterTask()                        │
│  ├─ teardown_unit_task()    │  ├─ UpdateResources() - scale GPUs        │
│  ├─ scale_up/down_unit_task │  ├─ GetRoute() - executor selection       │
│  └─ Spawns K8s Pods/Services│  └─ Healthcheck()                         │
├─────────────────────────────────────────────────────────────────────────┤
│  Task Executors (GPU Pods)                                              │
│  ├─ VLLMDescriptor (LLM)         - vLLM 0.9.2 integration               │
│  ├─ PrefillVLLMDescriptor        - Prefill stage with NixlConnector     │
│  ├─ DecodeVLLMDescriptor         - Decode stage with NixlConnector      │
│  ├─ EricDescriptor (Encoder)     - Multimodal encoders                  │
│  └─ GeriDescriptor (Generator)   - Image/audio generators               │
├─────────────────────────────────────────────────────────────────────────┤
│  Sidecar (per-GPU)              │  Task Registry (CRD-based)            │
│  ├─ DataForward routing         │  ├─ TaskDefinition CR                 │
│  ├─ Shared memory (/dev/shm)    │  ├─ UnitTaskInstance CR               │
│  └─ RDMA via UCX                │  └─ ExecutionDescriptor CR            │
└─────────────────────────────────────────────────────────────────────────┘
```

### 1.3.2 gRPC Service Definitions

**From `proto/v1/` directory:**

```protobuf
// resource_manager.proto
service ResourceManager {
  rpc DeployUnitTask(DeployUnitTaskRequest) returns (DeployUnitTaskResponse);
  rpc TeardownUnitTask(TeardownUnitTaskRequest) returns (TeardownUnitTaskResponse);
  rpc ScaleUnitTask(ScaleUnitTaskRequest) returns (ScaleUnitTaskResponse);
  rpc Healthcheck(HealthcheckRequest) returns (HealthcheckResponse);
}

// task_manager.proto
service TaskManager {
  rpc RegisterTask(RegisterTaskRequest) returns (RegisterTaskResponse);
  rpc UpdateResources(UpdateResourcesRequest) returns (UpdateResourcesResponse);
  rpc GetRoute(GetRouteRequest) returns (GetRouteResponse);  // Returns executor URL
  rpc Healthcheck(HealthcheckRequest) returns (HealthcheckResponse);
}

// task_dispatcher.proto
service TaskDispatcher {
  rpc NotifyUnitTaskDeployment(NotifyUnitTaskDeploymentRequest) returns (NotifyUnitTaskDeploymentResponse);
  rpc NotifyUnitTaskTeardown(NotifyUnitTaskTeardownRequest) returns (NotifyUnitTaskTeardownResponse);
}
```

### 1.3.3 vLLM Integration Details

Cornserve integrates vLLM 0.9.2 through **TaskExecutionDescriptors**:

```python
# VLLMDescriptor - Standard LLM execution
class VLLMDescriptor(TaskExecutionDescriptor):
    def get_container_args(self, gpus, port):
        return [
            self.task.model_id,
            "--tensor-parallel-size", str(len(gpus)),
            "--port", str(port),
            "--trust-remote-code",
            "--cornserve-sidecar-ranks", *[str(gpu.global_rank) for gpu in gpus],
            "--enforce-eager",  # Required for hidden states transfer
            "--gpu-memory-utilization", "0.93",
        ]

# PrefillVLLMDescriptor - Prefill stage for P/D disaggregation
class PrefillVLLMDescriptor(TaskExecutionDescriptor):
    def get_container_args(self, gpus, port):
        return [
            # ... same as above, plus:
            "--kv-transfer-config",
            '{"kv_connector":"NixlConnector","kv_role":"kv_producer"}',
        ]

# DecodeVLLMDescriptor - Decode stage
class DecodeVLLMDescriptor(TaskExecutionDescriptor):
    def get_container_args(self, gpus, port):
        return [
            # ... same as above, plus:
            "--kv-transfer-config",
            '{"kv_connector":"NixlConnector","kv_role":"kv_consumer"}',
        ]
```

**Key vLLM customizations in Cornserve:**
- `--cornserve-sidecar-ranks`: Routes embedding data through sidecar
- `CORNSERVE_VLLM_DISABLE_MULTIMODAL`: When encoders are disaggregated
- Custom `vllm_xargs` for hidden states and KV transfer param forwarding

### 1.3.4 Kubernetes Custom Resources

Cornserve uses CRDs for task lifecycle management:

```python
# From constants.py
CRD_GROUP = "cornserve.ai"
CRD_VERSION = "v1"

# Custom Resources
CRD_KIND_TASK_DEFINITION = "TaskDefinition"       # Defines task types
CRD_KIND_UNIT_TASK_INSTANCE = "UnitTaskInstance"  # Running task instances
CRD_KIND_EXECUTION_DESCRIPTOR = "ExecutionDescriptor"  # How to run tasks
```

### 1.3.5 Data Transfer Architecture

**Intra-node (≤8 GPUs):**
```python
# Uses Linux shared memory /dev/shm
# Direct GPU → shared memory writes
# gRPC notifications between executors
```

**Inter-node (>8 GPUs):**
```python
# RDMA via UCX communication library
# NixlConnector for KV cache transfer
# Infiniband device mounting in containers
```

### 1.3.6 Composite Task Example (Qwen3-Omni)

```python
# examples/qwen3_omni.py
class OmniTask(Task[OmniInput, Stream[OpenAIChatCompletionChunk]]):
    """Combines thinker (LLM) and talker (audio generator)"""

    def post_init(self):
        self.thinker_text = MLLMTask(model_id=self.model_id, ...)
        self.thinker_embedding = MLLMEmbeddingTask(model_id=self.model_id, ...)
        self.talker_vocoder = OmniTalkerVocoderTask(model_id=self.model_id)

    def invoke(self, task_input: OmniInput):
        if not task_input.return_audio:
            return self.thinker_text.invoke(thinker_input)

        # Get embeddings from thinker
        thinker_output = self.thinker_embedding.invoke(thinker_input)

        # Pass to talker for audio generation
        return self.talker_vocoder.invoke(OmniTalkerVocoderInput(
            thinker_hidden_states=thinker_output.embeddings,
            ...
        ))
```

---

## 2. AIBrix Current State Analysis

### 2.1 Relevant Existing Capabilities

| AIBrix Feature | Relevance to Omni-Modal Serving |
|----------------|-------------------------------|
| **StormService/RoleSet** | Maps directly to disaggregated component roles (encoder, LLM, generator) |
| **Prefill/Decode Disaggregation** | Proves AIBrix can handle multi-stage inference |
| **NixlConnector (RDMA)** | Inter-component data transfer infrastructure |
| **Gateway Routing Algorithms** | Foundation for omni-modal request routing |
| **Multi-Modal Samples** | Qwen2-Audio, Qwen2.5-VL already deployed |

### 2.2 Current Multi-Modal Support

AIBrix already supports multi-modal models via standard vLLM:

```yaml
# samples/multimodality/vllm/qwen-audio.yaml
containers:
  - name: vllm-openai
    image: aibrix/vllm-openai:v0.9.2
    args:
      - --model /models/Qwen2-Audio-7B-Instruct/
    env:
      - name: VLLM_AUDIO_FETCH_TIMEOUT
        value: "1200"
```

**Limitation:** Current support is limited to:
- Multi-modal **input** (images, audio, video → text)
- Text-only **output**

### 2.3 Architecture Alignment

```
┌─────────────────────────────────────────────────────────────────┐
│                    CORNSERVE ARCHITECTURE                        │
├─────────────────────────────────────────────────────────────────┤
│  Planner ─────────────────────┐                                 │
│  Gateway ──────────────────── │─── Task Dispatcher              │
│  Task Manager ── Task Executor (GPU)                            │
└─────────────────────────────────────────────────────────────────┘
                              ↕ (maps to)
┌─────────────────────────────────────────────────────────────────┐
│                    AIBRIX ARCHITECTURE                           │
├─────────────────────────────────────────────────────────────────┤
│  Controllers ─────────────────┐                                 │
│  Gateway Plugin ───────────── │─── Routing Algorithms           │
│  StormService ── RoleSet ── Pods (GPU)                          │
└─────────────────────────────────────────────────────────────────┘
```

---

## 3. Integration Approaches

### 3.1 Approach A: Direct vLLM-Omni Deployment (Low Effort)

**Description:** Deploy vLLM-Omni as a new engine type alongside existing vLLM.

**Changes Required:**
1. Add new container image: `aibrix/vllm-omni:v0.11.0`
2. Add engine label: `model.aibrix.ai/engine: "vllm-omni"`
3. Create sample deployments for Qwen-Omni models
4. Update gateway to recognize omni-modal response types

**Pros:**
- Minimal code changes
- Leverages vLLM-Omni's built-in pipeline management
- Quick time to market

**Cons:**
- Doesn't leverage AIBrix's disaggregation capabilities
- Limited scalability for large models
- No automatic planning

**Implementation Estimate:** 1-2 weeks

### 3.2 Approach B: Hybrid Integration (Medium Effort)

**Description:** Use AIBrix StormService for component disaggregation with vLLM-Omni stages.

**Architecture:**
```
┌────────────────────────────────────────────────────┐
│                 StormService                        │
├────────────────────────────────────────────────────┤
│  Role: encoder (vLLM-Omni encoder stage)           │
│  Role: llm (vLLM-Omni LLM core)                    │
│  Role: generator (vLLM-Omni generator stage)       │
└────────────────────────────────────────────────────┘
```

**Changes Required:**
1. Extend StormService to support 3+ roles
2. Add omni-modal routing strategy in gateway
3. Implement data transfer between stages (extend NixlConnector)
4. Create CRD extensions for stage-specific configurations

**Pros:**
- Leverages existing AIBrix infrastructure
- Per-stage independent scaling
- Familiar operational model

**Cons:**
- Requires vLLM-Omni API adaptation
- Manual disaggregation decisions

**Implementation Estimate:** 4-6 weeks

### 3.3 Approach C: Cornserve-Inspired Full Integration (High Effort)

**Description:** Implement Cornserve's automatic planning and fission capabilities within AIBrix.

**New Components:**

```go
// api/orchestration/v1alpha1/omniserving_types.go

// OmniModel represents an any-to-any multimodal model
type OmniModelSpec struct {
    // Computation graph definition
    ComputeGraph ComputeGraphSpec `json:"computeGraph"`

    // Target throughput for planning
    TargetThroughput resource.Quantity `json:"targetThroughput"`

    // Allowed disaggregation strategies
    FissionStrategies []FissionStrategy `json:"fissionStrategies,omitempty"`
}

type ComputeGraphSpec struct {
    // Stages in the pipeline
    Stages []OmniStage `json:"stages"`

    // Edges between stages
    Edges []StageEdge `json:"edges"`
}

type OmniStage struct {
    Name      string `json:"name"`
    Type      string `json:"type"` // encoder, llm, generator
    Model     string `json:"model"`
    Resources ResourceRequirements `json:"resources"`
}
```

**Planner Component:**
```go
// pkg/controller/omniplanner/planner.go

type OmniPlanner struct {
    // Cell-based allocation (Cornserve concept)
    cellSizes []int // [1, 2, 4, 8, 16, ...]

    // Cached optimal strategies per cell size
    strategies map[int]FissionStrategy
}

func (p *OmniPlanner) ComputeOptimalFission(
    graph ComputeGraph,
    targetThroughput float64,
    availableGPUs int,
) (*FissionPlan, error) {
    // Implement multicommodity network design
}
```

**Pros:**
- Maximum performance optimization
- Automatic resource allocation
- Full feature parity with Cornserve

**Cons:**
- Significant engineering effort
- Requires deep understanding of model characteristics
- Higher operational complexity

**Implementation Estimate:** 3-6 months

---

## 4. Recommended Integration Strategy

### Phase 1: Foundation (Weeks 1-2)
**Goal:** Enable basic vLLM-Omni model deployment

1. Add vLLM-Omni container image support
2. Create Qwen-Omni sample deployment
3. Update gateway to handle multi-modal responses
4. Add documentation

**Deliverables:**
- `samples/omnimodality/vllm-omni/qwen-omni.yaml`
- Gateway updates for audio/image response handling
- User guide for omni-modal deployment

### Phase 2: Disaggregated Serving (Weeks 3-6)
**Goal:** Enable component-level disaggregation via StormService

1. Extend StormService for 3+ role support
2. Implement encoder → LLM → generator pipeline
3. Add `pd-omni` routing strategy in gateway
4. Create disaggregated Qwen-Omni sample

**Sample Configuration:**
```yaml
apiVersion: orchestration.aibrix.ai/v1alpha1
kind: StormService
metadata:
  name: qwen-omni
spec:
  replicaMode:
    replicas: 1
    template:
      roles:
        - name: encoder
          replicas: 1
          template:
            containers:
              - name: vllm-omni
                args: ["--stage", "encoder"]
        - name: llm
          replicas: 2
          template:
            containers:
              - name: vllm-omni
                args: ["--stage", "llm"]
        - name: generator
          replicas: 1
          template:
            containers:
              - name: vllm-omni
                args: ["--stage", "generator"]
```

### Phase 3: Intelligent Planning (Weeks 7-12)
**Goal:** Automatic disaggregation strategy selection

1. Implement OmniModel CRD with compute graph spec
2. Build planner component (simplified Cornserve approach)
3. Add autoscaling for omni-modal workloads
4. Performance profiling and optimization

### Phase 4: Production Hardening (Weeks 13-16)
**Goal:** Enterprise-ready omni-modal serving

1. Multi-tenancy support
2. Cost optimization (heterogeneous GPU placement)
3. Observability and debugging tools
4. Load testing and benchmarking

---

## 5. Technical Deep Dive: Key Integration Points

### 5.1 Gateway Routing for Omni-Modal

The gateway needs to understand request modality types:

```go
// pkg/plugins/gateway/algorithms/omni.go

type OmniRouter struct {
    // Route based on input/output modality combination
    modalityRoutes map[ModalityPair]*RouteConfig
}

type ModalityPair struct {
    Input  []Modality // [text, image, audio, video]
    Output []Modality
}

func (r *OmniRouter) Route(req *InferenceRequest) (*Backend, error) {
    modalities := extractModalities(req)

    // Select appropriate pipeline based on modality
    route := r.modalityRoutes[modalities]

    // Apply load balancing within the selected pipeline
    return route.selectBackend()
}
```

### 5.2 Data Transfer Between Stages

Extend NixlConnector for multimodal data:

```go
// pkg/connector/omni_connector.go

type OmniDataTransfer struct {
    // KV cache transfer (for LLM stage)
    kvCache *NixlConnector

    // Embedding transfer (encoder → LLM)
    embeddings *SharedMemoryTransfer

    // Generated content transfer (LLM → generator)
    generatedTokens *SharedMemoryTransfer
}

func (t *OmniDataTransfer) TransferEncoderOutput(
    ctx context.Context,
    embeddings []float32,
    targetStage string,
) error {
    // Use shared memory for intra-node
    // Use RDMA for inter-node
}
```

### 5.3 Autoscaling for Omni-Modal

Different stages have different scaling characteristics:

```yaml
apiVersion: autoscaling.aibrix.ai/v1alpha1
kind: PodAutoscaler
metadata:
  name: qwen-omni-autoscaler
spec:
  scaleTargetRef:
    kind: StormService
    name: qwen-omni
  roleScaling:
    - role: encoder
      metric: queue_depth
      targetValue: 10
    - role: llm
      metric: kv_cache_utilization
      targetValue: 80
    - role: generator
      metric: diffusion_steps_pending
      targetValue: 5
```

---

## 6. Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| vLLM-Omni API instability | Medium | High | Pin to stable version, maintain compatibility layer |
| Performance regression | Medium | Medium | Extensive benchmarking before release |
| Operational complexity | High | Medium | Comprehensive documentation, sane defaults |
| Resource fragmentation | Medium | Medium | Intelligent placement, cell-based allocation |
| Debugging difficulty | High | Low | Enhanced observability, request tracing |

---

## 7. Resource Requirements

### Development Team
- 2 backend engineers (Go/Kubernetes)
- 1 ML infrastructure engineer (Python/PyTorch)
- 0.5 DevOps engineer (CI/CD, testing)

### Hardware for Testing
- 8× A100 80GB GPUs (minimum)
- High-speed networking (100Gbps+ recommended for RDMA)
- Kubernetes cluster with GPU operator

---

## 8. Success Metrics

| Metric | Target | Measurement Method |
|--------|--------|-------------------|
| Throughput | 3× vs monolithic baseline | Load testing with Qwen-Omni |
| P99 Latency | 50% reduction vs baseline | Percentile tracking |
| Resource Utilization | >70% GPU utilization | Prometheus metrics |
| Deployment Time | <10 min for new model | E2E deployment timing |
| API Compatibility | 100% OpenAI-compatible | Integration test suite |

---

## 9. Why AIBrix Has Strong Advantage to Build This Layer

After deep analysis of Cornserve's codebase, there are striking similarities with AIBrix's existing architecture that make integration highly feasible:

### 9.1 Concept Mapping: Cornserve → AIBrix

| Cornserve Concept | AIBrix Equivalent | Status |
|-------------------|-------------------|--------|
| **ResourceManager** | StormService Controller | ✅ Exists |
| **TaskManager** (per-executor gRPC) | RoleSet Pod management | ✅ Exists |
| **TaskDispatcher** | Gateway Plugin | ✅ Exists |
| **Task Executor Descriptors** | Container configurations in RoleSet | ✅ Exists |
| **NixlConnector (P/D)** | NixlConnector in samples/disaggregation | ✅ Exists |
| **Sidecar (data routing)** | Could extend AI Runtime | 🔶 Partial |
| **TaskDefinition CRD** | Could add new CRD | ⬜ To build |
| **Cell-based Planner** | Could add to PodAutoscaler | ⬜ To build |

### 9.2 Overlapping Infrastructure

**Both Cornserve and AIBrix already share:**

```
┌─────────────────────────────────────────────────────────────────────┐
│                    SHARED INFRASTRUCTURE                             │
├─────────────────────────────────────────────────────────────────────┤
│  ✅ vLLM as LLM executor (same --kv-transfer-config flags)          │
│  ✅ NixlConnector for KV cache transfer (kv_producer/kv_consumer)   │
│  ✅ Kubernetes-native deployment (Pods, Services, CRDs)             │
│  ✅ Role-based disaggregation (Prefill/Decode → Encoder/LLM/Gen)    │
│  ✅ gRPC for control plane communication                            │
│  ✅ Gateway/router for request routing                              │
│  ✅ OpenTelemetry tracing support                                   │
│  ✅ Tensor parallelism per component                                │
│  ✅ GPU resource management per role                                │
└─────────────────────────────────────────────────────────────────────┘
```

### 9.3 Code Comparison: vLLM Args

**Cornserve PrefillVLLMDescriptor:**
```python
args = [
    self.task.model_id,
    "--tensor-parallel-size", str(len(gpus)),
    "--kv-transfer-config", '{"kv_connector":"NixlConnector","kv_role":"kv_producer"}',
    "--cornserve-sidecar-ranks", *[str(gpu.global_rank) for gpu in gpus],
]
```

**AIBrix samples/disaggregation/vllm/1p1d.yaml:**
```yaml
args:
  - --kv-transfer-config
  - '{"kv_connector":"NixlConnector","kv_role":"kv_producer",...}'
  - --tensor-parallel-size
  - "1"
```

**The vLLM configurations are nearly identical!**

### 9.4 What AIBrix Can Adopt from Cornserve

| Feature | Cornserve Implementation | Effort for AIBrix |
|---------|--------------------------|-------------------|
| **Eric (Encoder) executor** | Python service, ~2K LoC | Medium - add as new engine type |
| **Geri (Generator) executor** | Python service, ~2K LoC | Medium - add as new engine type |
| **DataForward abstraction** | Pydantic models + sidecar | Medium - extend AI Runtime |
| **Composite Task graph** | Python task composition | Low - could use YAML/CRD |
| **GPU scaling API** | UpdateResources gRPC | Low - extend PodAutoscaler |
| **App registration** | Gateway endpoints | Low - gateway plugin extension |

### 9.5 Recommended Path: Hybrid Approach

Given the high overlap, AIBrix should:

1. **Adopt Cornserve's executor pattern** - Port Eric/Geri as new engine types alongside vLLM
2. **Extend StormService** - Add encoder/generator roles beyond prefill/decode
3. **Enhance Gateway Plugin** - Add task dispatch logic from Cornserve's TaskDispatcher
4. **Keep Kubernetes-native** - Use CRDs instead of Cornserve's Python-based task registry

### 9.6 Strategic Value

Building omni-modal support on AIBrix's existing foundation provides:

| Advantage | Explanation |
|-----------|-------------|
| **Lower engineering cost** | 60-70% of infrastructure already exists |
| **Familiar patterns** | Same StormService/RoleSet model for users |
| **Enterprise integration** | Fits with existing RBAC, multi-tenancy, observability |
| **vLLM continuity** | Already using same vLLM flags and NixlConnector |
| **Kubernetes-first** | CRD-based vs Python-based task management |

---

## 10. Conclusion

Integrating vLLM-Omni and Cornserve concepts into AIBrix is strategically valuable as any-to-any multimodal models become mainstream. **AIBrix's existing architecture provides a very strong foundation** - after codebase analysis, we found that 60-70% of the infrastructure already exists:

1. **StormService** already supports role-based disaggregation (same as Cornserve's TaskManager)
2. **Gateway** has extensible routing framework (same as Cornserve's TaskDispatcher)
3. **NixlConnector** provides high-performance data transfer (same vLLM integration)
4. **Kubernetes-native CRDs** align with Cornserve's task registry pattern

The recommended phased approach balances:
- **Quick wins** (Phase 1-2): Enable basic omni-modal serving in 6 weeks
- **Strategic value** (Phase 3-4): Full Cornserve-level optimization in 16 weeks

**Next Steps:**
1. Port Cornserve's Eric (encoder) and Geri (generator) as new AIBrix engine types
2. Extend StormService to support encoder/llm/generator roles
3. Add DataForward-style embedding routing to AI Runtime
4. Benchmark disaggregated vs monolithic performance on Qwen-Omni
5. Evaluate contributing back to upstream vLLM-Omni

---

## References

1. [Cornserve Paper (arXiv:2512.14098)](https://arxiv.org/abs/2512.14098)
2. [vLLM-Omni GitHub](https://github.com/vllm-project/vllm-omni)
3. [vLLM-Omni Blog Post](https://blog.vllm.ai/2025/11/30/vllm-omni.html)
4. [Cornserve Website](https://cornserve.ai/)
5. [AIBrix Architecture Docs](/docs/source/designs/architecture.rst)
