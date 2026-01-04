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

Cornserve is a distributed serving system for any-to-any multimodal models with:
- **3.81× throughput improvement** over baselines
- **5.79× tail latency reduction**
- **Automatic model fission** determining optimal disaggregation strategies

**Key Innovations:**
1. **Model Fission Planning** - Automatic disaggregation strategy selection
2. **Executor Graph Abstraction** - DAG-based computation representation
3. **Cell-Based Resource Allocation** - Power-of-two GPU allocation units
4. **Request-Static Routing** - Probabilistic path assignment

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

## 9. Conclusion

Integrating vLLM-Omni and Cornserve concepts into AIBrix is strategically valuable as any-to-any multimodal models become mainstream. AIBrix's existing architecture provides a strong foundation:

1. **StormService** already supports role-based disaggregation
2. **Gateway** has extensible routing framework
3. **NixlConnector** provides high-performance data transfer

The recommended phased approach balances:
- **Quick wins** (Phase 1-2): Enable basic omni-modal serving in 6 weeks
- **Strategic value** (Phase 3-4): Full Cornserve-level optimization in 16 weeks

**Next Steps:**
1. Validate vLLM-Omni v0.11.0 compatibility with AIBrix container build
2. Create proof-of-concept Qwen-Omni deployment
3. Benchmark disaggregated vs monolithic performance
4. Finalize Phase 1 implementation plan

---

## References

1. [Cornserve Paper (arXiv:2512.14098)](https://arxiv.org/abs/2512.14098)
2. [vLLM-Omni GitHub](https://github.com/vllm-project/vllm-omni)
3. [vLLM-Omni Blog Post](https://blog.vllm.ai/2025/11/30/vllm-omni.html)
4. [Cornserve Website](https://cornserve.ai/)
5. [AIBrix Architecture Docs](/docs/source/designs/architecture.rst)
