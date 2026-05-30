# ② 长期演进路线 + 工程 Roadmap

> 把家族 A–E 落成可执行的里程碑。每个里程碑给:**目标 / 数据面×控制面的结合 / 落点(真实文件/符号)/
> 成功指标 / 粗略工作量 / 风险**。原则:**优先复用 AIBrix 服务面已有原语**,而不是重造。

## 现状基线 (M-1,今天的代码)

```
用户 → /v1/files + /v1/batches (OpenAI 兼容, console/api + planner)
     → BatchJobSpec{input_file_id, endpoint, completion_window=24h, aibrix{template,profile,overrides}}
     → JobScheduler (FIFO, scheduler.py:39/186) + BasicCongestionControl(固定 pool, :57)
     → 每个 job 起一个 K8s Job: [batch-worker sidecar] + [原生 vLLM]
     → worker 逐条 httpx.post 到 localhost:8000 (inference_client.py:60; scheduler.py:256 "one request")
     → 结果写回 S3/TOS/GCS (storage 抽象)
```
**特征**:控制面完整(OpenAI 兼容、K8s、存储抽象、模板/profile、RBAC、测试齐全),
**数据面=0 差异化**(原生 vLLM + 逐条回放),**资源面=0**(无 colocation/抢占/弹性/spot/异构)。

设计判断:**不要推翻这套控制面**,它是资产。演进 = 在它上面长出"batch 感知数据面"和"弹性 sourcing 层"。

---

## M0 — 控制面硬化 (table stakes,v0.7.0 必做)

**目标**:把"功能正确的 demo"变成"能扛生产 + 能承载后续高级特性的地基"。这不是差异化,但缺了它后面都立不住。

| 项 | 现状 | 要做 | 落点 |
|---|---|---|---|
| 调度策略抽象 | `SchedulePolicy.FIFO` 唯一,已留 TODO | 抽成 `SchedulingPolicy` 接口(FIFO / 最短作业优先 / **deadline 感知**) | `scheduler.py:39,190` |
| deadline 感知 | 只有 `expire_jobs` 到期作废 | completion-window 作为**调度目标**(EDF),而非仅过期 | `scheduler.py:219`, `batch_job.py:37` |
| 取消 | `CANCELLING` 态存在但 cancel "future" | 落地 `POST /v1/batches/{id}/cancel` 全链路 | `batch_job.py:47`, console `job.go` |
| 重试/幂等 | round-robin 重试(`job_manager`) | 按 `custom_id` 幂等 + 死信 + `fail_after_n_requests` 语义化 | `job_manager.py`, `BatchJobSpec.opts` |
| 成本计量 | 无 | 每 job 记录 GPU/CPU-秒、token 数、$、节点类型 → **成本归因**(后续所有里程碑的度量基础) | 新增 `batch/accounting/` |
| 多模型池 | 每 job 起专属 vLLM | 引入**共享模型池**概念(为 M1/M2 共享引擎铺路) | `manifest/`, `job_driver/` |
| 可观测 | 基础日志 | Prometheus 指标(队列深度、goodput、JCT、$/1M tok)、Grafana 板 | runtime |

**成功指标**:1000-job 混合负载下无 job 卡死/错标;cancel/retry 正确;每 job 有可信成本账单;队列深度/JCT/$ 可观测。
**工作量**:~1 个 sprint(主要是工程化,无研究风险)。 **风险**:低。

---

## M1 — Batch 感知数据面 (家族 A;v0.7.0 第一刀)

**目标**:把"逐条回放给 vanilla vLLM"换成"**批级优化 + 引擎 hint**",在**同样硬件**上提吞吐 → 降单价。
这是 batch 第一次有"自己的数据面智能"。

**数据面 × 控制面结合**:
- **控制面 `batch/optimizer/`**(新):job 提交后对整批 prompt 建 radix prefix 树(BatchLLM)+ 计算密度排序/双扫描器重排(BlendServe)→ 产出**有序+分组**的请求流 + prefix manifest。纯 stateless、按 job 并行、`O(N log N)`。
- **数据面**:执行不再逐条乱序 post;按**组**提交,把 prefix 分组作为 hint;成批判据从"请求计数"换成**显存预算 `M_threshold`**。
- **复用已有原语**:把分组喂给 **分布式 prefix cache / KVCache V1 connector**,prefix 复用直接兑现;`M_threshold` 按节点可用 VRAM 设(为异构铺路)。

**落点**:`batch/optimizer/{prefix_tree.py,reorder.py}`;`job_driver/` 增加"批级提交"路径;`scheduler.py:256` 的"one request"执行单元扩成"一组";`engine_adapter` 传 hint。
**成功指标**:共享 prefix 负载 **tokens/s/GPU ↑ 1.3–2×**、KV 复用率显著上升;$/1M tok 相应下降;optimizer 开销 <1% JCT。
**工作量**:~1–1.5 sprint(prefix 树/重排是已知算法)。 **风险**:中(要确保 hint 与引擎 prefix cache 语义一致;输出长度异质要兜底)。

---

## M2 — 弹性 colocation / 在线机队收割 (家族 B;v0.7.0 MVP,后续加固)

**目标**:让 batch 请求成为**在线机队空隙的填充料**,把"已经付过钱的"GPU 波谷变成近乎免费的 batch 算力。
**这是把价格打到 5 折以下的最大单一杠杆,也是 v0.7.0 的核心卖点。**

**分阶段(成熟度递进,对应 HyGen → Valve)**:
- **M2.a (v0.7.0 MVP,feature-flag)** — *HyGen 式延迟预算准入,迭代级*。
  - 数据面:在 vLLM `_schedule()` 后加"填充槽"——用 ~18µs 线性回归延迟预测器(`predict()` / `get_max_prefill()`)在在线请求装完后,用剩余延迟预算装 batch token。
  - 控制面:**SLO-aware profiler** 离线二分标出安全预算 `t*`,按 (model, 硬件, SLO 档) 存进模板/profile 注册表;**干扰容忍比 = 价格旋钮**,落 `BatchJobSpec.aibrix`。
  - 路由:复用 **mixed-workload routing (v0.6) + `FederalInferenceEngineClient`**,把 batch 请求路由进低负载在线 pod。
  - 约束:先**单模型池**、迭代级抢占(改动小,~HyGen 的 1300 LOC 量级)。
- **M2.b (后续) — Valve 式生产硬保障**:CUDA **channel 亚毫秒抢占** + `T_cool=2G` 限率 + **MIAD** 显存回收 + 三因子放置模型(Jaccard≥0.95)。把 1 行驱动补丁列为 **bare-metal 节点的可选前置**(托管云退化为 channel-only,延迟放宽)。

**落点**:节点级 **colocation daemon (DaemonSet)** 订阅在线 SLO 信号管准入/抢占;`scheduler.py:97` 的 `update_job_pool_size` TODO 接**队列深度 + 在线负载**驱动的弹性;新增 `BatchJobState.PAUSED/PREEMPTED`(`batch_job.py:47`);放置控制器用 Valve 三因子。
**成功指标**:在 Azure/Mooncake 在线 trace 回放下,**在线 TTFT/TPOT 退化 < 设定档(如 5%)** 且 batch goodput 显著>0;*有效价格随收割比例下降*的曲线成立(见 `03-experiments.md`)。
**工作量**:M2.a ~1.5–2 sprint;M2.b 一个独立大项(含 daemon + 驱动 + 放置器)。 **风险**:高(在线 SLO 是付费客户的承诺;预测器/抢占要稳;多模型是开放问题)。**这是最该投、也最该谨慎的里程碑。**

---

## M3 — 异构 / offload 成本档位 (家族 C)

**目标**:让 batch 能跑在**更便宜的硬件**上(商用 PCIe GPU / CPU-rich 节点),进一步压硬件单价。

**数据面 × 控制面**:
- **复用 KV offloading framework + KVCache V1 connector (v0.3/v0.4)** 做 KV 下沉;**复用 P/D 解耦 / StormService** 承载 Glinthawk 两层与 PipeMax 的 PP。
- 新增 **CPU/attention 下沉执行**(NEO 思路的 attention backend 插件,或 Glinthawk 两层 dispatcher);**商用节点 PP + block-first KV 布局**(PipeMax)。
- 控制面 **成本感知 planner**(Glinthawk 仿真器思路):给定异构机队,解"达到目标 $/token 的最优 (节点类型, 配比, 批大小)"。

**落点**:`ModelDeploymentTemplate` 的 accelerator/parallelism schema(`samples/batch/`,已支持 tp/pp/dp/ep + SKU)扩出**异构池 + offload 开关**;新增 `batch/planner/`。
**成功指标**:70B 长上下文 batch 在 T4/RTX/CPU 档上达到目标 $/token,**比纯 H100 专属池单价↓**(对标 Glinthawk 2.4–2.8×、PipeMax 2.45× 的量级)。
**工作量**:大(每条 offload 路径都要 profile + kernel/调度)。 **风险**:中高(量化会侵蚀收益;PCIe 上限;CPU kernel 维护)。建议**先 PipeMax 商用节点池(最易,纯调度+布局),再 Glinthawk/NEO 的 attention 下沉**。

---

## M4 — 跨区/跨云 spot (家族 D) 【对标 SkyPilot 的里程碑】

**目标**:把弹性从"机队内"扩到"**跨区/跨云 spot 套利**",并用**推理原生**做出 SkyPilot 做不到的差异化。

**数据面 × 控制面**:
- 控制面 **spot 放置控制器**(K8s controller):跑 SkyNomad 统一成本模型(价格+可用性+egress+deadline)+ 生存分析预测 + 探活 CronJob;迁移单位是**推理 job group / 解耦的 P-D 单元**。
- 数据面 **推理原生 checkpoint(护城河)**:按"请求队列(KB)+ 在飞 prompt 的 prefix-KV(≤GB)"checkpoint 到 object store(复用 storage 抽象),**秒级**恢复、不重算 prefill(把 SpotServe 的单区 token 级 KV save/restore 泛化到跨区 batch)。
- **两级融合**:本区先收割在线机队(M2),收割耗尽 + 本区 spot 也贵 → 才触发跨区。

**落点**:新增 `batch/sky/`(成本模型 + 探活 + 放置);新增 `BatchJobState.MIGRATING`;checkpoint 走 `storage/`;预烤镜像 + 权重持久卷(把冷启动从 6min 压到秒~分钟)。
**成功指标**:同一 deadline-bound 大 job,**AIBrix 跨区方案 $ < SkyPilot managed-spot**(目标:逼近 SkyNomad 的 1.25–3.96×,且因推理原生 checkpoint 而**冷启动/迁移损失更低**);所有 deadline 满足。
**工作量**:大。 **风险**:中(单云多区先行;真·跨云的 API/egress 复杂度审慎)。**这是 Sky Computing 叙事的兑现点。**

---

## M5 — Agentic / workflow batch (家族 E) 【新产品面】

**目标**:提供闭源 batch API 没有的 `workflow-batch` 产品,把"一批 workflow"当一个查询优化。

**落点 / 结合**:新 API `POST /v1/workflow-batch`(`workflow_spec` DAG + `queries[]`);控制面 **DAG planner**(DP 放置);数据面 **KV 血缘亲和路由**(在分布式 prefix cache 上加血缘亲和)+ **tool 调用去重中间件**。
**成功指标**:多步 RAG/agent 批,**vs 客户端逐调用 + 已开 prefix cache 的基线降 GPU-秒 ~2×**;端到端比 naive 数倍。
**工作量**:大(新 IR/API/planner)。 **风险**:中(静态 DAG 限制;需从 LangGraph 自动抽取或限定模板)。**可作为 v0.8+ 的旗舰差异化。**

---

## ★ 北极星 — 统一「Batch 成本优化器」 + 价格/SLO 档位

把 A–E 收敛成一个对用户暴露的抽象:**"在 deadline T 之前,以最低成本完成这批 job"**,系统自动在
*(机队内收割 ⊕ 跨区 spot) × (吞吐优化 + 硬件档位)* 的联合空间里求解,并兑现一个**成本 SLO**:

```
给定: 一批请求/工作流 + 完成窗口 + (可选)成本上限/SLO 档位
优化: min  Σ(实例$ + egress$ + 存储$)
约束: 完成时间 ≤ deadline; 在线 SLO 退化 ≤ 档位; 结果与在线等价
杠杆: M1 重排↑吞吐 · M2 收割↓边际成本 · M3 异构↓硬件单价 · M4 跨区 spot↓最低价 · M5 workflow 去重↓总 token
```

对用户,这就是一句话价值:**"把这批活在 24h 内跑完,价格我帮你压到市场最低,而且全程你可控可见。"**
这是 OpenAI batch(黑盒 5 折)和 SkyPilot(工作负载无关、推理不可知)都给不了的。

---

## 依赖图(里程碑顺序)

```
M0 (地基) ─┬─► M1 (吞吐, A) ──────────────┐
           ├─► M2 (收割, B) ──┐            ├─► ★ 北极星
           │                  ├─► M4 (spot, D, 对标 SkyPilot)
           └─► (M0 成本计量) ─┘            │
                              M3 (异构, C) ─┘
                              M5 (workflow, E) ── 独立产品面,可并行
```

- **v0.7.0**: M0 全量 + M1 第一刀 + M2.a MVP(flag)。
- **v0.8–0.9**: M2.b 加固 + M3(先 PipeMax 池)+ M4 单云多区。
- **v1.0 叙事**: 北极星 + M5。

> 关于"去实现":建议从 **M0 的成本计量 + M1 的 prefix 重排**起手——风险最低、立刻能产出可对外的 $/token 数字,
> 给 M2 的收割实验提供度量基线。具体第一个 PR 的拆解见与本文档配套的对话结尾。
