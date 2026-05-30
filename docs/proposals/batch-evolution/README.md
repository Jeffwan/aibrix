# AIBrix Batch: 从 OpenAI-Batch 克隆到「弹性批量推理引擎」的演进

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
P/D 解耦 StormService、KVCache V1 connector、负载感知路由、autoscaler),也没有弹性算力、
抢占、跨区 spot 等高级特性。

闭源厂商 (OpenAI / Anthropic / Google) 的 batch 都是**对在线 list price 打 5 折 + 24h 窗口**。
那是**定价让利**(填谷、让出 margin),不是**成本下降**——而且是黑盒:你不能自带硬件、
看不到也调不动成本结构、无法和自己的在线机队 co-locate。

**AIBrix 的差异化机会**: 做一个**开源、自托管、真正压低单 token 成本**的批量推理引擎——
通过「**batch 感知的数据面** + **弹性算力底座**」,让有效价格能**显著低于在线价的 5 折**,
并且全程可观测、可调、可对标。这把 batch 从「又一个 API 克隆」变成 AIBrix 落地前沿
数据面创新的**最佳载体**(因为 batch 的 24h 松 SLO 让这些激进优化都变得安全)。

本目录是四个交付物:

| 文件 | 对应你的问题 | 语言 |
|---|---|---|
| [`01-paper-breakthroughs.md`](./01-paper-breakthroughs.md) | ① 从 10 篇 paper 提炼的 **5 类突破口** + 可借鉴点 + 不该照搬的地方 | 中文 |
| [`02-evolution-roadmap.md`](./02-evolution-roadmap.md) | ② **长期演进路线 + 工程 roadmap** (M0–M5 + 北极星),落到真实文件/符号 | 中文 |
| [`03-experiments.md`](./03-experiments.md) | ③ **实验与验证方法**:指标 / 基线 / 负载 / A-B 协议 / 成本模型 / SkyPilot bake-off | 中文 |
| [`04-v0.7.0-prfaq-blog.md`](./04-v0.7.0-prfaq-blog.md) | ④ **v0.7.0 batch 单独发布**的 PRFAQ + blog 草稿 + 分阶段上市 | English |

---

## 1. 战略主线 (贯穿四个交付物)

**论点:闭源 batch 卖的是「定价折扣」,AIBrix 要卖的是「成本下降」。**

批量推理相对在线推理有一个结构性优势——**整批已知 + 松 SLO (24h)**。这让三类成本杠杆都变得可用,
而且每一类都有 paper 背书:

1. **同样的硬件,更高吞吐** (offline-only 调度): 全局 prefix 共享 + 资源感知混排 + 显存预算成批
   → 每 GPU-hour 产出更多 token。 *(BatchLLM, BlendServe)*
2. **边际成本算力** (弹性): 收割自己在线机队的「波谷」 + 跨区/跨云 spot 套利
   → GPU 要么已经付过钱了,要么便宜 3–10×。 *(HyGen / ConServe / Valve + SkyNomad)*
3. **更便宜的硬件单价** (内存层级/异构): KV 与 attention 下沉到 CPU / 商用 PCIe GPU
   → batch 跑在 T4 / RTX / CPU 层。 *(NEO, Glinthawk, PipeMax)*

再加一条**产品面扩张**: 把「一批 agentic workflow」当作一个查询来优化,提供闭源 batch API 没有的
`workflow-batch` 产品。 *(Halo / Helium)*

**关键洞察 ①——大部分能力 AIBrix 已经有了,只是 batch 没接上。** 服务面在 v0.3–v0.6 已经交付了
KV offloading framework、分布式 prefix cache、P/D 解耦 (StormService)、KVCache V1 connector、
负载感知路由、autoscaler。Roadmap 的相当一部分不是「从零造数据面」,而是**把 batch 接到这些已有原语上**,
再补一个 batch 感知调度器和一个弹性 sourcing 层。

**关键洞察 ②——vs SkyPilot 的护城河是「推理原生」。** SkyPilot / SkyNomad 是**工作负载无关**的
跨云 VM 编排器:整机 checkpoint (几百 GB、~6 分钟冷启动、gang 抢占)。AIBrix 可以做到**推理引擎原生**:
按「请求队列 + prefix-KV」粒度 checkpoint(KB–GB、秒级)、迁移解耦的 P/D 单元、并且把跨区 spot
和**在线机队收割**两级融合。SkyPilot 不知道什么是 KV cache;AIBrix 知道。这就是 Sky Computing
叙事里我们的差异点。

---

## 2. 五类突破口 × 你的目标 (速览)

> 详见 [`01-paper-breakthroughs.md`](./01-paper-breakthroughs.md)。标签: 💰=降单价 ⚡=弹性算力 🚀=提吞吐 🆕=新产品面

| 家族 | 代表 paper | 核心机制 (一句话) | 主要杠杆 | AIBrix 已有的可接入原语 | 净新增 |
|---|---|---|---|---|---|
| **A. Offline 感知调度** | BatchLLM, BlendServe | 整批已知 → 全局 prefix 树重排 + 计算/访存互补混排 + 显存预算成批 | 🚀→💰 | 分布式 prefix cache, KVCache connector | 批级 optimizer pass、显存预算成批、引擎 hint |
| **B. 在线↔离线 colocation / 收割** | HyGen, ConServe, Valve | batch 当「填充料」跑在线机队空隙,延迟预算准入 + 细粒度抢占 + 有界抢占率 | ⚡→💰 | 负载感知路由, autoscaler, mixed-workload routing (v0.6) | colocation runtime(准入+抢占)、SLO 预算控制器、放置模型、价格档位旋钮 |
| **C. 内存层级 / 异构硬件** | NEO, Glinthawk, PipeMax | 把访存受限的 KV+attention 下沉到 CPU / 商用 PCIe GPU,流水线掩盖搬运 | 💰 (⚡) | KV offloading framework, P/D 解耦, KVCache V1 connector | CPU attention 执行、商用/异构 batch 池、成本感知 planner |
| **D. 跨区/跨云 spot (Sky Computing)** | SkyNomad | 统一货币成本模型 (价格+可用性+egress+deadline) 主动跨区迁移 spot | 💰⚡ | (无;新层) | spot 放置控制器、**推理原生 checkpoint**、deadline 安全网 — **对标 SkyPilot** |
| **E. Agentic / workflow batch** | Halo / Helium | 一批 workflow DAG 当一个查询: 跨实例去重 + KV 血缘路由 + DP 放置 | 🆕💰🚀 | 路由, prefix cache | workflow IR/API、DAG planner、KV 血缘路由、tool 去重 |

---

## 3. 演进路线速览 (M0 → 北极星)

> 详见 [`02-evolution-roadmap.md`](./02-evolution-roadmap.md)。每个里程碑都给了落点文件、成功指标、工作量与风险。

```
M0  控制面硬化            (table stakes; 不是差异化,但是一切的地基)
     └─ 调度策略抽象化 / deadline 感知 / 取消 / 重试幂等 / 成本计量 / 多模型池
M1  Batch 感知数据面 (A)   ── 把"逐条回放"换成"批级优化 + 引擎 hint",接已有 prefix cache
M2  弹性 colocation (B)    ── batch 作为在线机队的填充料,先 HyGen 式准入,再 Valve 式抢占
M3  异构 / offload 档位 (C) ── 商用/spot 节点池 + CPU/attention 下沉,接 KV offloading framework
M4  跨区/跨云 spot (D)     ── 推理原生 checkpoint 的跨区迁移 —— 正面对标 SkyPilot/SkyNomad
M5  Agentic workflow (E)   ── /v1/workflow-batch 新产品面
★   北极星: 统一"batch 成本优化器" + 价格/SLO 档位
     └─ 两级 (机队内收割 + 跨区 spot) × (吞吐+硬件+地理) 联合最优,对用户暴露"成本 SLO"
```

**v0.7.0 实际范围 (建议):** M0 (硬化) + M1 第一刀 (prefix 感知重排,复用已有 prefix cache) +
M2 的 MVP (HyGen 式在线机队收割,单模型,feature flag 后)。这是一个有野心但可信的 v0.7.0,
blog 标题方向: *"AIBrix Batch: turn your idle GPUs into batch capacity — self-hosted batch
inference at a fraction of the closed-API price."*

---

## 4. 怎么验证 (速览)

> 详见 [`03-experiments.md`](./03-experiments.md)。

- **北极星指标**: `$ / 1M tokens`(prompt 与 generated 分开算,含 GPU-hr + CPU-hr + egress + 存储)。
- **市场对标基线**: OpenAI / Anthropic batch = 在线价 5 折(绝对值 + 比值两种口径)。
- **诚实内部基线**: 今天的 AIBrix batch(专属 vanilla vLLM)与 vLLM 离线 `LLM.generate`(专属上限)。
- **SkyPilot bake-off**: 同一个 deadline-bound 大 job,在真实 spot trace 上比 AIBrix 跨区方案 vs SkyPilot managed-spot。
- **核心可信图**: *有效价格 vs 收割比例曲线* 与 *有效价格 vs spot 占比曲线* —— 用来支撑
  "在 X% 波谷收割 + Y% spot 时,AIBrix batch = OpenAI batch 价格的 Z%"。
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
