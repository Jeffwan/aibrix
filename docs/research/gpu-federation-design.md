# GPU 服务联邦设计方案

> 本文档基于 Karmada 联邦能力，设计 GPU 推理服务的联邦层抽象。GPU 服务相比传统微服务，在网络拓扑、P/D disaggregation、Quota 管理、服务迁移等方面有额外的复杂性。

## 目录

- [1. 问题陈述](#1-问题陈述)
- [2. 现有基础](#2-现有基础)
- [3. GPU Federation 核心抽象](#3-gpu-federation-核心抽象)
- [4. 集群 GPU 拓扑上报](#4-集群-gpu-拓扑上报)
- [5. 联邦层 GPU Quota](#5-联邦层-gpu-quota)
- [6. P/D Ratio 与联邦调度](#6-pd-ratio-与联邦调度)
- [7. Queue 在联邦层的角色](#7-queue-在联邦层的角色)
- [8. GPU Rebalancer](#8-gpu-rebalancer)
- [9. 运维场景分析](#9-运维场景分析)
- [10. 实现路径](#10-实现路径)

---

## 1. 问题陈述

### 1.1 为什么 GPU 服务联邦更复杂？

传统微服务联邦只需解决"把多少个 pod 放到哪些集群"。GPU 推理服务额外面临:

| 维度 | 微服务联邦 | GPU 服务联邦 |
|------|-----------|-------------|
| **资源类型** | CPU/Memory (同质化) | GPU (异构: H100/A100/H200, 不同互连拓扑) |
| **拓扑约束** | 无 | NVLink/NVSwitch 域内亲和, RDMA 网络 |
| **Pod 大小** | 单一规格 | Big Pod (多 GPU tensor parallelism) vs Mini Pod (单/少 GPU) |
| **角色差异** | 无状态、同质 | Prefill (计算密集, 大 Pod) vs Decode (memory-bound, Mini Pod) |
| **比例关系** | 所有 pod 等价 | P/D ratio 动态变化 (1:2 ~ 1:8) |
| **状态** | 无状态 | KV Cache (迁移时需考虑状态) |
| **调度粒度** | 单 Pod | PodGroup (gang scheduling, 多节点推理) |
| **Quota** | CPU/Memory 可互换 | GPU 型号不可互换, 拓扑组不可拆分 |
| **迁移成本** | 低 (秒级) | 高 (分钟级, 模型加载 + KV Cache warmup) |

### 1.2 核心挑战

1. **Big Pod 放置**: Prefill 角色需要 NVLink/NVSwitch 域内的连续 GPU（例如 8xH100 NVLink Pod），不是任意 8 个 GPU 都可以
2. **P/D 亲和**: Prefill 和 Decode 之间有 KV Cache 传输，跨集群延迟不可接受（除非有专用互连）
3. **异构 GPU**: 不同集群有不同 GPU 型号，Prefill 需要高端 GPU，Decode 可以用中端
4. **Quota 粒度**: 不能简单用 `nvidia.com/gpu: 100`，需要区分 `H100-NVLink: 64, A100-NVSwitch: 32`
5. **迁移代价**: GPU 服务迁移涉及模型重加载、KV Cache 冷启动，需要更精细的编排

---

## 2. 现有基础

### 2.1 AIBrix 现有 GPU 推理能力

**RoleSet** (代码: `api/orchestration/v1alpha1/roleset_types.go`):
- 多角色管理，天然支持 Prefill/Decode 角色分离
- 每个 Role 独立的 replicas、template、updateStrategy
- 集成 Godel/Coscheduling/Volcano 调度策略
- SchedulingStrategy 支持 PodGroup (gang scheduling)

**PodSet** (代码: `api/orchestration/v1alpha1/podset_types.go`):
- 原子 Pod 组 (2-100 个 Pod)
- 用于多节点 tensor parallelism（如 2 节点 16 GPU 推理）
- ReplaceUnhealthy / Recreate 恢复策略
- 集成 gang scheduling

**StormService** (代码: `api/orchestration/v1alpha1/stormservice_types.go`):
- 管理多个 RoleSet 的上层抽象
- P/D 路由集成

**P/D Router** (代码: `pkg/plugins/gateway/algorithms/pd_disaggregation.go`):
- 成熟的 Prefill/Decode 请求路由
- 负载均衡算法

**PodAutoscaler** (代码: `api/autoscaling/v1alpha1/`):
- 支持 HPA/KPA/APA 多种自动伸缩策略
- 自定义 metrics 支持

### 2.2 Karmada 联邦能力 (详见 federation-analysis.md)

- PropagationPolicy + ResourceBinding: 资源分发
- ReplicaSchedulingStrategy: 副本分配 (Divided + DynamicWeight)
- FederatedResourceQuota: 跨集群 Quota
- FederatedHPA: 跨集群自动伸缩
- SchedulerEstimator: 精确副本评估
- Descheduler: 动态重平衡
- Failover + StatePreservation: 故障转移

---

## 3. GPU Federation 核心抽象

### 3.1 设计原则

1. **不重新发明轮子**: 复用 Karmada 的 PropagationPolicy/ResourceBinding 流程
2. **不改造 AIBrix CRD**: RoleSet/StormService/PodSet 不需要感知联邦
3. **扩展而非替代**: 在 Karmada 现有抽象上扩展 GPU 感知能力
4. **关注点分离**: 拓扑约束在集群内解决，联邦层只做集群选择和副本分配

### 3.2 抽象层次

```
                    ┌─────────────────────────────────┐
                    │     联邦层 (Karmada 控制面)        │
                    │                                   │
                    │  GPUServicePropagationPolicy      │
                    │  ├── prefillPlacement             │
                    │  │   ├── gpuRequirement           │
                    │  │   ├── topologyConstraint       │
                    │  │   └── replicaScheduling        │
                    │  ├── decodePlacement              │
                    │  │   ├── gpuRequirement           │
                    │  │   └── replicaScheduling        │
                    │  ├── pdAffinity                   │
                    │  └── pdRatio                      │
                    │                                   │
                    │  FederatedGPUQuota                │
                    │  ├── overall (per GPU model)      │
                    │  └── perCluster assignments       │
                    │                                   │
                    │  GPUClusterProfile (扩展)          │
                    │  ├── gpuTopology                  │
                    │  └── gpuAvailability              │
                    └──────────┬────────────────────────┘
                               │
            ┌──────────────────┼──────────────────┐
            ▼                  ▼                  ▼
    ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
    │  Cluster A   │  │  Cluster B   │  │  Cluster C   │
    │  H100 NVLink │  │  A100 NVSw   │  │  H200        │
    │              │  │              │  │              │
    │  StormService│  │  StormService│  │  StormService│
    │  ├─ RoleSet  │  │  ├─ RoleSet  │  │  ├─ RoleSet  │
    │  │  (Prefill)│  │  │  (Decode) │  │  │  (Prefill)│
    │  │  4x PodSet│  │  │  8 Pods   │  │  │  2x PodSet│
    │  └─ RoleSet  │  │  └─ (only D) │  │  └─ RoleSet  │
    │     (Decode) │  │              │  │     (Decode) │
    │     8 Pods   │  │              │  │     4 Pods   │
    └──────────────┘  └──────────────┘  └──────────────┘
```

### 3.3 GPUServicePropagationPolicy

**基于 Karmada PropagationPolicy 扩展**，增加 GPU 服务特有的调度语义。有两种实现路径:

**路径 A: 注解/Label 扩展 (侵入性低)**

在标准 PropagationPolicy 上通过 annotation 传递 GPU 信息:

```
PropagationPolicy:
  metadata:
    annotations:
      gpu.aibrix.ai/prefill-gpu-model: "H100,H200"
      gpu.aibrix.ai/prefill-min-nvlink-domain: "8"
      gpu.aibrix.ai/decode-gpu-model: "H100,A100,H200"
      gpu.aibrix.ai/pd-affinity: "PreferSameCluster"
      gpu.aibrix.ai/pd-target-ratio: "1:4"
  spec:
    resourceSelectors:
      - apiVersion: orchestration.aibrix.ai/v1alpha1
        kind: StormService
        name: llama-70b-service
    placement:
      clusterAffinity:
        clusterNames: [cluster-a, cluster-b, cluster-c]
      replicaScheduling:
        replicaSchedulingType: Divided
        replicaDivisionPreference: Weighted
        weightPreference:
          dynamicWeight: AvailableReplicas
    propagateDeps: true
```

**路径 B: 自定义 CRD (表达力强)**

```
GPUServicePropagationPolicy
├── spec
│   ├── targetService              # 目标 StormService/RoleSet
│   │   ├── apiVersion
│   │   ├── kind
│   │   └── name
│   │
│   ├── prefillPlacement           # Prefill 角色放置策略
│   │   ├── gpuRequirement
│   │   │   ├── models: ["H100", "H200"]        # 可接受的 GPU 型号
│   │   │   ├── minGPUsPerPod: 8                 # 每 Pod 最少 GPU
│   │   │   └── topologyConstraint: NVLink       # NVLink / NVSwitch / Any
│   │   ├── podGroupConstraint
│   │   │   ├── podGroupSize: 2                  # 多节点推理 Pod 数
│   │   │   └── requireSameSwitch: true          # 要求同一 NVSwitch 域
│   │   ├── clusterAffinity                      # 集群亲和 (同 Karmada)
│   │   ├── replicaScheduling
│   │   │   ├── minReplicas: 2
│   │   │   ├── maxReplicas: 10
│   │   │   └── divisionPreference: Aggregated   # Prefill 优先集中
│   │   └── overrides[]                          # 按集群 Override
│   │
│   ├── decodePlacement            # Decode 角色放置策略
│   │   ├── gpuRequirement
│   │   │   ├── models: ["H100", "A100", "H200"]  # 可接受范围更广
│   │   │   ├── minGPUsPerPod: 1
│   │   │   └── topologyConstraint: Any
│   │   ├── clusterAffinity
│   │   ├── replicaScheduling
│   │   │   ├── minReplicas: 4
│   │   │   ├── maxReplicas: 40
│   │   │   └── divisionPreference: Weighted     # Decode 可以分散
│   │   └── overrides[]
│   │
│   ├── pdAffinity                 # P/D 亲和策略
│   │   ├── type: PreferSameCluster              # 见下文
│   │   ├── crossClusterLatencyBudget: 5ms       # 跨集群延迟预算
│   │   └── kvCacheTransferMode: RDMA            # KV Cache 传输方式
│   │
│   ├── pdRatio                    # P/D 比例控制
│   │   ├── targetRatio: "1:4"                   # 目标 P:D 比
│   │   ├── autoAdjust: true                     # 自动调整
│   │   ├── minPrefillReplicas: 1
│   │   └── minDecodeReplicas: 2
│   │
│   └── migrationPolicy            # 迁移策略
│       ├── strategy: RollingMigrate             # 滚动迁移
│       ├── migrateDecodeFirst: true             # 先迁 Decode
│       └── warmupBeforeCutover: true            # 切流前预热
```

**pdAffinity.type 选项**:

| 类型 | 语义 | 适用场景 |
|------|------|---------|
| `RequireSameCluster` | P 和 D 必须在同一集群 | 延迟敏感，无跨集群网络 |
| `PreferSameCluster` | 优先同集群，允许跨集群 | 有跨集群网络但优先本地 |
| `AllowCrossCluster` | 允许 P 和 D 分布在不同集群 | 有高速互连 (RoCE/InfiniBand) |
| `RequireSameRegion` | 必须同 Region，可跨集群 | 多集群同 Region 部署 |

### 3.4 推荐: 路径 A + 自定义 Scheduler Plugin

初期推荐路径 A（annotation 扩展），配合自定义 Karmada Scheduler 插件:

1. 用标准 PropagationPolicy + annotation 传递 GPU 约束
2. 开发 `GPUTopologyFilter` 调度插件，在 Filter 阶段根据 annotation 筛选有匹配 GPU 拓扑的集群
3. 开发 `GPUTopologyScore` 调度插件，在 Score 阶段根据 GPU 可用性打分
4. 后期根据需要升级到路径 B

---

## 4. 集群 GPU 拓扑上报

### 4.1 问题

Karmada 现有的 Cluster Status 只上报 `ResourceSummary` (Allocatable/Allocated/Allocating as ResourceList)。对 GPU 来说不够:

- `nvidia.com/gpu: 200` 无法区分 GPU 型号
- 无法表达 NVLink 域大小和可用情况
- 无法表达哪些 GPU 组可以满足 Big Pod 需求

### 4.2 GPU Topology Summary 设计

**扩展 Cluster Status**，增加 GPU 拓扑信息:

```
ClusterStatus
└── gpuTopologySummary (新增)
    ├── gpuModels[]                          # 各型号 GPU 汇总
    │   ├── model: "NVIDIA-H100-80GB"
    │   ├── total: 128
    │   ├── allocated: 96
    │   ├── available: 32
    │   └── interconnect: "NVLink"           # 互连类型
    │
    ├── topologyDomains[]                    # 拓扑域信息
    │   ├── domainType: NVLink               # NVLink / NVSwitch / PCIe
    │   ├── domainSize: 8                    # 域内 GPU 数
    │   ├── totalDomains: 16                 # 总域数
    │   ├── availableDomains: 4              # 完全可用的域数
    │   └── partiallyAvailableDomains: 3     # 部分可用的域数
    │
    ├── allocatableGroups[]                  # 可分配的 GPU 组
    │   ├── groupSize: 8                     # 组内 GPU 数
    │   ├── gpuModel: "NVIDIA-H100-80GB"
    │   ├── interconnect: NVLink
    │   └── count: 4                         # 可分配组数
    │
    └── networkCapabilities
        ├── rdmaAvailable: true
        ├── interNodeBandwidth: "400Gbps"    # 节点间带宽
        └── crossRackBandwidth: "200Gbps"    # 跨机架带宽
```

### 4.3 实现方式

**方案 A: 扩展 Karmada ClusterStatus (推荐)**

在 Karmada Cluster CRD 中增加 `gpuTopologySummary` 字段。优点是原生集成，缺点是需要修改 Karmada 代码。

**方案 B: 独立 CRD GPUClusterProfile**

```
GPUClusterProfile (per cluster, cluster-scoped)
├── metadata
│   └── name: cluster-a-gpu-profile
├── spec
│   └── clusterName: cluster-a
├── status
│   ├── gpuModels[]
│   ├── topologyDomains[]
│   ├── allocatableGroups[]
│   └── lastUpdateTime
```

优点是不修改 Karmada，缺点是需要自定义 controller 采集和 scheduler plugin 查询。

**方案 C: 利用 Karmada ResourceModels 扩展**

Karmada 已有 `ResourceModels` 机制（按资源等级给节点分级）。可以定义 GPU 特有的 ResourceModel:

```
Cluster.spec.resourceModels:
  - grade: 0   # 无 GPU
  - grade: 1   # 1-2 GPU, PCIe
  - grade: 2   # 4 GPU, NVLink
  - grade: 3   # 8 GPU, NVLink (单节点)
  - grade: 4   # 8 GPU, NVSwitch (跨节点)
  - grade: 5   # 16+ GPU, NVSwitch (多节点)
```

然后通过 `AllocatableModelings` 上报每个等级有多少节点可用。

**推荐**: 初期用方案 B (独立 CRD, 不侵入 Karmada), 成熟后考虑方案 A。

### 4.4 GPU 信息采集

在每个成员集群部署 `GPUTopologyCollector`:

```
GPUTopologyCollector (DaemonSet / Controller)
1. 读取节点 nvidia.com/gpu capacity
2. 调用 NVML 获取 GPU 互连拓扑 (NVLink/NVSwitch)
3. 结合 Pod 分配信息计算 available GPU 组
4. 聚合为集群级 GPUClusterProfile
5. 上报到 Karmada 控制面
```

---

## 5. 联邦层 GPU Quota

### 5.1 为什么需要 GPU 级 Quota

- GPU 是稀缺资源，各团队需要有明确的 Quota 保障
- 不同 GPU 型号不可互换 (H100 ≠ A100)
- 拓扑组约束: 申请 8 GPU NVLink 不等于任意 8 个 GPU
- 迁移时需要预留缓冲 (migration buffer)

### 5.2 FederatedGPUQuota 设计

**基于 Karmada FederatedResourceQuota 扩展**:

```
FederatedGPUQuota (namespace-scoped)
├── spec
│   ├── overall                               # 全局 GPU 配额
│   │   ├── nvidia.com/gpu: "200"             # 总 GPU 数
│   │   ├── aibrix.ai/gpu-h100: "120"         # H100 配额
│   │   ├── aibrix.ai/gpu-a100: "60"          # A100 配额
│   │   └── aibrix.ai/gpu-h200: "20"          # H200 配额
│   │
│   ├── topologyQuota                         # 拓扑组级 Quota (新概念)
│   │   ├── nvlink-8gpu-groups: "15"          # 8 GPU NVLink 组数
│   │   └── nvswitch-16gpu-groups: "4"        # 16 GPU NVSwitch 组数
│   │
│   ├── perClusterAssignments[]               # 每集群分配
│   │   ├── clusterName: cluster-a
│   │   │   hard:
│   │   │     aibrix.ai/gpu-h100: "80"
│   │   │   topologyHard:
│   │   │     nvlink-8gpu-groups: "10"
│   │   └── clusterName: cluster-b
│   │       hard:
│   │         aibrix.ai/gpu-a100: "60"
│   │
│   ├── migrationBuffer                       # 迁移缓冲
│   │   ├── percentage: 10                    # 预留 10% 用于迁移
│   │   └── absolute:                         # 或绝对值
│   │       nvidia.com/gpu: "20"
│   │
│   └── borrowingPolicy                       # 借用策略
│       ├── allowBorrowing: true              # 允许跨团队借用
│       ├── maxBorrowPercentage: 20           # 最多借用 20%
│       └── reclaimPolicy: Preemptive         # 回收策略
│
├── status
│   ├── overall
│   ├── overallUsed
│   ├── overallAvailable
│   ├── migrationBufferUsed                   # 迁移缓冲使用情况
│   └── perClusterStatus[]
│       ├── clusterName
│       ├── hard
│       ├── used
│       ├── available
│       └── topologyStatus
│           ├── nvlink-8gpu-groups-total: 10
│           ├── nvlink-8gpu-groups-used: 7
│           └── nvlink-8gpu-groups-available: 3
```

### 5.3 Quota 执行机制

```
联邦层 Quota 执行流程:

1. 准入控制 (联邦层)
   用户创建/更新 StormService
   → FederatedGPUQuota Webhook 检查:
     - 全局 GPU 数量是否超限
     - 对应型号 GPU 是否超限
     - 拓扑组是否足够

2. 调度约束 (Scheduler Plugin)
   GPUQuotaFilter:
   → 排除 quota 已满的集群
   → 考虑 migrationBuffer 预留

   GPUQuotaScore:
   → 优先选择 quota 余量大的集群

3. 集群级执行
   → Karmada 在成员集群创建对应 ResourceQuota
   → 集群内 admission controller 执行实际限制

4. 状态聚合
   → 定期从各集群收集 ResourceQuota.status
   → 聚合为 FederatedGPUQuota.status
```

### 5.4 Quota 与 P/D 的关系

Prefill 和 Decode 消耗不同的 Quota:

```
一个 Prefill Pod (Big Pod):
  - 消耗: 8x H100 GPU
  - 消耗: 1 个 nvlink-8gpu-group
  - 拓扑约束: 必须在同一 NVLink 域

一个 Decode Pod (Mini Pod):
  - 消耗: 1x H100 GPU (或 A100)
  - 消耗: 0 个拓扑组 (无拓扑约束)
  - 任意 GPU 即可

P/D Ratio 1:4 的一个 "单元":
  - Prefill: 8 GPU (H100) + 1 NVLink 组
  - Decode: 4 GPU (H100 或 A100)
  - 总计: 12 GPU + 1 NVLink 组
```

---

## 6. P/D Ratio 与联邦调度

### 6.1 P/D Ratio 的复杂性

P/D ratio 不是一个固定值，它取决于:
- 模型类型 (dense model vs MoE)
- 请求特征 (长 prompt vs 短 prompt)
- 延迟要求 (TTFT vs TPS 优先)
- GPU 性能 (H100 vs A100 的 P/D 效率不同)

### 6.2 联邦层 P/D Ratio 管理

```
                    联邦层
                    ┌─────────────────────────────┐
                    │                             │
                    │  PDRatioController           │
                    │  ├── 聚合各集群 P/D metrics  │
                    │  ├── 计算全局 P/D ratio      │
                    │  └── 调整各集群 P/D 分配     │
                    │                             │
                    └──────────┬──────────────────┘
                               │
         ┌─────────────────────┼─────────────────────┐
         ▼                     ▼                     ▼
    Cluster A              Cluster B              Cluster C
    P:4, D:16             P:2, D:8               P:1, D:4
    ratio=1:4             ratio=1:4              ratio=1:4

    当 Cluster A 过载时:
    → PDRatioController 决策
    → 在 Cluster B 扩 Decode (Mini Pod, 快速启动)
    → 路由层分流
    → 全局比例重新平衡
```

### 6.3 P/D Ratio 自动调整策略

```
PDRatioPolicy
├── targetMetrics
│   ├── prefillLatencyP99: 200ms        # Prefill P99 延迟目标
│   ├── decodeTPSMin: 50                 # Decode 最低吞吐
│   └── kvCacheUtilizationMax: 80%       # KV Cache 利用率上限
│
├── adjustmentRules
│   ├── scaleUpPrefillWhen:
│   │   - prefillLatencyP99 > 300ms     # Prefill 慢 → 加 Prefill
│   │   - kvCacheUtilization > 90%      # KV Cache 满 → 加 Prefill
│   ├── scaleUpDecodeWhen:
│   │   - decodeTPS < 30                 # Decode 慢 → 加 Decode
│   │   - decodeQueueLength > 100        # Decode 排队 → 加 Decode
│   └── scaleDownWhen:
│       - utilization < 30%              # 利用率低 → 缩容
│
├── constraints
│   ├── minPrefillReplicas: 1
│   ├── minDecodeReplicas: 2
│   ├── maxTotalGPU: 200                 # 受 Quota 限制
│   └── ratioRange: "1:2" ~ "1:8"       # P/D ratio 范围
│
└── crossClusterPolicy
    ├── preferBalancedRatio: true        # 各集群尽量保持相同 ratio
    └── allowAsymmetricRatio: true       # 允许不同集群不同 ratio
```

### 6.4 与 FederatedHPA 集成

现有 Karmada FederatedHPA 基于标准 metrics。对 GPU 服务需要扩展:

```
FederatedHPA (用于 Decode 角色):
  spec:
    scaleTargetRef:
      apiVersion: orchestration.aibrix.ai/v1alpha1
      kind: RoleSet
      name: llama-70b-decode
    minReplicas: 4
    maxReplicas: 40
    metrics:
      - type: Pods
        pods:
          metric:
            name: aibrix_decode_tokens_per_second
          target:
            type: AverageValue
            averageValue: "50"

FederatedHPA (用于 Prefill 角色):
  spec:
    scaleTargetRef:
      apiVersion: orchestration.aibrix.ai/v1alpha1
      kind: RoleSet
      name: llama-70b-prefill
    minReplicas: 1
    maxReplicas: 10
    metrics:
      - type: Pods
        pods:
          metric:
            name: aibrix_prefill_latency_p99_ms
          target:
            type: AverageValue
            averageValue: "200"
```

P/D ratio 通过两个独立的 FederatedHPA 间接实现，PDRatioController 作为上层协调器确保全局 ratio 合理。

---

## 7. Queue 在联邦层的角色

### 7.1 集群内 Queue

各集群使用 Volcano/Godel/Kueue 的 Queue 管理资源:

```
集群内 Queue 层级:
Cluster
├── Queue: gpu-high-priority
│   ├── capacity: {nvidia.com/gpu: 64}
│   ├── used: {nvidia.com/gpu: 50}
│   └── pending jobs: 3
├── Queue: gpu-default
│   ├── capacity: {nvidia.com/gpu: 128}
│   ├── used: {nvidia.com/gpu: 100}
│   └── pending jobs: 8
└── Queue: gpu-preemptible
    ├── capacity: {nvidia.com/gpu: 32}
    ├── used: {nvidia.com/gpu: 32}
    └── pending jobs: 15
```

### 7.2 联邦层 Queue 聚合

```
FederatedQueueStatus (只读, 聚合状态)
├── queueName: gpu-serving
├── clusterStatuses[]
│   ├── clusterName: cluster-a
│   │   ├── capacity: {nvidia.com/gpu: 64}
│   │   ├── used: {nvidia.com/gpu: 50}
│   │   ├── available: {nvidia.com/gpu: 14}
│   │   └── pendingJobs: 3
│   ├── clusterName: cluster-b
│   │   ├── capacity: {nvidia.com/gpu: 128}
│   │   ├── used: {nvidia.com/gpu: 100}
│   │   ├── available: {nvidia.com/gpu: 28}
│   │   └── pendingJobs: 8
├── aggregated
│   ├── totalCapacity: {nvidia.com/gpu: 192}
│   ├── totalUsed: {nvidia.com/gpu: 150}
│   ├── totalAvailable: {nvidia.com/gpu: 42}
│   └── totalPending: 11
```

### 7.3 Queue 与调度决策

联邦调度器在选择集群时参考 Queue 状态:

```
GPUQueueAwareScore (Scheduler Plugin):
  对每个候选集群:
    queueStatus = getQueueStatus(cluster, targetQueue)
    score += normalize(queueStatus.available / queueStatus.capacity) * 50  # 空闲率
    score -= normalize(queueStatus.pendingJobs) * 30                        # 排队惩罚
    score += isHighPriorityQueue(targetQueue) * 20                          # 高优先级加分
```

### 7.4 Queue 名称映射

不同集群可能有不同的 Queue 名称，通过 OverridePolicy 映射:

```
OverridePolicy:
  spec:
    targetCluster:
      clusterNames: [cluster-b]
    overriders:
      annotationsOverrider:
        - operator: replace
          value:
            volcano.sh/queue-name: "gpu-serving-b"  # cluster-b 用不同 Queue 名
```

---

## 8. GPU Rebalancer

### 8.1 为什么需要 GPU 专用 Rebalancer

Karmada 现有 Descheduler 不够:
- 仅支持 Deployment (GPU 服务用 RoleSet/StormService)
- 不考虑 GPU 拓扑约束
- 不理解 P/D 关系
- 迁移成本评估缺失

### 8.2 GPUServiceRebalancer 设计

```
GPUServiceRebalancer (Controller)
│
├── 触发条件
│   ├── 定期扫描 (每 5 分钟)
│   ├── 集群 GPU 资源告警 (utilization > 90%)
│   ├── 新集群加入 (有新 GPU 资源)
│   └── Quota 变更
│
├── 评估流程
│   1. 收集各集群 GPU 状态
│   │   ├── GPUClusterProfile (拓扑信息)
│   │   ├── FederatedGPUQuota (Quota 使用)
│   │   └── Queue 状态
│   │
│   2. 计算不均衡指标
│   │   ├── GPU 利用率差异 (cluster variance)
│   │   ├── Quota 余量分布
│   │   ├── P/D ratio 偏差
│   │   └── 拓扑组碎片化程度
│   │
│   3. 生成迁移计划
│   │   ├── 候选迁移: 哪些 Pod/RoleSet 可迁移
│   │   ├── 目标集群: 基于 GPU 拓扑匹配
│   │   ├── 迁移成本: 模型大小 × 加载时间
│   │   └── 优先级: 低优先级先迁移
│   │
│   4. 执行迁移 (分步, 可回滚)
│
└── 迁移策略
    ├── RollingMigrate          # 滚动迁移 (先扩后缩)
    ├── BlueGreenMigrate        # 蓝绿迁移 (全量切换)
    └── DecodeFirstMigrate      # 先迁 Decode 再迁 Prefill
```

### 8.3 迁移成本模型

```
MigrationCost = ModelLoadTime + KVCacheWarmupTime + TrafficDrainTime

其中:
  ModelLoadTime = ModelSize / DiskBandwidth + InitTime
    - Llama-70B: ~70GB / 10GB/s + 30s ≈ 37s
    - Llama-405B: ~405GB / 10GB/s + 60s ≈ 100s

  KVCacheWarmupTime = f(CacheSize, RequestRate)
    - Cold start: 无历史 Cache, 需要重新积累
    - Warm transfer: 通过 RDMA 传输 (如果跨集群有 RDMA)

  TrafficDrainTime = f(InFlightRequests, Timeout)
    - 通常 30-60s 优雅排空
```

迁移决策公式:

```
ShouldMigrate = (ImbalanceBenefit - MigrationCost) > Threshold

ImbalanceBenefit:
  - 消除 GPU 碎片化 → 释放 NVLink 组
  - 降低延迟 → P/D 同集群
  - 提升利用率 → 均匀分布负载
```

---

## 9. 运维场景分析

### Scenario 1: GPU 不够 — 挪服务

```
前提:
  - Cluster A: 128 H100 GPU (NVLink), 已用 120
  - Cluster B: 64 A100 GPU (NVSwitch), 已用 40
  - Service: llama-70b, P:4(each 8GPU), D:16(each 1GPU) on Cluster A
  - 新需求: 需要再扩 2 个 Prefill Pod (16 GPU)
  - 问题: Cluster A 只剩 8 GPU, 不够 2 个 Prefill Pod

决策流程:

1. FederatedHPA/用户 → 尝试扩 Prefill replicas
   ↓
2. Karmada Scheduler → 发现 Cluster A 无法满足
   ↓
3. GPUTopologyFilter → 检查 Cluster B:
   - A100 可接受 (在 gpuRequirement.models 中)
   - 但 NVSwitch 拓扑, 需要评估性能差异
   ↓
4. 方案评估:
   a) 在 Cluster A 腾挪 → 迁走低优先级 Decode 到 Cluster B
      - 释放 8 GPU on A → 满足 1 个 Prefill
      - 剩余 1 个 Prefill 放 Cluster B (A100)
   b) 在 Cluster B 直接部署 Prefill → 用 A100
      - 性能降低但可用
   c) 在 Cluster B 部署 Decode → 释放 Cluster A 的 Decode GPU
      - 最小化 Prefill 性能影响
   ↓
5. 执行方案 c) (推荐):
   Step 1: 在 Cluster B 创建 8 个 Decode Pod (A100)
   Step 2: 路由层将 Decode 流量逐步切到 Cluster B
   Step 3: 缩减 Cluster A 的 8 个 Decode Pod
   Step 4: Cluster A 释放 8 GPU
   Step 5: 在 Cluster A 创建 1 个新 Prefill Pod (H100 NVLink)
   Step 6: 如果还需要, 在 Cluster B 创建 Prefill Pod (A100)
   ↓
6. 结果:
   Cluster A: P:5(40GPU), D:8(8GPU) → 总 48 GPU / 128
   Cluster B: P:0or1, D:8(8GPU) → 总 8-16 GPU / 64
```

### Scenario 2: GPU 异构环境 — 最优放置

```
前提:
  - Cluster A: 64x H100-SXM (NVLink, 8GPU/域), 高速 RDMA
  - Cluster B: 128x A100-SXM (NVSwitch, 8GPU/域)
  - Cluster C: 32x H200 (NVLink, 8GPU/域), 新一代
  - Service: Mixtral-8x7B (MoE model)

最优放置策略:

1. Prefill 放置:
   - 优先 Cluster C (H200): 最新 GPU, MoE 推理性能最好
   - 次选 Cluster A (H100): NVLink 拓扑适合大 batch prefill
   - 不选 Cluster B (A100): MoE 模型在 A100 上效率较低

2. Decode 放置:
   - Cluster B (A100): 性价比高, decode 对 GPU 代际不敏感
   - Cluster A (H100): 如果 Cluster B 容量不够

3. P/D 亲和:
   - 如果 pdAffinity=AllowCrossCluster:
     - P on Cluster C, D on Cluster B
     - 需要 Cluster B↔C 有 RDMA 互连
   - 如果 pdAffinity=PreferSameCluster:
     - P on Cluster C, D on Cluster C (容量有限)
     - 溢出的 D 放 Cluster A

4. OverridePolicy:
   - Cluster B: 调整模型量化参数 (A100 用 FP8 效率低, 用 INT8)
   - Cluster C: 使用 FP8 (H200 原生支持)

GPU 联邦的 OverridePolicy 示例:
  OverridePolicy:
    targetCluster:
      clusterNames: [cluster-b]
    overriders:
      fieldOverrider:
        - subPath: /spec/template/spec/containers/0/env
          patchesJSON6902:
            - op: add
              path: /-
              value:
                name: QUANTIZATION_MODE
                value: "int8"
```

### Scenario 3: Quota 超限回收

```
前提:
  - Team A quota: 100 GPU (H100: 60, A100: 40)
  - Team A 实际使用: 110 GPU (超用 10 GPU, 因 borrowing)
  - Team B 需要使用被借出的 GPU

回收流程:

1. FederatedGPUQuota Controller 检测超限
   ↓
2. 检查 borrowingPolicy:
   - reclaimPolicy: Preemptive → 可以抢占
   ↓
3. 选择回收目标:
   a) 优先回收低优先级服务的 Decode Pod (影响小)
   b) 找到 Team A 优先级最低的服务
   c) 选择 Quota 超出最多的集群
   ↓
4. 执行回收:
   Step 1: 通知 Team A (event + alert)
   Step 2: grace period (5 分钟)
   Step 3: 缩减 Team A 低优先级服务副本
   Step 4: 验证 Team A 使用量 ≤ 100
   Step 5: 释放的 GPU 可用于 Team B
```

### Scenario 4: 集群故障 Failover

```
前提:
  - Cluster A 故障 (网络分区 / 电力故障)
  - Cluster A 上运行: P:4(32GPU), D:16(16GPU) for llama-70b
  - 可用集群: Cluster B (40 free A100), Cluster C (16 free H200)

Failover 流程:

1. Karmada 检测 Cluster A 不健康
   → ClusterConditionReady = False
   → 触发 Failover
   ↓
2. GPUServiceRebalancer 评估:
   - 需要迁移: 4 Prefill (各 8GPU) + 16 Decode (各 1GPU)
   - 总需: 48 GPU
   - Cluster B 可提供: 40 A100 (5 个 NVSwitch 域)
   - Cluster C 可提供: 16 H200 (2 个 NVLink 域)
   ↓
3. 迁移计划:
   Phase 1 - Decode 优先 (快速恢复部分服务):
     - Cluster B: 部署 12 Decode Pod (12 A100)
     - Cluster C: 部署 4 Decode Pod (4 H200)
     - 恢复 Decode 服务

   Phase 2 - Prefill (恢复完整服务):
     - Cluster C: 部署 2 Prefill Pod (16 H200, 2 NVLink 域)
     - Cluster B: 部署 2 Prefill Pod (16 A100, 2 NVSwitch 域)
     - 或: 仅 Cluster C 部署 Prefill (更优互连)
   ↓
4. StatePreservation:
   - KV Cache: 无法保留 (集群故障)
   - Model weights: 从 object storage 重新加载
   - Routing state: 联邦路由层自动更新
   ↓
5. 流量切换:
   - 联邦 Gateway 更新后端集群列表
   - P/D Router 适配新的集群拓扑
   ↓
6. Cluster A 恢复后:
   - 不自动回迁 (避免抖动)
   - 运维手动决定是否回迁
   - 或: 通过 Rebalancer 逐步回迁
```

### Scenario 5: 新集群上线 — 资源扩容

```
前提:
  - 现有 Cluster A/B 运行正常
  - 新采购的 Cluster C (256x H200) 上线
  - 多个团队的 GPU 服务需要扩容

扩容流程:

1. Cluster C 注册到 Karmada
   → GPUTopologyCollector 上报拓扑信息
   → GPUClusterProfile 创建
   ↓
2. FederatedGPUQuota 更新:
   - 增加 overall 总量
   - 增加 Cluster C 的 perClusterAssignment
   ↓
3. GPUServiceRebalancer 触发 (新集群加入):
   评估哪些服务应该利用新集群:
   a) 正在排队的扩容请求 → 优先满足
   b) 当前跨集群 P/D 的服务 → 迁回同集群
   c) 高利用率集群的服务 → 部分迁移到新集群
   ↓
4. 逐步迁移:
   - 不一次性大规模迁移
   - 按优先级逐个服务调整
   - 每次调整后观察 10 分钟再继续
```

---

## 10. 实现路径

### Phase 1: 基础能力 (Month 1-2)

1. **GPUClusterProfile CRD + Controller**
   - 定义 GPU 拓扑上报数据结构
   - 在各集群部署 Collector
   - 上报到 Karmada 控制面

2. **GPU Scheduler Plugin**
   - `GPUTopologyFilter`: 筛选有匹配 GPU 拓扑的集群
   - `GPUTopologyScore`: 根据 GPU 可用性和拓扑匹配度打分
   - 注册为 Karmada Scheduler 插件

3. **GPU Annotation Convention**
   - 定义 annotation key 规范 (gpu.aibrix.ai/*)
   - PropagationPolicy + annotation 实现基本 GPU 感知调度

### Phase 2: Quota + P/D (Month 3-4)

4. **FederatedGPUQuota**
   - 扩展 FederatedResourceQuota 支持 GPU 型号粒度
   - 拓扑组 Quota (NVLink 组数)
   - Migration buffer 预留
   - Quota 准入 Webhook

5. **P/D 联邦调度**
   - pdAffinity 实现 (基于 WorkloadAffinity 扩展)
   - P/D ratio monitoring + controller
   - 与 FederatedHPA 集成

### Phase 3: Rebalancer + 运维 (Month 5-6)

6. **GPUServiceRebalancer**
   - GPU 感知的重平衡算法
   - 迁移成本模型
   - 分步迁移编排

7. **运维工具**
   - GPU 联邦 Dashboard (各集群 GPU 使用/拓扑)
   - 迁移计划预览 (dry-run)
   - 告警集成 (Quota 超限, 利用率不均)

### Phase 4: 优化 (Month 7+)

8. **高级特性**
   - Queue 联邦聚合
   - 跨集群 KV Cache 迁移 (基于 RDMA)
   - 动态 P/D ratio 自动调优
   - 多模型混部的联邦 Quota 调度

---

## 附录: 术语表

| 术语 | 说明 |
|------|------|
| **Big Pod** | 使用多个 GPU 的 Pod，通常用于 Prefill 角色，需要 NVLink/NVSwitch 互连 |
| **Mini Pod** | 使用少量 GPU 的 Pod，通常用于 Decode 角色，对互连要求低 |
| **P/D Disaggregation** | Prefill 和 Decode 分离部署，各自独立扩缩 |
| **P/D Ratio** | Prefill 与 Decode 副本/GPU 数量的比例 |
| **NVLink** | NVIDIA GPU 间的高速互连，通常 8 GPU 一个域 |
| **NVSwitch** | 跨节点的 GPU 互连技术 |
| **KV Cache** | 推理过程中缓存的 Key-Value 状态，Decode 阶段使用 |
| **Topology Domain** | 一组通过高速互连连接的 GPU |
| **Migration Buffer** | Quota 中预留给迁移操作的 GPU 缓冲 |
| **Gang Scheduling** | 一组 Pod 必须全部调度成功或全部不调度 |
| **SchedulerEstimator** | Karmada 组件，精确评估集群可调度资源 |
