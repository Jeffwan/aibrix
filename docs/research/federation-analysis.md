# Kubernetes 多集群联邦项目分析: Karmada vs KubeAdmiral

> 本文档对比分析 Karmada 和 KubeAdmiral 两个 Kubernetes 联邦项目的核心抽象，包括联邦部署、HPA、Quota、集群资源上报、Rebalancer 等能力，为 GPU 服务联邦设计提供参考。

## 目录

- [1. 项目概览](#1-项目概览)
- [2. Federation Deployment 抽象](#2-federation-deployment-抽象)
- [3. 副本调度策略](#3-副本调度策略)
- [4. Override 策略](#4-override-策略)
- [5. Federation HPA](#5-federation-hpa)
- [6. Quota 管理](#6-quota-管理)
- [7. 集群资源上报](#7-集群资源上报)
- [8. Queue 与集群粒度上报](#8-queue-与集群粒度上报)
- [9. Rebalancer / Descheduler](#9-rebalancer--descheduler)
- [10. Scheduler 架构](#10-scheduler-架构)
- [11. Failover 机制](#11-failover-机制)
- [12. 对比总结](#12-对比总结)

---

## 1. 项目概览

| 维度 | Karmada | KubeAdmiral |
|------|---------|-------------|
| 来源 | CNCF 毕业项目 (华为主导) | 字节跳动 (kubewharf) |
| 架构基础 | 独立设计，参考 K8s 调度器 | 基于 Kubernetes Federation v2 扩展 |
| API Group | `policy.karmada.io`, `work.karmada.io`, `cluster.karmada.io`, `autoscaling.karmada.io` | `core.kubeadmiral.io` |
| 集群同步模式 | Push + Pull | Push |
| 核心抽象 | PropagationPolicy → ResourceBinding → Work | FederatedObject + PropagationPolicy |
| 调度框架 | Filter → Score → Select (内置插件) | Filter → Score → Select (支持 Webhook 扩展) |

### Karmada 核心流程

```
用户资源(Deployment 等) + PropagationPolicy
    ↓
ResourceDetector (匹配资源与策略)
    ↓
ResourceBinding (资源绑定关系)
    ↓
Scheduler (调度决策: 选集群 + 分副本)
    ↓
ResourceBinding.Status (调度结果)
    ↓
BindingController → Work (每个目标集群一个 Work 对象)
    ↓
ExecutionController → 在成员集群创建实际资源
```

### KubeAdmiral 核心流程

```
FederatedObject (Template + Overrides + Placements)
    + PropagationPolicy
    ↓
Scheduler (Filter → Score → Select)
    ↓
FederatedObject.Placements (调度结果)
    ↓
SyncController → 在成员集群创建实际资源
    ↓
StatusAggregator → CollectedStatus
```

---

## 2. Federation Deployment 抽象

### 2.1 Karmada: PropagationPolicy + 原生资源

Karmada 的核心设计理念是**不改变用户资源模型**。用户直接在 Karmada 控制面创建标准 Kubernetes 资源（如 Deployment），然后通过 `PropagationPolicy` 定义分发规则。

**PropagationPolicy** 核心字段（代码路径: `karmada/pkg/apis/policy/v1alpha1/propagation_types.go`）:

```
PropagationPolicy
├── spec
│   ├── resourceSelectors[]        # 选择目标资源 (apiVersion + kind + name/labelSelector)
│   ├── placement
│   │   ├── clusterAffinity        # 集群亲和 (clusterNames, labels, fieldSelector)
│   │   ├── clusterAffinities[]    # 多组集群亲和 (按优先级依次尝试)
│   │   ├── clusterTolerations[]   # 容忍集群 taints
│   │   ├── spreadConstraints[]    # 分散约束 (按 Cluster/Region/Zone/Provider 维度)
│   │   ├── replicaScheduling      # 副本调度策略 (见下节)
│   │   └── workloadAffinity       # 工作负载间亲和
│   ├── propagateDeps              # 是否自动传播依赖 (ConfigMap, Secret 等)
│   ├── priority                   # 策略优先级
│   ├── failover                   # 故障转移行为
│   ├── schedulePriority           # 调度优先级 (alpha)
│   └── suspension                 # 暂停分发控制
```

**ResourceBinding**（中间状态对象，代码路径: `karmada/pkg/apis/work/v1alpha2/binding_types.go`）:

```
ResourceBinding
├── spec
│   ├── resource                   # 引用的资源 (GVK + Name)
│   ├── placement                  # 从 PropagationPolicy 复制的放置规则
│   ├── requiredBy[]               # 被哪些资源依赖
│   └── clusters[]                 # 调度结果: 目标集群 + 副本数
├── status
│   ├── schedulerObservedGeneration
│   ├── schedulerObservedAffinityName
│   └── aggregatedStatus[]         # 各集群资源状态聚合
```

### 2.2 KubeAdmiral: FederatedObject

KubeAdmiral 沿用 Fed v2 的 `FederatedObject` 模式，将模板、Override、Placement 封装在一个对象中。

**FederatedObject** 核心字段（源码: `kubeadmiral/pkg/apis/core/v1alpha1/types_federatedobject.go`）:

```
FederatedObject / ClusterFederatedObject
├── spec (GenericFederatedObjectSpec)
│   ├── template             # 原始 K8s 资源 JSON (RawExtension)
│   ├── overrides[]          # 按 controller 分组的集群级 patch
│   │   ├── controller       # 控制器名 (如 scheduler)
│   │   └── clusters[]       # ClusterReferenceWithPatches
│   │       ├── cluster      # 集群名
│   │       └── patches[]    # JSON patch (op, path, value)
│   ├── placements[]         # 按 controller 分组的放置结果
│   │   ├── controller       # 控制器名
│   │   └── clusters[]       # 目标集群列表
│   └── follows[]            # 依赖跟随 (LeaderReference: group/kind/name)
├── status (GenericFederatedObjectStatus)
│   ├── syncedGeneration
│   ├── conditions[]
│   └── clusters[]           # 每集群传播状态 (PropagationStatus)
```

**PropagationPolicy** 核心字段（源码: `kubeadmiral/pkg/apis/core/v1alpha1/types_propagationpolicy.go`）:

```
PropagationPolicy / ClusterPropagationPolicy
├── spec
│   ├── schedulingMode          # Duplicate 或 Divide
│   ├── replicasStrategy        # Binpack 或 Spread
│   ├── clusterSelector         # label selector
│   ├── clusterAffinity[]       # 多组选择器 (OR 逻辑)
│   ├── tolerations[]           # 集群 taint 容忍
│   ├── maxClusters             # 最大集群数
│   ├── placements[]            # 显式集群列表 + 偏好
│   │   └── preferences
│   │       ├── minReplicas/maxReplicas  # 每集群副本范围
│   │       ├── weight                    # 分配权重 (Spread 模式)
│   │       └── priority                  # 集群优先级 (Binpack 模式)
│   ├── autoMigration           # 自动迁移配置
│   │   ├── when: Unschedulable
│   │   └── keepUnschedulableReplicas
│   ├── reschedulePolicy        # 重调度策略
│   │   ├── triggers[]          # 触发条件 (PolicyChange/ClusterJoined/ClusterLabelsChange/...)
│   │   └── replicaRescheduling
│   └── disableFollowerScheduling
```

### 2.3 关键差异

| 维度 | Karmada | KubeAdmiral |
|------|---------|-------------|
| **资源模型** | 原生 K8s 资源 + 独立 Policy | FederatedObject 封装 (Template+Override+Placement) |
| **策略绑定** | ResourceSelector 匹配 (松耦合) | FederatedTypeConfig 注册 + PolicyLabel 绑定 |
| **中间对象** | ResourceBinding → Work | 直接在 FederatedObject 上操作 |
| **依赖传播** | PropagateDeps=true 自动传播 | Follows (LeaderReference) 跟随调度 |
| **集群亲和** | ClusterAffinity + ClusterAffinities (分组优先级) | ClusterSelector + ClusterAffinity (OR 逻辑) |
| **分散约束** | SpreadConstraints (Cluster/Region/Zone/Provider 多维度) | MaxClusters (简单数量限制) |
| **工作负载亲和** | WorkloadAffinity (workload 间的集群亲和) | 无直接对应 |

---

## 3. 副本调度策略

### 3.1 Karmada ReplicaSchedulingStrategy

代码路径: `karmada/pkg/apis/policy/v1alpha1/propagation_types.go:708`

```
ReplicaSchedulingStrategy
├── replicaSchedulingType
│   ├── Duplicated    # 每个集群部署相同副本数 (全量复制)
│   └── Divided       # 将总副本数分割到各集群
├── replicaDivisionPreference (仅 Divided 模式)
│   ├── Aggregated    # 尽量少集群 (类似 Binpack), 基于集群可调度副本数
│   └── Weighted      # 按权重分配
│       └── weightPreference
│           ├── staticWeightList[]   # 静态权重 (TargetCluster + Weight)
│           └── dynamicWeight        # 动态权重因子 (当前仅支持 AvailableReplicas)
```

**动态权重 (DynamicWeight=AvailableReplicas)**: 根据各集群当前可用副本数动态计算分配比例。Karmada 通过 `SchedulerEstimator` gRPC 服务精确评估每个集群可以承载的副本数。

**Aggregated 模式的分配算法**（代码路径: `karmada/pkg/scheduler/core/division_algorithm.go`）:
1. 按集群可用资源排序
2. 贪心填充: 优先填满资源最充足的集群
3. 确保不超过单集群可调度上限

### 3.2 KubeAdmiral ReplicasStrategy

```
SchedulingMode: Duplicate | Divide

ReplicasStrategy (仅 Divide 模式):
├── Binpack     # 最少集群数 (按 priority 排序填充)
│   └── per-cluster preferences.priority  # 优先级越高越先被填充
└── Spread      # 按权重分散
    └── per-cluster preferences.weight    # 权重比例分配
    └── per-cluster preferences.minReplicas / maxReplicas  # 每集群上下限约束
```

### 3.3 对比

| 维度 | Karmada | KubeAdmiral |
|------|---------|-------------|
| **模式** | Duplicated / Divided | Duplicate / Divide |
| **Divided 子策略** | Aggregated / Weighted | Binpack / Spread |
| **动态权重** | DynamicWeight=AvailableReplicas | 无 (静态权重) |
| **精确评估** | SchedulerEstimator gRPC | 基于集群 Allocatable/Available |
| **每集群约束** | 无显式 min/max | minReplicas / maxReplicas |
| **分配算法** | 内置贪心 + 均衡 | 按 priority/weight 分配 |

---

## 4. Override 策略

### 4.1 Karmada OverridePolicy

代码路径: `karmada/pkg/apis/policy/v1alpha1/override_types.go`

```
OverridePolicy / ClusterOverridePolicy
├── spec
│   ├── targetCluster           # 目标集群匹配
│   │   ├── clusterNames[]
│   │   ├── labelSelector
│   │   └── fieldSelector
│   └── overriders
│       ├── plaintext[]         # 通用 JSON patch (path + op + value)
│       ├── imageOverrider[]    # 镜像修改 (component, operator, value)
│       ├── commandArgsOverrider[]  # 命令行参数
│       ├── labelsOverrider[]   # 标签操作
│       ├── annotationsOverrider[]  # 注解操作
│       └── fieldOverrider[]    # 任意字段 (subPath + JSON/YAML patch)
```

### 4.2 KubeAdmiral OverridePolicy

```
OverridePolicy / ClusterOverridePolicy
├── spec
│   ├── overrideRules[]
│   │   ├── targetClusters      # 集群匹配
│   │   └── overriders
│   │       ├── image[]         # 镜像修改
│   │       ├── command[]       # 命令修改
│   │       ├── args[]          # 参数修改
│   │       ├── envs[]          # 环境变量修改
│   │       ├── annotations[]   # 注解修改
│   │       ├── labels[]        # 标签修改
│   │       └── jsonpatch[]     # 通用 JSON patch
```

两者能力类似，Karmada 的 fieldOverrider 更灵活（支持 YAML patch），KubeAdmiral 额外支持 envs 修改。

---

## 5. Federation HPA

### 5.1 Karmada FederatedHPA

代码路径: `karmada/pkg/apis/autoscaling/v1alpha1/federatedhpa_types.go`

Karmada 提供原生的 `FederatedHPA` CRD，在联邦层面进行跨集群自动伸缩。

```
FederatedHPA
├── spec
│   ├── scaleTargetRef          # 目标资源 (apiVersion + kind + name)
│   ├── minReplicas             # 全局最小副本数
│   ├── maxReplicas             # 全局最大副本数
│   ├── behavior                # 扩缩行为 (同 K8s HPA)
│   │   ├── scaleUp
│   │   └── scaleDown
│   └── metrics[]               # 指标定义 (同 K8s MetricSpec)
│       ├── type: Resource/Pods/Object/External
│       └── resource
│           ├── name: cpu/memory
│           └── target: Utilization/AverageValue/Value
├── status
│   ├── currentReplicas
│   ├── desiredReplicas
│   ├── currentMetrics[]
│   └── conditions[]
```

**工作原理**:
1. FederatedHPA Controller 从各成员集群的 metrics server 聚合指标
2. 使用标准 HPA 算法计算目标副本数
3. 更新目标资源（如 Deployment）的 replicas
4. Karmada Scheduler 根据 ReplicaSchedulingStrategy 将新副本数分配到各集群

**CronFederatedHPA**: Karmada 还提供 `CronFederatedHPA`，支持定时扩缩，可与 FederatedHPA 联合使用。

### 5.2 KubeAdmiral HPA Aggregator

KubeAdmiral 通过 `pkg/apis/hpaaggregator/` 提供 HPA 聚合能力。

其思路不同于 Karmada 的集中式 FederatedHPA:
- KubeAdmiral 在各集群内部署标准 K8s HPA
- HPAAggregator 聚合各集群 HPA 的状态
- 联邦层协调各集群 HPA 的 min/max 范围

### 5.3 对比

| 维度 | Karmada | KubeAdmiral |
|------|---------|-------------|
| **模式** | 集中式 FederatedHPA (联邦层计算) | 分布式 HPA + 聚合 |
| **指标聚合** | 联邦层从各集群收集 metrics | 各集群独立 HPA，聚合状态 |
| **决策点** | 联邦控制面 | 各集群本地 (联邦层协调) |
| **定时扩缩** | CronFederatedHPA | 无 |
| **副本分配** | 与 ReplicaScheduling 联动 | 与 PropagationPolicy 联动 |

---

## 6. Quota 管理

### 6.1 Karmada FederatedResourceQuota

代码路径: `karmada/pkg/apis/policy/v1alpha1/federatedresourcequota_types.go`

Karmada 提供 `FederatedResourceQuota` CRD，实现跨集群的资源配额管理。

```
FederatedResourceQuota (namespace-scoped)
├── spec
│   ├── overall                     # 全局 quota 总量 (ResourceList)
│   │   例: cpu: "100", memory: "200Gi", nvidia.com/gpu: "50"
│   └── staticAssignments[]         # 每集群静态分配
│       ├── clusterName: "cluster-a"
│       │   hard: {cpu: "40", nvidia.com/gpu: "20"}
│       └── clusterName: "cluster-b"
│           hard: {cpu: "60", nvidia.com/gpu: "30"}
│   # dynamicAssignments (设计中，尚未实现)
│   # - 动态分配规则
│   # - 跨集群 quota 均衡
│   # - 与 FederatedHPA 协作
├── status
│   ├── overall                     # 实际生效的总配额
│   ├── overallUsed                 # 全局实际使用量
│   └── aggregatedStatus[]          # 每集群使用状态
│       ├── clusterName
│       ├── hard                    # 该集群配额
│       └── used                    # 该集群使用量
```

**工作原理**:
1. 用户创建 FederatedResourceQuota 定义全局配额和每集群配额
2. Karmada 在各集群创建对应的 ResourceQuota
3. 定期聚合各集群 ResourceQuota 的 used 状态
4. 注意: **当前调度器不使用此配额做调度决策**（文档注释: "The Karmada scheduler currently does NOT use this configuration for scheduling decisions"）

**支持 GPU**: `overall` 和 `hard` 字段使用标准 `corev1.ResourceList`，天然支持 `nvidia.com/gpu` 等扩展资源。

### 6.2 KubeAdmiral Quota

KubeAdmiral **没有独立的联邦 Quota CRD**。其方案是:
- 依赖各集群内的标准 K8s ResourceQuota
- 通过 FederatedCluster Status 中的 Allocatable/Available 上报可用资源
- 调度器在选择集群时参考可用资源

### 6.3 对比

| 维度 | Karmada | KubeAdmiral |
|------|---------|-------------|
| **联邦 Quota CRD** | FederatedResourceQuota | 无 |
| **Quota 层级** | 全局总量 + 每集群分配 | 仅集群级 K8s ResourceQuota |
| **GPU 支持** | 通过 ResourceList 支持任意扩展资源 | 通过集群状态间接支持 |
| **动态分配** | 设计中 (DynamicAssignments) | 无 |
| **调度集成** | 当前不集成 (计划中) | 通过集群可用资源影响调度 |
| **使用聚合** | 支持 (aggregatedStatus) | 通过集群状态获取 |

---

## 7. 集群资源上报

### 7.1 Karmada Cluster Status

代码路径: `karmada/pkg/apis/cluster/v1alpha1/types.go`

```
Cluster
├── spec
│   ├── syncMode: Push | Pull
│   ├── region / zones[]         # 地域/可用区
│   ├── provider                 # 云厂商
│   ├── taints[]                 # 集群污点
│   └── resourceModels[]         # 资源建模 (自定义资源等级)
│       ├── grade: 0..8
│       └── ranges[]
│           ├── name: cpu/memory/...
│           ├── min
│           └── max
├── status
│   ├── kubernetesVersion
│   ├── conditions[]             # Ready, CompleteAPIEnablements
│   ├── nodeSummary
│   │   ├── totalNum
│   │   └── readyNum
│   ├── resourceSummary
│   │   ├── allocatable          # 总可分配资源 (ResourceList)
│   │   │   例: cpu: "1000", memory: "4000Gi", nvidia.com/gpu: "200"
│   │   ├── allocating           # 等待调度的资源量
│   │   └── allocated            # 已调度的资源量
│   │   # available = allocatable - allocated - allocating
│   │   └── allocatableModelings[]  # 按资源等级统计节点数
│   │       ├── grade: 2
│   │       └── count: 10        # 10 个节点属于 grade 2
│   └── apiEnablements[]         # 集群支持的 API 资源
│   └── remedyActions[]          # 补救措施
```

**ResourceModels**: Karmada 的资源建模机制将节点按资源量分级（0-8 级），例如:
- Grade 0: CPU [0, 1C), Memory [0, 4GB)
- Grade 7: CPU [64C, 128C), Memory [512GB, 1024GB)
- Grade 8: CPU [128C, ∞), Memory [1024GB, ∞)

这个模型可以扩展支持 GPU，例如定义 GPU 数量等级。

**AllocatableModelings**: 统计每个资源等级有多少节点可用，供调度器做粗粒度评估。

### 7.2 KubeAdmiral FederatedCluster

源码: `kubeadmiral/pkg/apis/core/v1alpha1/types_federatedcluster.go`

```
FederatedCluster
├── spec
│   ├── apiEndpoint
│   ├── secretRef
│   └── taints[]
├── status
│   ├── conditions[]             # Ready, Joined, Offline
│   ├── joinPerformed
│   ├── apiResourceTypes[]       # 支持的 API 资源
│   └── resources
│       ├── schedulableNodes     # 可调度节点数
│       ├── allocatable          # 总可分配资源 (ResourceList)
│       └── available            # 当前可用资源 (ResourceList)
```

### 7.3 对比

| 维度 | Karmada | KubeAdmiral |
|------|---------|-------------|
| **资源粒度** | Allocatable + Allocating + Allocated (三维度) | Allocatable + Available (两维度) |
| **资源建模** | ResourceModels (自定义分级) + AllocatableModelings | 无 |
| **GPU 上报** | ResourceList 支持 nvidia.com/gpu | ResourceList 支持 nvidia.com/gpu |
| **拓扑信息** | 无 (仅 Region/Zone) | 无 |
| **节点统计** | NodeSummary (total/ready) | schedulableNodes |
| **API 能力** | apiEnablements (用于校验资源是否可创建) | apiResourceTypes |
| **集群标签** | metadata.labels (用于集群选择) | metadata.labels |

---

## 8. Queue 与集群粒度上报

### 8.1 Queue 的定位

Queue（队列）是**集群内调度器**（如 Volcano、Godel、Kueue）的概念，用于:
- 多租户资源隔离和公平调度
- 作业排队和优先级管理
- 资源预算 (Resource Budget) 控制

Karmada 和 KubeAdmiral 都**不直接管理 Queue**。Queue 属于集群内部调度层面的概念。

### 8.2 如何在联邦层获取 Queue 信息

**方案 A: 通过集群状态间接反映**
- 集群的 Allocatable/Available 反映了总体资源状况
- 但无法区分不同 Queue 的剩余容量

**方案 B: 扩展 Cluster Status (Karmada)**
- 可以在 Karmada Cluster Status 中增加 QueueSummary
- 通过自定义 ClusterStatusCollector 采集 Queue 状态

```
# 设想的扩展
ClusterStatus
└── queueSummary[]
    ├── queueName: "gpu-serving"
    ├── capacity: {nvidia.com/gpu: 100}
    ├── allocated: {nvidia.com/gpu: 80}
    ├── pending: 5  # 排队作业数
    └── fairShareWeight: 0.6
```

**方案 C: 联邦层 Queue 抽象**
- 定义 FederatedQueue CRD
- 映射到各集群的 Volcano Queue / Kueue ClusterQueue
- 聚合各集群 Queue 状态

### 8.3 AIBrix 中的 Queue

AIBrix 当前通过 RoleSet 的 SchedulingStrategy 关联集群内 Queue:
- Volcano: `volcanoSchedulingStrategy.queue` 字段
- Godel: 通过 PodGroup Affinity
- Coscheduling: 通过 minResources

在联邦场景下，需要将 Queue 名称通过 Override 策略按集群映射。

---

## 9. Rebalancer / Descheduler

### 9.1 Karmada Descheduler

代码路径: `karmada/pkg/descheduler/descheduler.go`, `karmada/pkg/descheduler/core/filter.go`

Karmada 内置 Descheduler 组件，用于动态重平衡跨集群的副本分配。

**触发条件**:
- 仅对 `Divided` + `DynamicWeight` 模式的 ResourceBinding 生效
- 当前仅支持 Deployment 类型工作负载
- 定期扫描

**工作流程**:
```
1. FilterBindings: 筛选 Divided+DynamicWeight 的 ResourceBinding
   ↓
2. 查询 SchedulerEstimator: 获取各集群可调度副本上限
   ↓
3. 检查不均衡: 对比当前分配 vs 理想分配 (基于 AvailableReplicas)
   ↓
4. 减少过载集群副本: 修改 ResourceBinding
   ↓
5. Scheduler 接管: 将释放的副本重新调度到有资源的集群
```

**SchedulerEstimator** (关键组件):
- 每个成员集群部署一个 Estimator
- 通过 gRPC 提供精确的可调度副本评估
- 考虑节点资源、亲和性、反亲和性等约束
- 代码路径: `karmada/pkg/estimator/`

### 9.2 KubeAdmiral AutoMigration + ReschedulePolicy

**AutoMigration** (PropagationPolicy 字段):
```
autoMigration:
  when: Unschedulable                    # 触发条件
  triggerDuration: 300s                  # 等待时间
  keepUnschedulableReplicas: false       # 是否保留不可调度副本
```

当集群中的副本变为 Unschedulable 时:
1. 等待 triggerDuration
2. 如果仍不可调度，触发迁移
3. Scheduler 重新调度到其他集群
4. 根据 keepUnschedulableReplicas 决定是否清理

**ReschedulePolicy**:
```
reschedulePolicy:
  disableRescheduling: false
  triggers:
    - policyContentChanged: true         # 策略内容变化
    - clusterJoined: true                # 新集群加入
    - clusterLabelsChanged: true         # 集群标签变化
    - clusterAPIResourcesChanged: true   # 集群 API 变化
  replicaRescheduling:
    type: DynamicWeight                  # 动态权重重调度
```

### 9.3 对比

| 维度 | Karmada | KubeAdmiral |
|------|---------|-------------|
| **组件** | 独立 Descheduler 进程 | PropagationPolicy 内置字段 |
| **触发方式** | 定期扫描 | 事件驱动 (Unschedulable / Policy变化等) |
| **支持类型** | 仅 Deployment | 通用 (取决于 FederatedObject) |
| **精确评估** | SchedulerEstimator gRPC | 无 (基于集群状态) |
| **重平衡策略** | 动态权重重计算 | AutoMigration + ReschedulePolicy |
| **可配置性** | 较少 (全局配置) | 丰富 (每个 Policy 独立配置) |
| **等待时间** | 无 (立即重平衡) | triggerDuration 可配置 |

---

## 10. Scheduler 架构

### 10.1 Karmada Scheduler

代码路径: `karmada/pkg/scheduler/`

```
Scheduler 流程:
1. Filter 阶段 (筛选可行集群)
   ├── APIEnablementFilter    # API 支持检查
   ├── ClusterAffinityFilter  # 集群亲和匹配
   ├── SpreadConstraintFilter # 分散约束
   ├── TaintTolerationFilter  # Taint/Toleration
   └── ClusterEvictionFilter  # 排除被驱逐集群

2. Score 阶段 (集群打分)
   ├── ClusterLocality        # 集群本地性偏好
   └── (可扩展)

3. Select 阶段 (选择最终集群)
   ├── SpreadConstraint       # 分散约束选择
   └── 副本分配算法

4. Assign 阶段 (分配副本)
   └── 根据 ReplicaSchedulingStrategy 分配副本到选中集群
```

**SchedulerEstimator 集成**:
- Scheduler 通过 gRPC 调用各集群的 Estimator
- 获取精确的可调度副本数 (考虑 nodeSelector, affinity, resource 等)
- 代码路径: `karmada/pkg/scheduler/core/estimation.go`

### 10.2 KubeAdmiral Scheduler

```
Scheduler 流程:
1. Filter 阶段 (筛选可行集群)
   └── 内置 + Webhook 扩展插件

2. Score 阶段 (集群打分)
   └── 内置 + Webhook 扩展插件

3. Select 阶段 (选择最终集群)
   └── 基于分数选择

SchedulingProfile CRD:
├── plugins
│   ├── filter
│   │   ├── enabled[]
│   │   └── disabled[]
│   ├── score
│   │   ├── enabled[] (含 weight)
│   │   └── disabled[]
│   └── select
│       ├── enabled[]
│       └── disabled[]
└── pluginConfig[]              # 插件参数
```

**Webhook 扩展**: KubeAdmiral 支持通过 `SchedulerPluginWebhookConfiguration` 注册外部 Webhook 插件，这比 Karmada 的纯内置插件模式更灵活。

---

## 11. Failover 机制

### 11.1 Karmada Failover

代码路径: `karmada/pkg/apis/policy/v1alpha1/propagation_types.go:328`

```
failover:
  application:                          # 应用级故障转移
    decisionConditions:
      tolerationSeconds: 300            # 容忍时间
    purgeMode: Gracefully               # 清理模式
    gracePeriodSeconds: 600             # 优雅期
    statePreservation:                  # 状态保持 (alpha)
      rules:
        - aliasLuaScript: |             # Lua 脚本提取状态
            function GetDependencies(desiredObj)
              ...
            end
  cluster:                              # 集群级故障转移
    purgeMode: Gracefully
    gracePeriodSeconds: 600
```

**PurgeMode** 选项:
- `Directly`: 立即清理旧集群资源（适用于不能双实例的场景，如 Flink）
- `Gracefully`: 等待新集群健康后才清理旧集群
- `Never`: 不清理，用户手动处理

**StatePreservation** (alpha): 通过 Lua 脚本在迁移时提取和注入状态数据，对有状态应用（如 KV Cache）非常有用。

### 11.2 KubeAdmiral

KubeAdmiral 的故障转移通过 `autoMigration` 实现（见 9.2 节），不如 Karmada 的 Failover 丰富。

---

## 12. 对比总结

### 12.1 综合对比表

| 能力 | Karmada | KubeAdmiral | 对 GPU 联邦的意义 |
|------|---------|-------------|-------------------|
| **资源模型** | 原生 K8s 资源 | FederatedObject 封装 | Karmada 更适合 (不需要改造现有 CRD) |
| **副本调度** | Duplicated/Divided + Aggregated/Weighted + DynamicWeight | Duplicate/Divide + Binpack/Spread | Karmada 的 DynamicWeight 更适合 GPU 场景 |
| **联邦 HPA** | FederatedHPA (集中式) + CronFederatedHPA | HPAAggregator (分布式聚合) | Karmada 更完整 |
| **联邦 Quota** | FederatedResourceQuota | 无 | **Karmada 必选**: GPU 需要跨集群 quota |
| **资源上报** | 三维度 + ResourceModels | 两维度 | Karmada 更精细 |
| **Rebalancer** | 独立 Descheduler + SchedulerEstimator | AutoMigration + ReschedulePolicy | 各有优势 |
| **Failover** | 丰富 (PurgeMode + StatePreservation) | 基础 (AutoMigration) | Karmada 更适合 GPU (状态保持) |
| **调度扩展** | 内置插件 | Webhook 扩展插件 | KubeAdmiral 更灵活 |
| **依赖传播** | PropagateDeps | FollowerScheduling | 都支持 |
| **工作负载亲和** | WorkloadAffinity | 无 | Karmada 有优势 (P/D 亲和) |

### 12.2 推荐

对于 GPU 服务联邦场景，**推荐基于 Karmada 构建**，原因:
1. **FederatedResourceQuota**: GPU 是稀缺资源，必须有跨集群 Quota 管理
2. **资源上报**: 三维度资源报告 + ResourceModels 可扩展支持 GPU 拓扑
3. **DynamicWeight**: 根据各集群实际 GPU 可用量动态分配
4. **SchedulerEstimator**: 精确评估集群 GPU 调度能力
5. **Failover StatePreservation**: 对 GPU 服务的 KV Cache 迁移有价值
6. **WorkloadAffinity**: 可用于实现 P/D 集群亲和
7. AIBrix 已经 vendor 了 Karmada 代码 (`/home/user/aibrix/karmada/`)

需要扩展的地方:
- Cluster Status 增加 GPU 拓扑信息
- Scheduler 增加 GPU 拓扑感知插件
- FederatedResourceQuota 增加 GPU 型号级粒度
- Descheduler 支持 GPU 工作负载类型 (目前仅 Deployment)
