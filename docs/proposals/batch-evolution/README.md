# AIBrix Batch: 从 OpenAI-Batch 克隆到「云原生弹性批量推理引擎」的演进

> 状态: Draft / 内部战略文档 (planning artifact, 不进 Sphinx docs build)
> 范围: AIBrix Batch 子系统的差异化能力、长期演进路线、实验验证方法、v0.7.0 市场叙事 (PRFAQ)
> 作者: AIBrix maintainers (draft)
> 关联代码: `python/aibrix/aibrix/batch/`, `apps/console/api/`

---

## 0. TL;DR

今天的 AIBrix Batch 是一个**功能正确、但只触及控制面**的 OpenAI Batch API 克隆:
它把一个 batch job 拆成请求,起一个**专属的、原生 vLLM** sidecar,然后**逐条 HTTP 回放**
(`ProxyInferenceEngineClient` → `localhost:8000`),所有 batching 都交给引擎自己做。
它没有用上 AIBrix 服务面**已经具备**的任何数据面能力(KV offloading、分布式 prefix cache、
P/D 解耦 StormService、KVCache V1 connector、负载感知路由、autoscaler),资源面正在重构中。

闭源厂商 (OpenAI / Anthropic / Google) 的 batch 都是**对在线 list price 打 5 折 + 24h 窗口**。
那是**定价让利**(填谷、让出 margin),不是**成本下降**——而且是黑盒:你不能自带硬件、
看不到也调不动成本结构、无法和自己的在线机队 co-locate。

**AIBrix 的定位**: 做**云原生 batch 的入口**——一个开源、自托管、OpenAI 兼容、且能**把活跑在最便宜算力**上的
批量推理引擎。它把 batch 从「又一个 API 克隆」变成 AIBrix 落地前沿数据面创新的**最佳载体**(因为 batch 的
24h 松 SLO 让这些激进优化都变得安全)。

本目录是七个文件(交付物 + 决策日志 + 量化评估):

| 文件 | 内容 | 语言 |
|---|---|---|
| [`01-paper-breakthroughs.md`](./01-paper-breakthroughs.md) | ① 从 10 篇 paper 提炼的 **5 类突破口** + 可借鉴点 + 不该照搬的地方 | 中文 |
| [`02-evolution-roadmap.md`](./02-evolution-roadmap.md) | ② **演进路线 + 工程 roadmap**,按 **feature 优先级 P0–P5** 组织,落到真实文件/符号 | 中文 |
| [`03-experiments.md`](./03-experiments.md) | ③ **实验与验证方法**:指标 / 基线 / 负载 / A-B 协议 / 成本模型 / SkyPilot bake-off | 中文 |
| [`04-v0.7.0-prfaq-blog.md`](./04-v0.7.0-prfaq-blog.md) | ④ **v0.7.0 batch 单独发布**的 PRFAQ + blog 草稿 + 分阶段上市 | English |
| **[`05-decisions.md`](./05-decisions.md)** | **决策日志(A/B/C/D 拍板结论 + 理由 + 被否决项 + P0–P5 优先级)。其余文档以此为准。** | 中文 |
| [`06-north-star-prfaq-blog.md`](./06-north-star-prfaq-blog.md) | 北极星(全部 P 落地后)的 PRFAQ + blog,以 **cost-SLO** 为主线;v1.0 叙事 | English |
| [`07-research-paper-ideas.md`](./07-research-paper-ideas.md) | 从实现沉淀 **research paper**:5 角度 novelty 验证 + 推荐论文组合 + 必引文献 + 风险登记册 | 中文 |
| [`08-AD-quantified-walkthrough.md`](./08-AD-quantified-walkthrough.md) | **A/D 收益量化**:具体 batch 文件 + 长度分布,逐步算清收益来自哪 + 落地可行性 | 中文 |

> **方向已拍板(见 `05-decisions.md`)**:v0.7.0 = **架构重构(MDS/planner/RM)+ 原生 neocloud provisioning(Lambda/RunPod)**,
> 定位为**云原生 batch 的入口、替代 SkyPilot**。近期主攻 **D(跨云 spot)与 A(吞吐)**;
> **online↔offline 共置(B)降为最低优先、可能不做**(生产混步难且不安全)。

---

## 1. 战略主线 (贯穿四个交付物)

**论点:闭源 batch 卖的是「定价折扣」,AIBrix 要做的是「云原生 batch 入口 + 真正的成本下降」。**

批量推理相对在线推理有一个结构性优势——**整批已知 + 松 SLO (24h)**。成本主要靠两根杠杆:

- **Sourcing(便宜的算力)**:把活跑在最便宜的算力上——neocloud(Lambda/RunPod,常比超大厂便宜 50–80%)
  → spot/跨区套利(家族 D)→ 异构/offload(家族 C)。
- **Throughput(更高吞吐)**:同硬件产出更多 token——整批 prefix 共享 + 资源混排(家族 A)。
- **(可选,已降级)Isolation**:和在线机队共享算力(收割,家族 B)——**生产混步又难又不安全,降为最低优先、可能不做**。

在 $/token 上,Sourcing 与 Throughput **乘性叠加**,这就是「能可信走到 5 折以下」的数学来源。

**关键洞察 ①——大部分能力 AIBrix 已经有了/正在建,只是 batch 没接上。** 服务面在 v0.3–v0.6 已交付
KV offloading framework、分布式 prefix cache、P/D 解耦、KVCache V1 connector、负载感知路由、autoscaler;
而 resource manager / planner 的多 provider 重构正在进行(#2239/#2245/#2243)。Roadmap 的相当一部分是**接线**,而非造轮子。

**关键洞察 ②——vs SkyPilot 的护城河是「云原生 + 推理原生」。** SkyPilot 是**工作负载无关**的 CLI/VM 编排器
(整机 checkpoint、~6 分钟冷启动、gang 抢占)。AIBrix 是**云原生(K8s)、推理原生、API 优先**的入口:
按「请求队列 + prefix-KV」粒度 checkpoint(KB–GB、秒级)、迁移解耦的 P/D 单元。
SkyPilot 不知道什么是 KV cache;AIBrix 知道。**我们替代它,不依赖它。**

---

## 2. 五类突破口 × 你的目标 (速览)

> 详见 [`01-paper-breakthroughs.md`](./01-paper-breakthroughs.md)。标签: 💰=降单价 ⚡=弹性算力 🚀=提吞吐 🆕=新产品面。
> (注:下表是**能力分类**;**优先级**见 §3——其中家族 B 已降为最低优先。)

| 家族 | 代表 paper | 核心机制 (一句话) | 主要杠杆 | AIBrix 已有的可接入原语 | 净新增 |
|---|---|---|---|---|---|
| **A. Offline 感知调度** | BatchLLM, BlendServe | 整批已知 → 全局 prefix 树重排 + 计算/访存互补混排 + 显存预算成批 | 🚀→💰 | 分布式 prefix cache, KVCache connector | 批级 optimizer pass、显存预算成批、引擎 hint |
| **B. 在线↔离线 colocation / 收割** | HyGen, ConServe, Valve | batch 当「填充料」跑在线机队空隙,延迟预算准入 + 细粒度抢占 + 有界抢占率 | ⚡→💰 | 负载感知路由, autoscaler, mixed-workload routing (v0.6) | (已降级)隔离保障、放置模型、价格档位旋钮 |
| **C. 内存层级 / 异构硬件** | NEO, Glinthawk, PipeMax | 把访存受限的 KV+attention 下沉到 CPU / 商用 PCIe GPU,流水线掩盖搬运 | 💰 (⚡) | KV offloading framework, P/D 解耦, KVCache V1 connector | CPU attention 执行、商用/异构 batch 池、成本感知 planner |
| **D. 跨区/跨云 spot (Sky Computing)** | SkyNomad | 统一货币成本模型 (价格+可用性+egress+deadline) 主动跨区迁移 spot | 💰⚡ | RM catalog 已建模 region/instance/pricing/prediction | spot 放置控制器、**推理原生 checkpoint**、deadline 安全网 — **替代 SkyPilot(自有 neocloud backend)** |
| **E. Agentic / workflow batch** | Halo / Helium | 一批 workflow DAG 当一个查询: 跨实例去重 + KV 血缘路由 + DP 放置 | 🆕💰🚀 | 路由, prefix cache | workflow IR/API、DAG planner、KV 血缘路由、tool 去重 |

---

## 3. 演进路线速览 (按 feature 优先级 P0 → 北极星)

> 详见 [`02-evolution-roadmap.md`](./02-evolution-roadmap.md) 与 [`05-decisions.md`](./05-decisions.md)。
> 主轴:**Sourcing(便宜算力)× Throughput(整批重排)**;共置(B)已降为最低优先、可能不做。

```
P0  Foundation: 重构(MDS/planner/RM) + 原生 neocloud provisioning   【v0.7.0 主线 · Sourcing】
P1  成本感知 spot / 跨 provider 放置 (D)   推理原生 checkpoint        【近期主攻 · 替代 SkyPilot · 你的重点】
P2  batch 感知吞吐 (A)                     prefix 重排 + 资源混排        【近期主攻 · 你的重点 · 任意时点】
P3  异构 / offload 档位 (C)                商用/spot 节点 + CPU/attention 下沉
P4  workflow batch (E)                     /v1/workflow-batch 新产品面
P5  online<->offline 隔离与收割 (B)        单模型                       【最低优先 / 可能不做 · 生产不安全】
★   北极星: 统一"batch 成本优化器" + 成本 SLO(deadline 内最低成本完成;不以收割为前提)
```

**v0.7.0 范围(已拍板):** **P0 = 架构重构 + neocloud provisioning(Lambda/RunPod)**,不讲 batch 高级 feature。
blog 标题方向: *"AIBrix Batch v0.7.0: A Cloud-Native Entry Point for Batch Inference on Any Cloud"*
—— 入口定位、替代 SkyPilot、跑最便宜的(neo)云。

---

## 4. 怎么验证 (速览)

> 详见 [`03-experiments.md`](./03-experiments.md)。

- **北极星指标**: `$ / 1M tokens`(prompt 与 generated 分开算,含 GPU-hr + CPU-hr + egress + 存储)。
- **市场对标基线**: OpenAI / Anthropic batch = 在线价 5 折(绝对值 + 比值两种口径)。
- **诚实内部基线**: 今天的 AIBrix batch(专属 vanilla vLLM)与 vLLM 离线 `LLM.generate`(专属上限)。
- **SkyPilot bake-off**: 同一个 deadline-bound 大 job,在真实 spot trace 上比 AIBrix 跨区方案 vs SkyPilot managed-spot。
- **v0.7.0 的核心图**: `$/1M tok(neocloud Lambda/RunPod)` vs 超大厂 on-demand vs 闭源 Batch —— 支撑"跑最便宜的云"主张。
- **联动 cost-calculator**: 让 [batch-cost-calculator](https://aibrix.github.io/tools/batch-cost-calculator/)
  的吞吐/价格假设与实测对齐,使它成为可信的对标/销售工具。

---

## 5. 论文与外部引用

10 篇 paper 的精读摘要(身份、核心机制、量化结果、适用前提、可借鉴点、不该照搬)见
[`01-paper-breakthroughs.md`](./01-paper-breakthroughs.md) 附录。其余引用:

- [AIBrix Batch Cost Calculator](https://aibrix.github.io/tools/batch-cost-calculator/)
- [ai-dynamo/aiconfigurator](https://github.com/ai-dynamo/aiconfigurator)
- 竞品: [OpenAI Batch API](https://platform.openai.com/docs/guides/batch) / [Anthropic Message Batches](https://www.anthropic.com/news/message-batches-api) — 均为在线价 5 折、≤24h。
- SkyPilot 对标: [SkyPilot](https://docs.skypilot.co/) / [Can't Be Late (NSDI'24)](https://www.usenix.org/conference/nsdi24/presentation/wu-zhanghao) / [SpotServe](https://arxiv.org/abs/2311.15566)
- neocloud(v0.7.0 接入目标): [Lambda](https://lambda.ai/) / [RunPod](https://www.runpod.io/)
