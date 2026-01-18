# Cornserve Paper Analysis: Efficiently Serving Any-to-Any Multimodal Models

**Paper:** arXiv:2512.14098
**Reference Implementation:** https://github.com/cornserve-ai/cornserve

## Executive Summary

Cornserve addresses the challenge of efficiently serving modern **any-to-any multimodal AI systems** that combine multiple specialized models (vision encoders, language models, image/audio generators). The paper identifies that existing inference systems optimize for single-model or homogeneous workloads, but compound multimodal systems require orchestrating multiple distinct models with different computational characteristics and data dependencies.

## Core Problem Statement

Modern multimodal AI systems are **compound systems** that chain multiple models:
- Vision encoder → LLM (vision-language understanding)
- Text encoder → Diffusion model → VAE decoder (image generation)
- Audio encoder → LLM → Vocoder (speech understanding & generation)

**Key Challenges:**
1. **Heterogeneous compute requirements**: Different models have vastly different compute profiles
2. **Data dependencies**: DAG-structured execution where later stages depend on earlier outputs
3. **Resource contention**: Multiple models compete for GPU memory and compute
4. **Batching complexity**: Optimal batch sizes differ across model stages
5. **Load imbalance**: Request patterns create uneven load across pipeline stages

## Core Contributions

### 1. Profiler Component

**Purpose:** Empirically characterize performance of each model stage.

**Key Insights:**
- Theoretical performance models are inaccurate for complex multimodal models
- Empirical profiling across batch sizes (1 to max supported) is essential
- Stores latency, throughput, and memory consumption in lookup tables
- Supports dynamic profiling as new models are added

**What's Profiled:**
```
Per Model Stage:
├── Latency vs Batch Size curve
├── Throughput vs Batch Size curve
├── GPU Memory consumption
├── Pareto frontier of throughput-latency tradeoffs
└── Max sustainable load for different GPU configurations
```

### 2. Planner Module

**Purpose:** Generate efficient execution schedules considering multi-model dependencies.

**Key Design Decisions:**
- Treats compound models as **Directed Acyclic Graphs (DAGs)**
  - Nodes = model stages (encoder, LLM, generator)
  - Edges = data dependencies between models
- Analyzes request sequences and model dependencies
- Generates candidate schedules that respect data flow constraints
- Evaluates schedules using profiled latency estimates
- Selects schedules minimizing end-to-end latency while respecting resource constraints

**Schedule Generation:**
```
Input: Set of requests, Model DAG, Profiled performance data
Output: Execution schedule with:
  - Batch assignments (which requests go together)
  - Execution ordering (respecting DAG constraints)
  - Resource assignments (which GPUs execute which stages)
```

### 3. Solver/Optimizer

**Purpose:** Real-time optimization and resource allocation.

**Optimization Formulation:**
- **Objective:** Minimize end-to-end latency across all requests in a batch
- **Constraints:** GPU memory, compute capacity, DAG dependencies

**Techniques:**
- Resembles **multicommodity network flow optimization**
- Each request's path through pipeline treated as a "commodity"
- Resource constraints model as network edge capacities
- Knapsack-like allocation decisions

**Mathematical Framework (Referenced Equations 3-5):**
```
Constraints:
├── GPU memory capacity per stage
├── Batch size limitations per model
├── Scheduling feasibility given DAG structure
├── Request deadline satisfaction (when applicable)
└── Communication overhead between stages
```

## Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│                     Frontend                            │
│              (Request parsing, multimodal input)        │
└────────────────────────┬────────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────────┐
│                     Profiler                            │
│    (Empirical performance measurement of components)    │
└────────────────────────┬────────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────────┐
│                     Planner                             │
│     (Schedule generation based on request + profile)    │
└────────────────────────┬────────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────────┐
│                     Solver                              │
│    (Real-time optimization and resource allocation)     │
└────────────────────────┬────────────────────────────────┘
                         │
┌────────────────────────▼────────────────────────────────┐
│                    Executor                             │
│       (Actual model inference coordinated across GPUs)  │
└─────────────────────────────────────────────────────────┘
```

## Key Innovation: Model Fission

**Core Philosophy:** "vLLM : Cornserve = Monolith : Microservice"

Instead of treating compound models as monolithic systems:
1. **Fission:** Split complex models into independently scalable components
2. **Sharing:** Automatically share components across applications
3. **Independent Scaling:** Scale each component based on its load

**Example - MLLMTask (Multimodal LLM):**
```
Traditional Monolithic:
┌─────────────────────────────────────┐
│     Single Pod: Encoder + LLM      │
└─────────────────────────────────────┘

Cornserve Model Fission:
┌──────────────────┐   ┌──────────────────┐
│  Encoder Pod(s)  │──▶│    LLM Pod(s)    │
│   (scale: 2x)    │   │   (scale: 8x)    │
└──────────────────┘   └──────────────────┘
          ▲                      ▲
          │                      │
     scale independently based on load
```

## Communication Pattern: Sidecar P2P

**Problem:** Moving tensor data between model stages is expensive.

**Solution:** Sidecar servers paired with GPUs for direct P2P communication.

**Key Features:**
- Avoids NVLink contention for tensor parallelism
- Intra-node: Uses `/dev/shm` (shared memory)
- Inter-node: Uses UCX with RDMA (if available)
- Producer → Sidecar → Consumer pattern

```protobuf
// Sidecar communication protocol
message SendRequest {
  string id = 1;
  repeated int32 dst_ranks = 2;
  bytes data = 3;
  int32 chunk_id = 4;
}

message ReceiveRequest {
  string id = 1;
  int32 chunk_id = 2;
  float timeout = 3;
}
```

## Experimental Results Summary

**Tested Configurations:**
- Vision-to-language: Image encoder → LLM
- Omni-models: Text + Image + Audio input/output (Qwen-2.5-Omni, Qwen3-Omni)
- Generation systems: Janus (vision understanding + image generation)

**Key Findings:**
1. Significant latency improvements over baseline approaches
2. Efficient resource utilization across multi-GPU setups
3. Scalability to realistic request distributions
4. Benefits increase with pipeline complexity

## Implications for AIBrix

### What AIBrix Can Learn from Cornserve:

1. **Profiler-Driven Optimization**
   - AIBrix's `ModelGPUProfile` is a good start but needs extension
   - Should profile at pipeline stage level, not just model level
   - Need empirical profiling infrastructure

2. **DAG-Aware Scheduling**
   - Current routing algorithms don't consider pipeline structure
   - Need to understand request flow through compound models

3. **Independent Component Scaling**
   - Key insight: Encoders and LLMs have different scaling needs
   - Should scale pipeline stages independently

4. **P2P Communication for Tensor Transfer**
   - Avoid routing all data through central gateway
   - Direct GPU-to-GPU communication between stages

### Key Differences from AIBrix's Current Approach:

| Aspect | Cornserve | AIBrix Current |
|--------|-----------|----------------|
| Execution Unit | Process (within container) | Container (pod) |
| Scaling Granularity | Pipeline stage | Entire model |
| Communication | Sidecar P2P | Through gateway |
| Resource Management | Dynamic per-request | Static per-deployment |
| Batching | Cross-request optimization | Per-model batching |

## Recommended Reading

1. **Section 3:** Profiler design and implementation
2. **Section 4:** Planner algorithms and schedule generation
3. **Section 5:** Solver formulation and optimization
4. **Section 6:** Experimental methodology and results
5. **Appendix:** Detailed mathematical formulations

## References

- Primary Paper: arXiv:2512.14098
- Code Repository: https://github.com/cornserve-ai/cornserve
- Related: vLLM fork at https://github.com/cornserve-ai/vllm
