# Cloud-Native Pipeline Orchestration for AIBrix

## Design Document: Planner/Profiler/Solver for Compound AI Systems

**Status:** Proposal
**Authors:** AIBrix Team
**Related Work:** Cornserve (arXiv:2512.14098), vLLM-Omni

---

## Executive Summary

This document proposes extending AIBrix with **cloud-native compound AI pipeline orchestration** capabilities, inspired by Cornserve's planner/profiler/solver architecture. The key insight is that instead of process-based model fission (Cornserve), we can achieve similar benefits with **container-based model fission** using Kubernetes primitives, which aligns with AIBrix's cloud-native philosophy and enables better GPU cost optimization.

### Core Value Proposition

| Approach | Cornserve | Proposed AIBrix |
|----------|-----------|-----------------|
| Execution Unit | OS Process | Kubernetes Pod |
| Orchestration | Custom Python services | Kubernetes Operators |
| Scaling | Custom Resource Manager | PodAutoscaler + new Pipeline Autoscaler |
| Scheduling | Internal Planner | Kubernetes Scheduler + Custom Controllers |
| Communication | Shared memory + ZMQ | Network + optional RDMA sidecar |
| Cost Optimization | Process-level | **Container-level (better for cloud billing)** |

---

## Problem Statement

### Current Limitations

1. **Monolithic Model Serving**
   - Current vLLM-omni runs entire multimodal pipeline in single pod
   - Cannot scale encoder, LLM, and generator independently
   - Coarse-grained resource allocation

2. **No Pipeline Awareness**
   - Gateway treats each request independently
   - No understanding of request flow through pipeline stages
   - Batching decisions don't consider pipeline structure

3. **Suboptimal GPU Utilization**
   - Encoder (compute-bound) and decoder (memory-bound) share same GPU
   - Cannot optimize for heterogeneous workloads
   - Idle GPU cycles during pipeline stage transitions

4. **Difficult Cost Optimization**
   - Can't scale individual stages based on load
   - Over-provisioning due to peak load of bottleneck stage
   - Cloud billing based on pod resources, not actual utilization

### Target Use Cases

1. **Multimodal Understanding:** Image/Video/Audio → Encoder → LLM → Text
2. **Multimodal Generation:** Text → LLM → Diffusion/VAE → Image/Video
3. **Any-to-Any Systems:** Multiple inputs → Multiple stages → Multiple outputs
4. **LoRA-heavy Workloads:** Shared base model + multiple adapters

---

## Proposed Architecture

### Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Control Plane                                    │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    │
│  │  Pipeline   │  │   Stage     │  │   Stage     │  │  Pipeline   │    │
│  │  Controller │  │  Profiler   │  │   Solver    │  │  Planner    │    │
│  │   (CRD)     │  │  Controller │  │  (Routing)  │  │  (Scaling)  │    │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘    │
│         │                │                │                │            │
│         └────────────────┼────────────────┼────────────────┘            │
│                          │                │                              │
│                    ┌─────▼────────────────▼─────┐                       │
│                    │      Pipeline Cache         │                       │
│                    │  (Profiles, Metrics, State) │                       │
│                    └─────────────┬───────────────┘                       │
└──────────────────────────────────┼───────────────────────────────────────┘
                                   │
┌──────────────────────────────────┼───────────────────────────────────────┐
│                         Data Plane                                       │
│                                  │                                       │
│    ┌─────────────────────────────▼─────────────────────────────────┐    │
│    │                    AIBrix Gateway                              │    │
│    │  (Pipeline-aware routing with stage profiler integration)      │    │
│    └───────────────────────────────────────────────────────────────┘    │
│                                  │                                       │
│         ┌────────────────────────┼────────────────────────┐             │
│         │                        │                        │             │
│    ┌────▼────┐              ┌────▼────┐              ┌────▼────┐        │
│    │ Encoder │──Sidecar──▶  │   LLM   │──Sidecar──▶  │Generator│        │
│    │  Pods   │   (P2P)      │  Pods   │   (P2P)      │  Pods   │        │
│    │ (2 GPU) │              │ (8 GPU) │              │ (4 GPU) │        │
│    └─────────┘              └─────────┘              └─────────┘        │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### New Custom Resource Definitions

#### 1. PipelineTask CRD

```yaml
apiVersion: pipeline.aibrix.ai/v1alpha1
kind: PipelineTask
metadata:
  name: vision-language-pipeline
spec:
  # DAG definition of pipeline stages
  stages:
    - name: encoder
      type: UnitTask
      modelId: openai/clip-vit-large-patch14
      resources:
        gpus: 1
        minReplicas: 1
        maxReplicas: 4
      modalities:
        input: [image, video]
        output: [embedding]

    - name: llm
      type: UnitTask
      modelId: meta-llama/Llama-2-7b-chat-hf
      resources:
        gpus: 1
        tensorParallel: 1
        minReplicas: 2
        maxReplicas: 16
      modalities:
        input: [embedding, text]
        output: [text]
      dependencies:
        - encoder

  # Pipeline execution configuration
  execution:
    batchingStrategy: adaptive  # static, adaptive, dynamic
    maxBatchSize: 32
    maxWaitTimeMs: 50

  # SLO targets
  slo:
    p95LatencyMs: 500
    throughputRps: 100

status:
  phase: Running
  stages:
    - name: encoder
      replicas: 2
      avgLatencyMs: 45
      throughput: 150
    - name: llm
      replicas: 8
      avgLatencyMs: 320
      throughput: 100
  bottleneck: llm
```

#### 2. StageProfile CRD

```yaml
apiVersion: pipeline.aibrix.ai/v1alpha1
kind: StageProfile
metadata:
  name: encoder-clip-vit-large
spec:
  stageRef:
    name: encoder
    pipeline: vision-language-pipeline

  # Profiling configuration
  profilingConfig:
    batchSizes: [1, 2, 4, 8, 16, 32]
    inputSizes:
      - {width: 224, height: 224}
      - {width: 336, height: 336}
    warmupIterations: 10
    measureIterations: 100

status:
  lastProfiledAt: "2025-01-15T10:30:00Z"
  profiles:
    - batchSize: 1
      avgLatencyMs: 12.5
      p99LatencyMs: 15.2
      throughput: 80
      gpuMemoryMB: 2048
      gpuUtilization: 0.65
    - batchSize: 4
      avgLatencyMs: 18.3
      p99LatencyMs: 22.1
      throughput: 218
      gpuMemoryMB: 3072
      gpuUtilization: 0.85
    - batchSize: 8
      avgLatencyMs: 28.7
      p99LatencyMs: 34.5
      throughput: 278
      gpuMemoryMB: 4096
      gpuUtilization: 0.92

  # Pareto frontier for optimization
  paretoFrontier:
    - {batchSize: 1, latency: 12.5, throughput: 80}
    - {batchSize: 4, latency: 18.3, throughput: 218}
    - {batchSize: 8, latency: 28.7, throughput: 278}
```

#### 3. PipelineAutoscaler CRD

```yaml
apiVersion: autoscaling.aibrix.ai/v1alpha1
kind: PipelineAutoscaler
metadata:
  name: vision-language-autoscaler
spec:
  pipelineRef:
    name: vision-language-pipeline

  # Per-stage scaling configuration
  stageScaling:
    - stageName: encoder
      algorithm: APA
      metrics:
        - type: StageLatency
          target: 50
        - type: QueueDepth
          target: 10
      minReplicas: 1
      maxReplicas: 4

    - stageName: llm
      algorithm: APA
      metrics:
        - type: KVCacheUtilization
          target: 0.7
        - type: StageLatency
          target: 300
      minReplicas: 2
      maxReplicas: 16

  # Pipeline-level optimization
  optimization:
    objective: MinimizeCost  # MinimizeLatency, MinimizeCost, BalancedSLO
    constraints:
      maxP95LatencyMs: 500
      minThroughputRps: 100

    # Cost model for optimization
    costModel:
      gpuCostPerHour: 2.50
      networkCostPerGB: 0.01

status:
  currentConfig:
    encoder: {replicas: 2, gpus: 1}
    llm: {replicas: 8, gpus: 1}

  metrics:
    totalLatencyP95Ms: 450
    throughputRps: 120
    costPerHour: 25.00

  recommendations:
    - stage: llm
      action: ScaleUp
      reason: "Queue depth exceeding target"
      newReplicas: 10
```

---

## Component Designs

### 1. Stage Profiler Controller

**Purpose:** Automatically profile pipeline stages to gather performance data.

**Location:** `pkg/controller/stageprofiler/`

```go
type StageProfilerReconciler struct {
    client.Client
    Scheme *runtime.Scheme

    // Profiling job management
    profilerJobManager *ProfilerJobManager

    // Profile cache
    profileCache cache.ProfileCache
}

func (r *StageProfilerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var stageProfile pipelinev1alpha1.StageProfile
    if err := r.Get(ctx, req.NamespacedName, &stageProfile); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Check if profiling is needed
    if r.needsProfiling(&stageProfile) {
        // Create profiling job
        job := r.createProfilingJob(&stageProfile)
        if err := r.Create(ctx, job); err != nil {
            return ctrl.Result{}, err
        }

        // Update status
        stageProfile.Status.Phase = "Profiling"
        if err := r.Status().Update(ctx, &stageProfile); err != nil {
            return ctrl.Result{}, err
        }
    }

    // Process completed profiling jobs
    if r.hasCompletedProfilingJob(&stageProfile) {
        profiles := r.collectProfiles(&stageProfile)
        stageProfile.Status.Profiles = profiles
        stageProfile.Status.ParetoFrontier = r.computeParetoFrontier(profiles)
        stageProfile.Status.LastProfiledAt = metav1.Now()

        if err := r.Status().Update(ctx, &stageProfile); err != nil {
            return ctrl.Result{}, err
        }

        // Update cache
        r.profileCache.Set(stageProfile.Name, profiles)
    }

    return ctrl.Result{RequeueAfter: time.Hour}, nil
}
```

**Profiling Job:**

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: profile-encoder-clip
spec:
  template:
    spec:
      containers:
      - name: profiler
        image: aibrix/stage-profiler:latest
        args:
        - --stage-name=encoder
        - --model-id=openai/clip-vit-large-patch14
        - --batch-sizes=1,2,4,8,16,32
        - --warmup=10
        - --iterations=100
        - --output=/profiles/result.json
        resources:
          limits:
            nvidia.com/gpu: 1
        volumeMounts:
        - name: profiles
          mountPath: /profiles
      volumes:
      - name: profiles
        persistentVolumeClaim:
          claimName: profile-storage
      restartPolicy: Never
```

### 2. Pipeline Controller

**Purpose:** Manage lifecycle of compound AI pipelines.

**Location:** `pkg/controller/pipeline/`

```go
type PipelineTaskReconciler struct {
    client.Client
    Scheme *runtime.Scheme

    // Sub-resource managers
    stageManager   *StageDeploymentManager
    sidecarManager *SidecarManager
    routeManager   *PipelineRouteManager
}

func (r *PipelineTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var pipeline pipelinev1alpha1.PipelineTask
    if err := r.Get(ctx, req.NamespacedName, &pipeline); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 1. Create/update stage deployments
    for _, stage := range pipeline.Spec.Stages {
        if err := r.stageManager.EnsureStageDeployment(ctx, &pipeline, &stage); err != nil {
            return ctrl.Result{}, err
        }
    }

    // 2. Create/update sidecar daemonset for P2P communication
    if err := r.sidecarManager.EnsureSidecars(ctx, &pipeline); err != nil {
        return ctrl.Result{}, err
    }

    // 3. Create/update HTTPRoutes for pipeline
    if err := r.routeManager.EnsurePipelineRoutes(ctx, &pipeline); err != nil {
        return ctrl.Result{}, err
    }

    // 4. Update pipeline status
    status := r.computePipelineStatus(ctx, &pipeline)
    pipeline.Status = status
    if err := r.Status().Update(ctx, &pipeline); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
```

**Stage Deployment:**

```go
func (m *StageDeploymentManager) EnsureStageDeployment(
    ctx context.Context,
    pipeline *pipelinev1alpha1.PipelineTask,
    stage *pipelinev1alpha1.PipelineStage,
) error {
    deployment := &appsv1.Deployment{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-%s", pipeline.Name, stage.Name),
            Namespace: pipeline.Namespace,
            Labels: map[string]string{
                "pipeline.aibrix.ai/name":  pipeline.Name,
                "pipeline.aibrix.ai/stage": stage.Name,
                "model.aibrix.ai/name":     stage.ModelId,
            },
        },
        Spec: appsv1.DeploymentSpec{
            Replicas: &stage.Resources.MinReplicas,
            Selector: &metav1.LabelSelector{
                MatchLabels: map[string]string{
                    "pipeline.aibrix.ai/name":  pipeline.Name,
                    "pipeline.aibrix.ai/stage": stage.Name,
                },
            },
            Template: corev1.PodTemplateSpec{
                ObjectMeta: metav1.ObjectMeta{
                    Labels: map[string]string{
                        "pipeline.aibrix.ai/name":  pipeline.Name,
                        "pipeline.aibrix.ai/stage": stage.Name,
                        "model.aibrix.ai/name":     stage.ModelId,
                    },
                },
                Spec: m.buildPodSpec(stage),
            },
        },
    }

    return m.CreateOrUpdate(ctx, deployment)
}
```

### 3. Pipeline Solver (Routing Algorithm)

**Purpose:** Route requests through pipeline stages optimally.

**Location:** `pkg/plugins/gateway/algorithms/pipeline_solver.go`

```go
type PipelineSolver struct {
    // Profile cache for stage performance data
    profileCache cache.ProfileCache

    // Pipeline graph for dependency resolution
    pipelineGraph *PipelineGraph

    // Stage load tracker
    loadTracker *StageLoadTracker
}

func (s *PipelineSolver) Route(ctx *types.RoutingContext, pods PodList) (string, error) {
    // 1. Parse pipeline from request
    pipeline := s.getPipelineFromRequest(ctx)
    if pipeline == nil {
        return "", fmt.Errorf("no pipeline found for request")
    }

    // 2. Get current stage loads
    stageLoads := s.loadTracker.GetStageLoads(pipeline.Name)

    // 3. For each stage in pipeline order (respecting DAG)
    selectedPods := make(map[string]*corev1.Pod)
    for _, stage := range s.pipelineGraph.TopologicalSort(pipeline) {
        // Get stage profile
        profile := s.profileCache.Get(stage.ProfileRef)

        // Get candidate pods for this stage
        stagePods := s.filterPodsForStage(pods, stage.Name)

        // Select optimal pod based on:
        // - Current load
        // - Profile data (latency at current load)
        // - Communication cost with previous stage
        selectedPod := s.selectOptimalPod(stagePods, stageLoads[stage.Name], profile, selectedPods)
        selectedPods[stage.Name] = selectedPod
    }

    // 4. Return first stage pod (gateway entry point)
    firstStage := s.pipelineGraph.GetFirstStage(pipeline)
    return selectedPods[firstStage.Name].Status.PodIP, nil
}

func (s *PipelineSolver) selectOptimalPod(
    pods []*corev1.Pod,
    currentLoad float64,
    profile *StageProfile,
    previousPods map[string]*corev1.Pod,
) *corev1.Pod {
    var bestPod *corev1.Pod
    bestScore := math.MaxFloat64

    for _, pod := range pods {
        // Compute expected latency at current load + 1
        expectedLatency := profile.InterpolateLatency(currentLoad + 1)

        // Compute communication cost (prefer same node as previous stage)
        commCost := s.computeCommCost(pod, previousPods)

        // Combined score
        score := expectedLatency + commCost
        if score < bestScore {
            bestScore = score
            bestPod = pod
        }
    }

    return bestPod
}
```

### 4. Pipeline Planner (Scaling)

**Purpose:** Optimize pipeline-wide scaling decisions.

**Location:** `pkg/controller/pipelineplanner/`

```go
type PipelinePlannerReconciler struct {
    client.Client
    Scheme *runtime.Scheme

    // Profile cache
    profileCache cache.ProfileCache

    // Metrics client
    metricsClient metrics.Client

    // Optimization solver
    solver *OptimizationSolver
}

func (r *PipelinePlannerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var autoscaler autoscalingv1alpha1.PipelineAutoscaler
    if err := r.Get(ctx, req.NamespacedName, &autoscaler); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 1. Collect current metrics for all stages
    pipelineMetrics := r.collectPipelineMetrics(ctx, &autoscaler)

    // 2. Get profiles for all stages
    profiles := r.getStageProfiles(ctx, &autoscaler)

    // 3. Run optimization solver
    optimalConfig := r.solver.Solve(
        pipelineMetrics,
        profiles,
        autoscaler.Spec.Optimization,
        autoscaler.Spec.StageScaling,
    )

    // 4. Apply scaling decisions
    for stageName, config := range optimalConfig.StageConfigs {
        if err := r.scaleStage(ctx, &autoscaler, stageName, config); err != nil {
            return ctrl.Result{}, err
        }
    }

    // 5. Update status
    autoscaler.Status.CurrentConfig = optimalConfig
    autoscaler.Status.Metrics = pipelineMetrics
    if err := r.Status().Update(ctx, &autoscaler); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}
```

**Optimization Solver:**

```go
type OptimizationSolver struct {
    costModel *CostModel
}

type OptimizationResult struct {
    StageConfigs map[string]StageConfig
    ExpectedCost float64
    ExpectedLatency float64
    ExpectedThroughput float64
}

func (s *OptimizationSolver) Solve(
    metrics *PipelineMetrics,
    profiles map[string]*StageProfile,
    optimization OptimizationSpec,
    stageSpecs []StageScalingSpec,
) *OptimizationResult {
    // Build optimization problem
    //
    // Variables:
    //   r_i = replicas for stage i
    //   b_i = batch size for stage i
    //
    // Objective (based on optimization.Objective):
    //   MinimizeCost: min sum(r_i * gpu_i * cost_per_gpu)
    //   MinimizeLatency: min max(latency_i(r_i, b_i))
    //   BalancedSLO: min cost subject to latency <= target
    //
    // Constraints:
    //   - r_i >= minReplicas_i
    //   - r_i <= maxReplicas_i
    //   - End-to-end latency <= maxLatency
    //   - Throughput >= minThroughput
    //   - Flow conservation: throughput(stage_i) >= throughput(stage_{i+1})

    // Use profiles to estimate latency and throughput at each configuration

    // For simplicity, use grid search over feasible configurations
    // (In production, use proper optimization library like CVXPY or Gurobi)

    bestResult := &OptimizationResult{}
    bestObjective := math.MaxFloat64

    for _, config := range s.generateConfigurations(stageSpecs) {
        // Simulate pipeline performance
        latency, throughput := s.simulatePipeline(config, profiles, metrics)
        cost := s.computeCost(config)

        // Check constraints
        if latency > optimization.Constraints.MaxP95LatencyMs {
            continue
        }
        if throughput < optimization.Constraints.MinThroughputRps {
            continue
        }

        // Compute objective
        var objective float64
        switch optimization.Objective {
        case "MinimizeCost":
            objective = cost
        case "MinimizeLatency":
            objective = latency
        case "BalancedSLO":
            objective = cost // Already constrained by latency
        }

        if objective < bestObjective {
            bestObjective = objective
            bestResult = &OptimizationResult{
                StageConfigs:       config,
                ExpectedCost:       cost,
                ExpectedLatency:    latency,
                ExpectedThroughput: throughput,
            }
        }
    }

    return bestResult
}
```

### 5. P2P Sidecar for Inter-Stage Communication

**Purpose:** Enable direct GPU-to-GPU tensor transfer between pipeline stages.

**Design Options:**

#### Option A: Network-Based Sidecar (Recommended for Cloud)

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: pipeline-sidecar
spec:
  selector:
    matchLabels:
      app: pipeline-sidecar
  template:
    spec:
      containers:
      - name: sidecar
        image: aibrix/pipeline-sidecar:latest
        ports:
        - containerPort: 50051  # gRPC
        - containerPort: 50052  # RDMA (if available)
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: ENABLE_RDMA
          value: "false"  # Enable on RDMA-capable clusters
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
```

**Sidecar Service:**

```go
type SidecarServer struct {
    // Local tensor buffer
    buffers map[string]*TensorBuffer

    // Peer connections
    peers map[string]*PeerConnection
}

func (s *SidecarServer) Send(ctx context.Context, req *SendRequest) (*SendResponse, error) {
    // Get or create peer connection
    peer := s.getOrCreatePeer(req.DestinationAddr)

    // Send tensor data
    if err := peer.SendTensor(req.TensorId, req.Data); err != nil {
        return nil, err
    }

    return &SendResponse{Success: true}, nil
}

func (s *SidecarServer) Receive(ctx context.Context, req *ReceiveRequest) (*ReceiveResponse, error) {
    // Wait for tensor with timeout
    buffer, err := s.waitForTensor(req.TensorId, req.TimeoutMs)
    if err != nil {
        return nil, err
    }

    return &ReceiveResponse{Data: buffer.Data}, nil
}
```

#### Option B: Shared Volume (Same-Node Stages)

For stages co-located on the same node, use shared memory volume:

```yaml
volumes:
- name: shared-tensors
  emptyDir:
    medium: Memory
    sizeLimit: 10Gi
```

### 6. Gateway Integration

**Modified Gateway Flow:**

```go
func (s *Server) HandleRequestBody(ctx context.Context, req *extproc.ProcessingRequest_RequestBody) (*extproc.ProcessingResponse, error) {
    routingCtx := s.getRoutingContext(ctx)

    // Check if this is a pipeline request
    if pipeline := s.getPipelineForModel(routingCtx.Model); pipeline != nil {
        // Use pipeline solver for routing
        return s.handlePipelineRequest(ctx, routingCtx, pipeline, req)
    }

    // Regular single-model routing
    return s.handleRegularRequest(ctx, routingCtx, req)
}

func (s *Server) handlePipelineRequest(
    ctx context.Context,
    routingCtx *types.RoutingContext,
    pipeline *PipelineTask,
    req *extproc.ProcessingRequest_RequestBody,
) (*extproc.ProcessingResponse, error) {
    // 1. Get pipeline solver
    solver := s.routerManager.GetRouter(types.RoutingAlgorithm("pipeline_solver"))

    // 2. Get all pods for all stages
    allPods := s.cache.GetPodsForPipeline(pipeline.Name)

    // 3. Route through pipeline solver
    targetPod, err := solver.Route(routingCtx, allPods)
    if err != nil {
        return nil, err
    }

    // 4. Add pipeline metadata to headers
    headers := []*corev3.HeaderValueOption{
        {Header: &corev3.HeaderValue{Key: "x-pipeline-name", Value: pipeline.Name}},
        {Header: &corev3.HeaderValue{Key: "x-pipeline-request-id", Value: routingCtx.RequestID}},
    }

    // 5. Forward to first stage
    return s.forwardToTarget(ctx, targetPod, headers)
}
```

---

## Implementation Phases

### Phase 1: Foundation (4-6 weeks)

1. **CRD Definitions**
   - [ ] PipelineTask CRD
   - [ ] StageProfile CRD
   - [ ] PipelineAutoscaler CRD

2. **Basic Pipeline Controller**
   - [ ] Stage deployment management
   - [ ] Service creation for each stage
   - [ ] HTTPRoute generation

3. **Simple Profiler**
   - [ ] Manual profiling job creation
   - [ ] Profile storage in CRD status

### Phase 2: Optimization (4-6 weeks)

4. **Stage Profiler Controller**
   - [ ] Automatic profiling triggers
   - [ ] Pareto frontier computation
   - [ ] Profile caching

5. **Pipeline Solver**
   - [ ] Basic routing algorithm
   - [ ] Load-aware pod selection
   - [ ] Profile-based latency estimation

6. **Pipeline Planner**
   - [ ] Basic scaling logic
   - [ ] Cost-aware optimization
   - [ ] SLO enforcement

### Phase 3: Communication (3-4 weeks)

7. **P2P Sidecar**
   - [ ] Network-based tensor transfer
   - [ ] Same-node shared memory optimization
   - [ ] Optional RDMA support

8. **Gateway Integration**
   - [ ] Pipeline-aware routing
   - [ ] Request metadata propagation
   - [ ] Metrics collection

### Phase 4: Advanced Features (ongoing)

9. **Advanced Optimization**
   - [ ] Dynamic batch sizing
   - [ ] Heterogeneous GPU support
   - [ ] Multi-pipeline optimization

10. **Observability**
    - [ ] Pipeline-level metrics
    - [ ] Stage-level tracing
    - [ ] Cost dashboards

---

## Migration Path from Current Architecture

### Backward Compatibility

1. **Existing single-model deployments continue to work unchanged**
2. **New pipeline CRDs are opt-in**
3. **Gradual migration path:**
   - Start with PipelineTask for new compound models
   - Migrate existing models as pipeline stages
   - No breaking changes to existing APIs

### Example Migration

**Before (Monolithic):**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: qwen-vl
spec:
  template:
    spec:
      containers:
      - name: vllm
        args:
        - --model=Qwen/Qwen-VL-Chat
        # Encoder + LLM in single container
```

**After (Pipeline):**
```yaml
apiVersion: pipeline.aibrix.ai/v1alpha1
kind: PipelineTask
metadata:
  name: qwen-vl-pipeline
spec:
  stages:
    - name: vision-encoder
      modelId: Qwen/Qwen-VL-Encoder
      resources:
        gpus: 1
        minReplicas: 1
        maxReplicas: 4
    - name: llm
      modelId: Qwen/Qwen-VL-LLM
      resources:
        gpus: 1
        minReplicas: 2
        maxReplicas: 16
      dependencies:
        - vision-encoder
```

---

## Key Differences from Cornserve

| Aspect | Cornserve | AIBrix Pipeline |
|--------|-----------|-----------------|
| **Isolation** | OS process | Container |
| **Orchestration** | Custom Python | Kubernetes operators |
| **Scaling Unit** | Process | Pod |
| **Communication** | Shared memory + ZMQ | Network + optional RDMA |
| **Scheduling** | Internal scheduler | K8s scheduler + custom logic |
| **Profile Storage** | gRPC service | Kubernetes CRD |
| **Cost Optimization** | Process-level | **Pod/container-level** |
| **Cloud Integration** | Limited | **Native K8s + cloud billing** |

### Advantages of Container-Based Approach

1. **Better Cloud Cost Optimization**
   - Cloud providers bill at container/pod level
   - Can use spot instances for less critical stages
   - Resource quotas and limits per stage

2. **Kubernetes Native**
   - Leverage existing K8s ecosystem
   - Service mesh integration
   - Standard observability tools

3. **Isolation and Security**
   - Container-level isolation
   - Network policies per stage
   - RBAC for stage management

4. **Operational Simplicity**
   - Standard K8s deployment patterns
   - Familiar tooling (kubectl, Helm, ArgoCD)
   - No custom runtime dependencies

---

## Open Questions

1. **Tensor Serialization Overhead**
   - Network-based transfer adds serialization cost
   - Mitigation: Use efficient formats (Arrow, custom binary)
   - Research: Zero-copy network transfer with RDMA

2. **Cross-Node Communication Latency**
   - Network latency between stages
   - Mitigation: Prefer same-node scheduling, use RDMA
   - Trade-off: Flexibility vs. latency

3. **Profiling Accuracy**
   - Container overhead may differ from bare metal
   - Mitigation: Profile in actual deployment environment
   - Consider: Network conditions in profiling

4. **Scheduler Integration**
   - How to hint K8s scheduler for stage co-location?
   - Options: Pod affinity, topology spread, custom scheduler

---

## Conclusion

This design proposes a **cloud-native alternative to Cornserve's process-based model fission**, using Kubernetes primitives for orchestration. The key insight is that container-based pipeline stages enable better cost optimization in cloud environments while maintaining the benefits of independent scaling and profiler-driven optimization.

The phased implementation approach allows gradual adoption without disrupting existing deployments, and the CRD-based design integrates naturally with AIBrix's existing controller architecture.
