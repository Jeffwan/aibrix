# ② 长期演进路线 + 工程 Roadmap

> **本文件以 [`05-decisions.md`](./05-decisions.md) 为准**(A/B/C/D 已拍板)。组织原则已从"M0→M5"改为
> **按 feature 优先级 P0–P6**(因为重点是"放什么 feature"),每个 P 给:目标 / 数据面×控制面 / 落点(真实文件)/
> 成功指标 / 风险。两根支柱:**Sourcing(便宜专属算力)** 与 **Isolation(安全共享算力)**。

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

## P0 — Foundation:架构重构 + neocloud provisioning 【v0.7.0 主线】

**目标**:把 batch 收口成清晰三层,并让 **resource manager 多 provider 化**——能在 **Lambda / RunPod** 等 neocloud 上 launch。
这是 **Sourcing 支柱的地基**,也是低风险、立竿见影的成本故事(neocloud GPU 常比超大厂 on-demand 便宜 50–80%)。

**三层 + 三个可插 seam**:
- **Metadata Service (MDS)**:OpenAI 兼容 API + job 生命周期(保持兼容)。
- **planner**:放置与调度。`planner/api/interface.go` 的 `Planner{Enqueue/GetJob/ListJobs/Cancel/Recover/Close}` 已是干净边界;`planner/impl/backend.go` 有 `backendRegistry`+`RegisterBackend`,`defaultPlannerBackend` 注释已写 *"serves kubernetes / aws / lambdaCloud"*,`Schedule()` 是 capacity-aware 调度钩子(TODO 加 `Plan()` 拉 catalog 容量)。
- **resource manager**:发现/定价/provisioning。`catalog/interface.go` 的 `Catalog = Provider + ResourceCatalog + PricingCatalog`(已含 `ListRegions/ListInstanceTypes/ListPricing/ListPricingPredictions/ListResourcePredictions`);`catalog/factory.go` 非 k8s 走 `NewCatalogExtension`;`resource_manager/provisioner` 负责实际 launch。

**要做(v0.7.0)**:
1. **原生 neocloud backend(Lambda / RunPod)**:各加一个 `ResourceProvisionType` + catalog 扩展 + provisioner + planner backend 注册。**不依赖 SkyPilot**(决策 D-D)。
2. **定价/容量感知 planner(基础版)**:用 catalog 的 pricing/prediction 让 `Schedule()` 选"够用且最便宜"的实例(为 P2 spot 铺路)。
3. **硬化(随重构一起)**:调度策略抽象(`scheduler.py:39/190` 的 FIFO TODO)、deadline 感知(`scheduler.py:219`)、cancel 全链路(`batch_job.py:47` 的 `CANCELLING`)、重试幂等、**成本计量**(GPU/CPU-秒/token/$/节点类型 → 后续所有度量的基线)、可观测(队列深度/JCT/$/1M tok)。

**成功指标**:从 OpenAI 兼容 API 在 **Lambda 与 RunPod 上成功 launch batch**;每 job 有可信成本账单;`$/1M tok(neocloud)` 显著 < 超大厂 on-demand(且给出 vs 闭源 Batch 的对比点)。
**风险**:中——主要是异构 neocloud API(鉴权/实例生命周期/失败模式/定价准确性)+ 重构不能回归 OpenAI 兼容行为。缓解:先两家 + catalog/provisioner/planner-backend 强契约测试 + k8s 仍为默认路径。

---

## P1 — online↔offline 隔离保障 【关键研究赌注,与 v0.7.0 并行】

**目标**:做出"**在线不被影响**"的*保障*——一个机制 + **有界、可证/可演示**的在线延迟影响。这是通往"共享算力"(harvesting)的前提,
也是比"我们会收割"更可信的卖点。**先有保障,P4 harvesting 才配做。**(决策 D-B)

**技术内核**:Valve(channel 亚毫秒抢占 + `T_cool=2G` 限率 + MIAD 显存回收 + 三因子放置 Jaccard≥0.95)+ ConServe(层级 safepoint 抢占 + 增量单 token KV checkpoint)。
**交付物**:机制 + 一个**有界在线影响**的论证 + W5 在线-trace 实验证"在线 p99 不受影响"(见 `03-experiments.md`)。
**约束**:单模型(决策 D-B);多模型隔离列为已知难题单独立项。
**风险**:高(研究性)。**这是研究 workstream,不一定随某个版本发版;它的产出 gate 住 P4。**

---

## P2 — 成本感知 spot / 跨 provider 放置 (家族 D) 【替代 SkyPilot 的差异化点】

**目标**:在 P0 的多 provider + 定价感知之上,加 **spot + 跨区/跨 provider 成本套利**,并用**推理原生**做出 SkyPilot 做不到的差异化。(决策 D-D:替代,不依赖)

**数据面 × 控制面**:
- 控制面:`planner.Schedule()` 接 SkyNomad 式**统一成本模型**(价格+可用性+egress+deadline)+ 生存分析预测(catalog 的 `ListPricingPredictions/ListResourcePredictions` 是天然入口)+ 探活 CronJob;迁移单位是**推理 job group / 解耦 P-D 单元**。
- 数据面:**推理原生 checkpoint(护城河)**——按"请求队列(KB)+ 在飞 prompt 的 prefix-KV(≤GB)"checkpoint 到 object store(复用 storage 抽象),**秒级**恢复、不重算 prefill(把 SpotServe 单区 token 级 KV save/restore 泛化到跨区 batch)。
- 预烤镜像 + 权重持久卷,把冷启动从 SkyNomad 的整机 ~6min 压到秒~分钟。

**成功指标**:同一 deadline-bound 大 job,**AIBrix $ < SkyPilot managed-spot**(W6,真实 spot trace),且**单次迁移损失低一个量级**(推理原生 vs 整机);deadline 100% 满足。
**风险**:中(先单云多区,真·跨云审慎)。

---

## P3 — batch 感知数据面吞吐 (家族 A)

**目标**:同硬件更高吞吐 → 降单价。纯 $/token win,风险中低,**任意时点可做**。

**数据面 × 控制面**:控制面 `batch/optimizer/`——整批 prompt 建 radix prefix 树(BatchLLM)+ 计算密度排序/双扫描器重排(BlendServe)→ 有序+分组请求流;数据面按组提交 + prefix hint + 显存预算成批(`M_threshold` 取代请求计数,适配异构 VRAM);喂已有分布式 prefix cache / KVCache V1 connector。
**落点**:`batch/optimizer/{prefix_tree,reorder}.py`;`job_driver/` 批级提交;`scheduler.py:256` 的"one request"执行单元扩成"一组"。
**成功指标**:共享 prefix 负载 tokens/s/GPU **↑1.3–2×**,结果等价性通过。
**风险**:中(hint 与引擎 prefix cache 语义一致;输出长度异质兜底)。

---

## P4 — harvesting 做成 feature (家族 B) 【门槛 = P1 保障成立】

**目标**:把"在线机队波谷"变成近乎免费的 batch 算力。**单模型**(决策 D-B),**且必须等 P1 隔离保障扎实**。
**数据面 × 控制面**:HyGen 式延迟预算准入(vLLM `_schedule()` 加填充槽,~18µs 预测器)起步 → P1 的 Valve 式硬保障兜底;复用 mixed-workload routing(v0.6)+ `FederalInferenceEngineClient`(`inference_client.py:72`)把 batch 路由进低负载在线 pod;`scheduler.py:97` 的 `update_job_pool_size` TODO 接队列深度+在线负载驱动弹性;新增 `BatchJobState.PAUSED/PREEMPTED`(`batch_job.py:47`)。**干扰容忍比 = 价格档位**,落 `BatchJobSpec.aibrix`(`batch_job.py:155`)。
**成功指标**:W5 下在线 p99 退化守住档位 且 batch goodput>0;*有效价格 vs 收割比例* 曲线成立(`03` 图①)。
**风险**:高(碰付费在线 SLO;多模型是开放问题)。**最该谨慎,被 P1 gate。**

---

## P5 — 异构 / offload 成本档位 (家族 C)

**目标**:batch 跑在更便宜硬件(商用 PCIe GPU / CPU-rich 节点),压硬件单价。
**结合**:复用 KV offloading framework + KVCache V1 connector + P/D 解耦(StormService);CPU/attention 下沉(NEO/Glinthawk)、商用节点 PP + block-first KV 布局(PipeMax);成本感知 planner(Glinthawk 仿真器思路,与 P2 共用 catalog 定价)。
**落点**:`ModelDeploymentTemplate` 的 accelerator/parallelism schema 扩异构池 + offload 开关;`batch/planner/`。
**成功指标**:某长上下文负载 $/tok vs 纯 H100 专属池 **↓≥2×**(Glinthawk 2.4–2.8× / PipeMax 2.45× 量级)。
**风险**:中高(量化侵蚀收益;PCIe 上限;CPU kernel 维护)。**先 PipeMax 商用节点池(纯调度+布局,最易),再 attention 下沉。**

---

## P6 — Agentic / workflow batch (家族 E) 【新产品面】

**目标**:`POST /v1/workflow-batch`(`workflow_spec` DAG + `queries[]`),把"一批 workflow"当一个查询优化:DAG planner(DP 放置)+ KV 血缘亲和路由(在 prefix cache 上加血缘亲和)+ tool 调用去重中间件。
**成功指标**:多步 RAG/agent 批,vs 客户端逐调用 + 已开 prefix cache 降 GPU-秒 ~2×。
**风险**:中(静态 DAG 限制;需从 LangGraph 自动抽取或限定模板)。**later / 研究线。**

---

## ★ 北极星 — 统一「Batch 成本优化器」 + 价格/SLO 档位

把 P0–P6 收敛成对用户的一句话抽象:**"在 deadline T 之前,以最低成本完成这批 job"**,系统在
*(机队内收割 ⊕ 跨区/跨 provider spot) × (吞吐优化 + 硬件档位)* 的联合空间里求解,并兑现一个**成本 SLO**。

```
给定: 一批请求/工作流 + 完成窗口 + (可选)成本上限/SLO 档位
优化: min Σ(实例$ + egress$ + 存储$)   约束: 完成 ≤ deadline; 在线退化 ≤ 档位; 结果等价
杠杆: P3 吞吐↑ · P4 收割↓边际成本 · P5 异构↓硬件单价 · P2 跨区/neocloud↓最低价 · P6 workflow 去重↓总 token
```
这是 OpenAI batch(黑盒 5 折)和 SkyPilot(工作负载无关、推理不可知)都给不了的——**AIBrix 是云原生 batch 的入口**。

---

## 版本映射

```
v0.7.0  P0 Foundation(重构 + neocloud provisioning Lambda/RunPod + 定价感知 planner 基础 + 硬化)
        ‖ 并行研究: P1 隔离保障
v0.8    P1 隔离保障落地 → P4 harvesting(单模型) 起步;  P2 spot/跨区(对标 SkyPilot)起步
v0.8–9  P3 吞吐(任意可插) ; P5 异构(先 PipeMax 池)
v1.0    ★ 北极星(统一成本优化器 + 成本 SLO) ; P6 workflow
```

依赖:`P0 ─┬─► P2(spot,需 P0 的多provider+定价) ─► ★`
` ├─► P1(研究) ─► P4(harvesting,被 P1 gate)`
` ├─► P3(吞吐,任意时点) ─► ★`
` └─► P5(异构) ─► ★ ; P6 独立产品面`

> 注:本文档为**纯规划**。不含功能代码;"怎么实现(fork/upstream 等)"按决策 D-C 视为后续细节,不在此展开。
