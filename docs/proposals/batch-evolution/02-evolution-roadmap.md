# ② 长期演进路线 + 工程 Roadmap

> **本文件以 [`05-decisions.md`](./05-decisions.md) 为准**。按 **feature 优先级 P0–P5** 组织。
> **重要修正(本轮)**:online↔offline 共置(隔离/收割,家族 B)**降级为最低优先、可选、可能不做**——
> 生产上混步难度大且不安全;排在**异构/offload(C)之后**。相应地,**D(跨云 spot)与 A(offline 感知)上提**为近期主攻。
> 主轴 = **Sourcing(便宜算力:neocloud → spot/跨区 → 异构/offload)× Throughput(整批重排)**;共置不再是主线。

## 现状基线 (今天的代码)

```
用户 → /v1/files + /v1/batches (OpenAI 兼容, console/api + planner)
     → JobScheduler (FIFO, scheduler.py:39/186) + 固定 pool (BasicCongestionControl, :57)
     → 每个 job 起一个 K8s Job: [batch-worker] + [原生 vLLM]; worker 逐条 httpx.post (inference_client.py:60; scheduler.py:256)
     → 结果写回 S3/TOS/GCS
```
**特征**:控制面完整,**数据面=0 差异化**(原生 vLLM + 逐条回放),**资源面**正在重构(见 P0)。

**重构已在动工**:`refactor[planner] split provider-agnostic core (#2239)`、`feat(RM): add extension support (#2245)`、
`batch: upstreamable storage/drivers/resource schema/model discovery (#2243)`。P0 是把它收口成一个完整的层。

---

## P0 — Foundation:架构重构 + neocloud provisioning 【v0.7.0 主线 · Sourcing】

**目标**:把 batch 收口成清晰三层,并让 **resource manager 多 provider 化**——能在 **Lambda / RunPod** 等 neocloud 上 launch。
低风险、立竿见影的成本故事(neocloud GPU 常比超大厂 on-demand 便宜 50–80%),也是 D/C 的底座。

**三层 + 三个可插 seam**:
- **MDS**:OpenAI 兼容 API + job 生命周期(保持兼容)。
- **planner**:放置与调度。`planner/api/interface.go` 的 `Planner{Enqueue/GetJob/ListJobs/Cancel/Recover/Close}` 已是干净边界;`planner/impl/backend.go` 有 `backendRegistry`+`RegisterBackend`,`defaultPlannerBackend` 注释已写 *"serves kubernetes / aws / lambdaCloud"*,`Schedule()` 是 capacity-aware 调度钩子。
- **resource manager**:发现/定价/provisioning。`catalog/interface.go` 的 `Catalog = Provider + ResourceCatalog + PricingCatalog`(已含 `ListRegions/ListInstanceTypes/ListPricing/ListPricingPredictions/ListResourcePredictions`);`catalog/factory.go` 非 k8s 走 `NewCatalogExtension`;`resource_manager/provisioner` 负责实际 launch。

**要做(v0.7.0)**:① 原生 neocloud backend(Lambda/RunPod):各加 `ResourceProvisionType` + catalog 扩展 + provisioner + planner backend 注册,**不依赖 SkyPilot**(决策 D-D)。② 定价/容量感知 planner(基础版),为 P1 spot 铺路。③ 硬化:调度策略抽象(`scheduler.py:39/190`)、deadline 感知(`:219`)、cancel 全链路(`batch_job.py:47`)、重试幂等、**成本计量**、可观测。

**成功指标**:从 OpenAI 兼容 API 在 Lambda 与 RunPod 上成功 launch batch;每 job 可信成本账单;`$/1M tok(neocloud)` 显著 < 超大厂 on-demand。
**风险**:中(异构 neocloud API + 重构不回归兼容性)。

---

## P1 — 成本感知 spot / 跨 provider 放置 (家族 D) 【近期主攻 · 替代 SkyPilot · 你的重点之一】

**目标**:在 P0 的多 provider + 定价感知之上,加 **spot + 跨区/跨 provider 成本套利**,用**推理原生**做出 SkyPilot/SkyNomad 做不到的差异化。**这是你最感兴趣的方向之一,也是 research #1 的落点。**

**数据面 × 控制面**:
- 控制面:`planner.Schedule()` 接 SkyNomad 式**统一成本模型**(价格+可用性+egress+deadline)+ 生存分析预测(catalog 的 `ListPricingPredictions/ListResourcePredictions` 是天然入口)+ 探活 CronJob;迁移单位 = 推理 job group / 解耦 P-D 单元。
- 数据面:**推理原生 checkpoint(护城河)**——按"请求队列(KB)+ 在飞 prompt 的 prefix-KV(≤GB)"checkpoint 到 object store(复用 storage 抽象),**秒级**恢复、不重算 prefill。**关键洞察:最便宜的 spot = 最易被抢占的 spot,只有这种细粒度 checkpoint 才敢吃它**(见 `08` 算账)。
- 预烤镜像 + 权重持久卷,冷启动从 SkyNomad 整机 ~6min 压到秒~分钟。

**成功指标**:同一 deadline-bound 大 job,**$ < SkyPilot managed-spot**(W6 真实 spot trace),**单次迁移损失低一个量级**;deadline 100% 满足。$/token 在 on-demand 基础上再降 2–8×(见 `08`)。
**风险**:中(先单云多区,真·跨云审慎)。**适用边界**:大、deadline 松、KV 不巨的活;小活/紧 deadline/超长上下文/数据驻留锁定**不适用**。

---

## P2 — batch 感知数据面吞吐 (家族 A) 【近期主攻 · 低风险 · 你的重点之一】

**目标**:同硬件更高吞吐 → 降单价。纯软件、不碰在线 SLO,**任意时点可做**;且与 P1 协同(prefix 共享缩小 KV 足迹 → D 的 checkpoint 更小、迁移更快)。

**数据面 × 控制面**:控制面 `batch/optimizer/`——整批 prompt 建 radix prefix 树(BatchLLM)+ 计算密度排序/双扫描器混排(BlendServe)→ 有序+分组请求流;数据面按组提交 + prefix hint + 显存预算成批(`M_threshold` 取代请求计数);喂已有分布式 prefix cache / KVCache V1 connector。
**落点**:`batch/optimizer/{prefix_tree,reorder}.py`;`job_driver/` 批级提交;`scheduler.py:256` 的"one request"执行单元扩成"一组"。
**成功指标**:共享 prefix / prefill 主导负载(评测/分类/抽取/合成数据)tokens/s/GPU **↑1.3–5×**(随前缀长度×共享度),结果等价性通过。
**风险**:中低(hint 与引擎 prefix cache 语义一致;输出长度异质兜底)。**适用边界**:有大固定前缀的批显著;纯异构无共享的批平庸。

---

## P3 — 异构 / offload 成本档位 (家族 C)

**目标**:batch 跑在更便宜硬件(商用 PCIe GPU / CPU-rich 节点),压硬件单价。
**结合**:复用 KV offloading framework + KVCache V1 connector + P/D 解耦(StormService);CPU/attention 下沉(NEO/Glinthawk)、商用节点 PP + block-first KV 布局(PipeMax);成本感知 planner(Glinthawk 仿真器思路,与 P1 共用 catalog 定价)。
**落点**:`ModelDeploymentTemplate` 的 accelerator/parallelism schema 扩异构池 + offload 开关;`batch/planner/`。
**成功指标**:某长上下文负载 $/tok vs 纯 H100 专属池 **↓≥2×**(Glinthawk 2.4–2.8× / PipeMax 2.45× 量级)。
**风险**:中高(量化侵蚀收益;PCIe 上限;CPU kernel 维护)。**先 PipeMax 商用节点池(纯调度+布局,最易),再 attention 下沉。**

---

## P4 — Agentic / workflow batch (家族 E) 【新产品面 · later】

**目标**:`POST /v1/workflow-batch`(`workflow_spec` DAG + `queries[]`),把"一批 workflow"当一个查询优化:DAG planner(DP 放置)+ KV 血缘亲和路由(在 prefix cache 上加血缘亲和)+ tool 调用去重中间件。
**成功指标**:多步 RAG/agent 批,vs 客户端逐调用 + 已开 prefix cache 降 GPU-秒 ~2×。
**风险**:中(静态 DAG 限制;需从 LangGraph 自动抽取或限定模板)。

---

## P5(最低优先 / 可选 / **可能不做**)— online↔offline 隔离与收割 (家族 B)

> **降级说明(本轮决策 D-E)**:**生产上在线/离线混步难度大、不安全**;价值不确定。**排在 C 之后,作为可选探索,最终完全可能不走这条路径。** roadmap 其余部分**不依赖**它。

- 若**真要**探索:**单模型**起步(决策 D-B),且**必须先有一个有界、可演示的"在线不被影响"保障**(Valve channel 抢占 + cooldown 限率 + MIAD;ConServe 层级抢占)才谈得上收割;多模型隔离是公开难题(见 `07` 的 research 角度 #3,**已随本路径一并降级为"可选 research"**)。
- 若**不走**:把"便宜的共享算力"这条腿砍掉,完全靠 **Sourcing(P1 spot/跨区 + P3 异构/offload + neocloud)× Throughput(P2)** 来打成本——这条线本身已足够强,且生产安全。

---

## ★ 北极星 — 统一「Batch 成本优化器」 + 成本 SLO

把 P1–P3 收敛成对用户的一句话抽象:**"在 deadline T 之前、(可选)预算 B 内,以最低成本完成这批 job"**,系统在
*(跨区/跨 provider spot ⊕ 异构/offload 档位) × 吞吐优化* 的空间里求解,并兑现一个**成本 SLO**。
**(注:不再以"在线机队收割"为前提——收割是可选项,见 P5。)**

```
给定: 一批请求/工作流 + 完成窗口 + (可选)预算上限
优化: min Σ(实例$ + egress$ + 存储$)   约束: 完成 ≤ deadline; 结果等价
杠杆: P2 吞吐↑ · P1 跨区/neocloud spot↓最低价 · P3 异构↓硬件单价 · (可选 P5 收割↓边际成本)
```
这是 OpenAI batch(黑盒 5 折)和 SkyPilot(工作负载无关、推理不可知)都给不了的——**AIBrix 是云原生 batch 的入口**。

---

## 版本映射

```
v0.7.0  P0 Foundation(重构 + neocloud provisioning Lambda/RunPod + 定价感知 planner 基础 + 硬化)
v0.8    P1 spot/跨区/跨 provider(家族 D,替代 SkyPilot) ; P2 吞吐(家族 A,可任意时点并行)
v0.9    P3 异构/offload(先 PipeMax 池) ; P4 workflow(家族 E)
v1.0    ★ 北极星(统一成本优化器:D ⊕ C × A + 成本 SLO)
(可选/可能不做)  P5 online↔offline 隔离与收割(家族 B)
```

依赖:`P0 ─┬─► P1(spot,D) ──┐`
` ├─► P2(吞吐,A,任意时点) ──┼─► ★ 北极星`
` ├─► P3(异构,C) ─────────┘`
` ├─► P4(workflow,E,独立产品面)`
` └┄┄► P5(共置,B)  可选/可能不做,不被任何里程碑依赖`

> 注:本文档为**纯规划**。不含功能代码;"怎么实现(fork/upstream 等)"按决策 D-C 视为后续细节。
