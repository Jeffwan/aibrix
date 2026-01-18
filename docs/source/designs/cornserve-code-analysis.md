# Cornserve Codebase Analysis

**Repository:** https://github.com/cornserve-ai/cornserve
**Language:** Python (98.4%), Protobuf, Kubernetes YAML

## Repository Structure

```
cornserve/
├── python/cornserve/              # Main Python package
│   ├── app/                       # Application framework
│   │   └── base.py               # AppConfig base class
│   ├── cli/                       # Command-line interface
│   │   └── main.py               # CLI commands
│   ├── sidecar/                   # P2P communication client
│   │   ├── api.py                # Sidecar API
│   │   └── schema.py             # Sidecar configuration
│   ├── task/                      # Task execution framework
│   │   └── base.py               # Task, UnitTask, CompositeTask
│   └── services/                  # Control plane services
│       ├── gateway/              # Entry point service
│       ├── task_manager/         # Task execution management
│       ├── task_dispatcher/      # Task routing
│       └── resource_manager/     # GPU resource allocation
├── proto/v1/                      # gRPC service definitions
│   ├── common.proto              # Shared messages
│   ├── task_manager.proto        # Task Manager API
│   ├── resource_manager.proto    # Resource Manager API
│   ├── task_dispatcher.proto     # Task Dispatcher API
│   └── sidecar.proto             # Sidecar P2P protocol
├── tasklib/                       # Built-in tasks and executors
├── kubernetes/                    # K8s deployment manifests
├── docs/architecture/             # Detailed architecture docs
├── examples/                      # Example applications
└── docker/                        # Container configurations
```

## Core Components Deep Dive

### 1. Task System (`python/cornserve/task/base.py`)

The task system is the foundation of Cornserve's pipeline abstraction.

#### Task Hierarchy

```python
class TaskInput(BaseModel):
    """Base class for task input."""
    pass

class TaskOutput(BaseModel):
    """Base class for task output."""
    pass

class Task:
    """Base task class."""
    descriptor: TaskExecutionDescriptor

    async def __call__(self, input: TaskInput) -> TaskOutput:
        """Execute task."""

class UnitTask(Task):
    """Atomic work unit - single model execution."""
    # Examples: EncoderTask, LLMTask, GeneratorTask
    pass

class CompositeTask(Task):
    """Composition of unit tasks - pipeline."""
    # Examples: MLLMTask (Encoder + LLM)
    pass
```

#### Streaming Support

```python
class Stream(TaskOutput, Generic[OutputT]):
    """Asynchronous stream for streaming responses."""
    async_iterator: AsyncIterator[str | bytes]
    response: aiohttp.ClientResponse

    async def __anext__(self) -> OutputT:
        """Get next streamed item."""

    def transform(self, transform_func, output_type=None) -> Stream:
        """Transform stream items."""
```

#### Task Context (Global State)

```python
# Global context variables for request tracking
task_context: ContextVar[TaskContext]       # Current task execution
task_manager_context: ContextVar[TaskManager]  # Gateway reference
current_task_context: ContextVar[Task]      # Active task
```

### 2. Application Framework (`python/cornserve/app/base.py`)

Applications are the user-facing abstraction for compound AI systems.

```python
class AppConfig(BaseModel):
    """Base class for application configuration."""
    tasks: ClassVar[dict[str, Task]] = Field(
        default_factory=dict,
        description="Dictionary of tasks that the app requires.",
    )

# App must implement:
async def serve(request: RequestT) -> ResponseT:
    """Main function handling request and returning response."""
    # Can return ResponseT or AsyncIterator[ResponseT] for streaming
```

#### Example: Gemma Arena (Multi-Model Application)

```python
# From examples/gemmarena.py
gemma_model_ids = {
    "gemma-2-2b": "google/gemma-2-2b-it",
    "gemma-2-9b": "google/gemma-2-9b-it",
    # ... more models
}

# Create tasks that SHARE the same encoder
gemma_tasks = {
    name: MLLMTask(
        modalities=[Modality.IMAGE],
        model_id=model_id,
        encoder_model_ids=set(gemma_model_ids.values()),  # Sharing!
    )
    for name, model_id in gemma_model_ids.items()
}

class Config(AppConfig):
    tasks = gemma_tasks

async def serve(request):
    # Parallel execution of multiple tasks
    # Concurrent streaming responses
```

### 3. Profiler Implementation

#### Profile Data Structure (`proto/v1/task_manager.proto`)

```protobuf
message ProfilePoint {
  int32 num_gpus = 1;                    // GPU count
  float max_sustainable_load = 2;        // Max throughput
  DeploymentConfig deployment_config = 3; // Parallelism config
}

message DeploymentConfig {
  int32 num_replicas = 1;               // Number of replicas
  int32 tensor_parallel_degree = 2;     // Tensor parallelism level
  int32 pipeline_parallel_degree = 3;   // Pipeline parallelism level
  repeated string gpu_assignments = 4;  // GPU allocations
}
```

#### Profiling APIs

```protobuf
service TaskManager {
  // Get performance profile for a task
  rpc GetTaskProfile(GetTaskProfileRequest)
    returns (GetTaskProfileResponse);
}

message GetTaskProfileRequest {
  string task_id = 1;
}

message GetTaskProfileResponse {
  repeated ProfilePoint profile_points = 1;
}
```

**What Gets Profiled:**
- Max sustainable load per GPU configuration
- Throughput-latency Pareto frontier
- Different parallelism strategy performance
- GPU assignment patterns for optimal inference

### 4. Resource Manager (Solver/Optimizer)

#### Resource Management APIs (`proto/v1/resource_manager.proto`)

```protobuf
service ResourceManager {
  // Deploy a new unit task
  rpc DeployUnitTask(DeployUnitTaskRequest)
    returns (DeployUnitTaskResponse);

  // Scale an existing task
  rpc ScaleUnitTask(ScaleUnitTaskRequest)
    returns (ScaleUnitTaskResponse);

  // Remove an unused task
  rpc TeardownUnitTask(TeardownUnitTaskRequest)
    returns (TeardownUnitTaskResponse);

  // Health check
  rpc Healthcheck(HealthcheckRequest)
    returns (HealthcheckResponse);
}
```

#### GPU Resource Representation

```protobuf
enum ResourceAction {
  ADD = 0;
  REMOVE = 1;
}

message GPUResource {
  ResourceAction action = 1;  // ADD or REMOVE
  string node_id = 2;         // Kubernetes node ID
  int32 global_rank = 3;      // Global GPU index across cluster
  int32 local_rank = 4;       // Local GPU index on node
}
```

#### Optimization Strategy

From `docs/architecture/index.md`:

```
Resource Optimization Triggers:
1. High-load Task Manager → Add resources
2. Low-load Task Manager → Remove resources
3. New app registration → Deploy necessary tasks
4. App unregistration → Teardown unused tasks

Optimization Goals:
- Balance request throughput given fixed GPU resources
- Maximize task sharing across applications
- Minimize end-to-end latency
```

#### Load Reconciliation

```protobuf
rpc ReconcileTargetLoad(ReconcileTargetLoadRequest)
  returns (ReconcileTargetLoadResponse);
```

### 5. Task Dispatcher (Request Routing)

#### Dispatcher APIs (`proto/v1/task_dispatcher.proto`)

```protobuf
service TaskDispatcher {
  // Notify when a task is deployed
  rpc NotifyUnitTaskDeployment(NotifyUnitTaskDeploymentRequest)
    returns (NotifyUnitTaskDeploymentResponse);

  // Notify when a task is torn down
  rpc NotifyUnitTaskTeardown(NotifyUnitTaskTeardownRequest)
    returns (NotifyUnitTaskTeardownResponse);
}
```

#### Request Routing (`proto/v1/task_manager.proto`)

```protobuf
message GetRouteRequest {
  string request_id = 1;
  optional string routing_hint = 2;  // e.g., image hash for cache reuse
}

message GetRouteResponse {
  string task_executor_url = 1;     // Target executor endpoint
  repeated int32 sidecar_ranks = 2;  // For tensor communication
}
```

### 6. Sidecar P2P Communication (`proto/v1/sidecar.proto`)

The sidecar enables direct GPU-to-GPU tensor transfer.

```protobuf
service Sidecar {
  // Send data to destination sidecars
  rpc Send(SendRequest) returns (SendResponse);

  // Receive data from producer
  rpc Receive(ReceiveRequest) returns (ReceiveResponse);

  // Mark data as consumed (free memory)
  rpc MarkDone(MarkDoneRequest) returns (MarkDoneResponse);

  // Close a streaming session
  rpc CloseStream(CloseStreamRequest) returns (CloseStreamResponse);

  // Prepare to receive data
  rpc PrepareReceive(PrepareReceiveRequest) returns (PrepareReceiveResponse);

  // Health check
  rpc CheckHealth(CheckHealthRequest) returns (CheckHealthResponse);

  // Register sidecar with group
  rpc Register(RegisterRequest) returns (RegisterResponse);
}
```

#### Sidecar Client API (`python/cornserve/sidecar/api.py`)

```python
class Sidecar:
    def __init__(self, config: SidecarConfig):
        """Initialize sidecar client."""

    async def send(
        self,
        id: str,
        dst_ranks: list[list[int]],
        data: torch.Tensor | Any,
        chunk_id: int = 0,
        num_chunks: int = 1
    ) -> None:
        """Send data to destination sidecars."""

    async def recv(
        self,
        id: str,
        chunk_id: int = 0,
        timeout: float | None = None
    ) -> torch.Tensor | Any:
        """Receive data chunk."""

    async def mark_done(self, id: str) -> None:
        """Free received data memory."""
```

#### Sidecar Configuration (`python/cornserve/sidecar/schema.py`)

```python
class SidecarConfig:
    sidecar_rank: int        # GPU rank
    group: list[int]         # Tensor parallel group
    send_slot_numel: int     # Send buffer size
    recv_slot_numel: int     # Receive buffer size
    concurrent_copy: bool    # Multi-GPU copy
    send_dtype: str          # Send tensor dtype
    recv_dtype: str          # Receive tensor dtype
```

### 7. Task Executors

#### Eric - Multimodal Data Embedding Server

**Location:** `cornserve.task_executors.eric`

```
Eric Architecture:
├── Router: FastAPI async server for modality encoding
├── Engine: Request scheduler and batch creation
├── Workers: One per GPU, tensor parallel inference
├── ModalityProcessor: Model-specific preprocessing
└── Communication: ZMQ with engine, Sidecar for inter-task data
```

#### Geri - Multimodal Content Generation Server

**Location:** `cornserve.task_executors.geri`

```
Geri Architecture:
├── Router: FastAPI async for generation requests
├── Engine: Batch scheduling for generation
├── Workers: Tensor parallel inference for generative models
└── Model-specific generators
```

#### vLLM Integration

- **Fork:** github.com/cornserve-ai/vllm v0.11.1
- **Used for:** LLM text generation unit tasks
- **Integrated via:** VLLMDescriptor

### 8. Task Registry System

Tasks are dynamically loaded from Kubernetes CRDs.

```
Task Loading Flow:
1. Gateway starts → Initialize Task Registry
2. Watch CRDs using "list then watch" pattern
3. Dynamically load task classes from CRD definitions
4. Maintain mapping: task_name → runtime class
5. Enable runtime flexibility without static compilation
```

### 9. Control Plane Services Summary

| Service | Purpose | Key APIs |
|---------|---------|----------|
| **Gateway** | Entry point, request routing | App registration, invocation |
| **Task Manager** | Spawn & manage executors | Profiling, routing, health |
| **Task Dispatcher** | Route task invocations | Deployment notifications |
| **Resource Manager** | GPU allocation | Deploy, scale, teardown |

### 10. Kubernetes Deployment

From `kubernetes/kustomize/`:

- **Sidecar StatefulSet:** One sidecar per GPU
- **Gateway Deployment:** Entry point service
- **Resource Manager:** Central resource controller
- **Task Dispatcher:** Replicated (default 3) for HA
- **Task Executor pods:** Created dynamically by Resource Manager

## Key Implementation Patterns

### 1. Automatic Task Sharing

When multiple apps need the same encoder:
```python
# App 1: Uses CLIP encoder + LLaMA
# App 2: Uses CLIP encoder + Qwen

# Cornserve automatically:
# 1. Detects both apps need CLIP encoder
# 2. Deploys single CLIP encoder Task Manager
# 3. Routes both apps' encoder requests to shared instance
```

### 2. Composite Task Execution

```
Request arrives at Gateway
    ↓
Composite task's __call__ records unit task invocations
    ↓
For each unit task:
    ├── Task Dispatcher queries Task Manager for best executor
    ├── HTTP request sent to Task Executor
    └── Result translated back to TaskOutput
    ↓
Data forwarding between stages via Sidecar
    ↓
Aggregate all results → final response
```

### 3. Dynamic Resource Scaling

```
Load Monitoring Loop:
    ↓
For each Task Manager:
    ├── Check current load vs. capacity
    ├── If high-load: Request more GPUs from Resource Manager
    ├── If low-load: Release GPUs to Resource Manager
    └── Resource Manager reallocates freed GPUs
```

## Configuration & Environment

```bash
# Key environment variables
CORNSERVE_GATEWAY_URL=<gateway_endpoint>
CORNSERVE_MOCK_SIDECAR=<true/false>  # For development
CORNSERVE_MOCK_SIDECAR_MAPPING=<path>  # Mock file paths
```

## Observability

- **Built-in OpenTelemetry support** for distributed tracing
- **Metrics export** for monitoring
- **Log streaming** during app registration

## Comparison: Process vs Container Model

| Aspect | Cornserve (Process) | AIBrix Goal (Container) |
|--------|--------------------|-----------------------|
| Isolation | OS process isolation | Container isolation |
| Scaling | Process spawn/kill | Pod scaling |
| Communication | Shared memory, ZMQ | Network, gRPC |
| Resource | Process limits | K8s resource quotas |
| Scheduling | Internal scheduler | K8s scheduler |
| Orchestration | Custom Resource Manager | K8s operators |

## Lessons for AIBrix

1. **Task Abstraction:** UnitTask/CompositeTask model is clean and powerful
2. **Profile-Driven:** Empirical profiling essential for optimization
3. **Sidecar Pattern:** P2P communication crucial for efficiency
4. **Dynamic Sharing:** Automatic component sharing reduces cost
5. **DAG Execution:** Pipeline-aware scheduling improves latency
