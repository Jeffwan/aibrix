# ⑤ 决策日志 (Decision Log)

> 经过与 maintainer 的几轮对齐,v0.7.0 与 batch 长期演进的方向已收敛。本文件是**唯一事实来源**:
> `02-roadmap` 与 `04-PRFAQ` 以此为准。每条决策含:**结论 / 理由 / 被否决的备选 / 影响**。

---

## 总框架:主轴是 Sourcing × Throughput(Isolation 已降级)

便宜的 batch 主要靠 **便宜的算力 (Sourcing)** × **更高的吞吐 (Throughput)**:

- **Sourcing** —— 把活跑在最便宜的算力上:neocloud → spot/跨区 → 异构/offload(家族 D + C)。
- **Throughput** —— 同硬件产出更多 token:整批 prefix 共享 + 资源混排(家族 A)。
- **(可选,已降级)Isolation** —— 和在线共享算力(收割,家族 B):**生产上混步又难又不安全,降为最低优先、可能不做**(决策 D-E)。

**v0.7.0 先打 Sourcing 的地基(P0:重构 + neocloud)**;近期主攻 **D(spot/跨云)与 A(吞吐)**;
异构/offload(C)随后;**共置(B)排到最后、可选**。北极星不再以"收割"为前提。

---

## D-A — v0.7.0 = 架构重构 + neocloud provisioning(**不讲 batch 高级 feature**)

- **结论**:v0.7.0 的主线是**重构 MDS / planner / resource manager**,并**突出 neocloud 接入**——
  能在 Lambda、RunPod 等 neocloud 上 launch batch。**不**把 prefix 重排、harvesting 等 batch 自身高级 feature 作为 v0.7.0 卖点。
- **理由**:
  1. 这个手术**已经在动**:`refactor[planner] split provider-agnostic core (#2239)`、`feat(RM): add extension support (#2245)`、`batch: upstreamable storage/drivers/resource schema/model discovery (#2243)`。
  2. neocloud 的 H100/A100 常比超大厂 on-demand 便宜 50–80%——**"自托管 batch 跑在最便宜 GPU 上、还不用改代码"** 本身就是一个**低风险、立刻成立**的成本故事,不需要任何调度黑科技。
  3. 它是 spot/跨区(Sourcing 后续)和成本感知放置的**底座**。
- **被否决**:把 M1(吞吐)/M2(harvesting)塞进 v0.7.0 —— 范围过大、风险高(harvesting 碰在线 SLO)、且会抢走"重构 + 可移植"这个清晰叙事的焦点。
- **影响**:
  - roadmap 的 **M0 升格为 "Foundation:重构 + 多 provider/neocloud provisioning"**,成为 v0.7.0 主体。
  - 落点是三个**已存在的 seam**各加一个 provider,而非从零:
    - `resource_manager/catalog`(`interface.go`/`factory.go`):`Catalog = Provider + ResourceCatalog + PricingCatalog`,已有 `ListRegions/ListInstanceTypes/ListPricing/ListPricingPredictions/ListResourcePredictions`;`NewCatalog` 非 k8s 走 `NewCatalogExtension`。→ neocloud = 注册新 `ResourceProvisionType` + catalog 扩展。
    - `resource_manager/provisioner`(`Provisioner.Type()`):实际 launch 的实现。→ neocloud = 新 provisioner。
    - `planner/impl/backend.go`:`backendRegistry` + `RegisterBackend`;`defaultPlannerBackend` 注释已写 **"serves kubernetes / aws / lambdaCloud"**,`Schedule()` 是"未来 capacity-aware 调度"的钩子,TODO 要加 `Plan()` 拉 catalog 容量。→ 成本/容量感知放置挂这里。

---

## D-B / D-E — colocation(家族 B)**降级:单模型、最低优先、可能不做**

- **结论(本轮修正,取代早先"isolation = 关键研究赌注"的说法)**:online↔offline 共置(隔离 + 收割)**降为最低优先级,排在异构/offload(C)之后,作为可选探索,最终完全可能不走这条路径**。若真要做,走**单模型**。
- **理由**:**生产上在线/离线混步难度大、不安全**(碰付费在线 SLO);HyGen/ConServe/Valve 都假设单模型单引擎,而 AIBrix 现实是多模型机队;价值不确定。
- **被否决**:① 把"隔离保障"当**关键研究赌注 / 与 v0.7.0 并行**(早先版本,已废);② 近期就上多模型 colocation;③ 把 harvesting 当近期头条 feature。
- **影响**:
  - roadmap 中家族 B = **P5(最低优先 / 可选 / 可能不做)**,排在 C 之后;**其余里程碑不依赖它**。
  - "便宜的共享算力"这条腿可砍;成本完全靠 **Sourcing(D spot/跨区 + C 异构/offload + neocloud)× Throughput(A)** 来打,这条线本身已足够强且生产安全。
  - research 里对应的 #3(多模型隔离)**随之降为"可选 research",不再是主攻**(见 `07`)。

---

## D-C — 能 upstream 就 upstream;**不进 feature 讨论**

- **结论**:数据面引擎钩子能 upstream 到 vLLM 就 upstream;AIBrix 的价值在**控制面联动**。fork-vs-upstream 是后续实现细节,**不占用 feature 决策的篇幅**。
- **理由**:AIBrix 在 vllm-project 下,upstream 战略一致、避免死 fork;但这是"怎么做",不是"做什么"。
- **影响**:roadmap/PRFAQ 不再讨论 fork/upstream;只讨论"放什么 feature / 讲什么故事"。引擎改动默认"能 upstream 则 upstream,AIBrix 侧留调度/编排/隔离"。

---

## D-D — **替代/竞争 SkyPilot**;AIBrix = **云原生 batch 的入口**

- **结论**:**竞争、替代** SkyPilot,不"站在它上面"。AIBrix **自己做原生 neocloud backend,不依赖 SkyPilot**。
- **理由**:若把 provisioning 与成本优化的"大脑"让给 SkyPilot,等于把入口和护城河让给竞品。AIBrix 要做**云原生 batch 的入口**,就必须**全栈自有**:provisioning + 放置 + API + 数据面。
- **被否决**:① 集成扩展(用 SkyPilot 做 provisioning backend)—— 会形成依赖、削弱入口定位;② 纯叙事对打而底层仍依赖它 —— 不诚实也不稳。
- **竞争定位(写进 blog)**:

  | 维度 | SkyPilot | AIBrix Batch |
  |---|---|---|
  | 形态 | 工作负载无关的 CLI / VM 启动器 | **云原生(K8s)基础设施 + OpenAI-Batch API 入口** |
  | 粒度 | 整机 / job | **推理原生**(KV/prefix/PD/路由感知;请求队列+prefix-KV 级 checkpoint) |
  | 接口 | CLI / YAML | **API 优先(OpenAI 兼容)端到端 batch 生命周期** |
  | provisioning | 自带多云 | **AIBrix 原生 neocloud backend(自有,不依赖 SkyPilot)** |
  | 差异化 | 云覆盖广、通用 | 云原生 + 推理原生 + API 优先 + 与控制面联动(隔离保障/KV/路由) |

- **诚实边界**:SkyPilot 成熟、支持云多;AIBrix 初期先做**关键 neocloud(Lambda/RunPod)**再扩。我们竞争点不是"支持多少云",而是上表后两行。

---

## Feature 优先级(P0–P5,本轮重排后)

| 优先级 | Feature / 故事 | 支柱 | 风险 | 何时 |
|---|---|---|---|---|
| **P0** | **可移植多 provider batch**:重构(MDS/planner/RM)+ 原生 neocloud launch(Lambda/RunPod)+ 定价/容量感知 planner(基础) | Sourcing | 低 | **v0.7.0 主线** |
| **P1** | **成本感知 spot/跨 provider 放置**(SkyNomad 式 + 推理原生 checkpoint)—— **替代 SkyPilot;你的重点之一** | Sourcing | 中 | P0 之后(v0.8) |
| **P2** | **batch 感知吞吐**(prefix 重排 + 资源混排)—— **你的重点之一** | Throughput | 中低 | 任意时点的纯 $/token win |
| **P3** | **异构 / offload 档位**(商用 GPU、CPU/attention 下沉、两层) | Sourcing | 中高 | v0.9 |
| **P4** | **workflow batch**(新产品面) | — | 中 | later / 研究线 |
| **P5** | **online↔offline 隔离与收割(单模型)** | (可选) | 高 + 生产不安全 | **最低优先 / 可能不做** |

---

## v0.7.0 范围(In / Out)

- **In**:重构 MDS/planner/RM;**原生 neocloud backend(Lambda/RunPod)**;定价/容量感知 planner(基础版);保持 OpenAI 兼容;成本计量;可观测(队列深度/JCT/$/1M tok)。
- **Out(明确不在 v0.7.0)**:harvesting/colocation、prefix 重排、spot/跨区、offload、workflow——这些是 what's-next。
- **降级 / 可能不做**:online↔offline 隔离与收割(家族 B,原"并行研究"提法已废)——见 D-B/D-E,排到 C 之后、可选。
- **一句话**:*v0.7.0 = "AIBrix 成为云原生 batch 的入口,能在最便宜的(neo)云上跑 OpenAI 兼容的 batch,不依赖 SkyPilot。"*

---

## 次要决策(已定)

| # | 议题 | 结论 |
|---|---|---|
| M1 | P0 的 neocloud 范围 | v0.7.0 先做 **Lambda + RunPod**,其余 provider 后续按需扩;resource manager 是**共享层**,在线服务以后可复用同一套 backend,但 v0.7.0 叙事**以 batch 为主**。 |
| M2 | 共置/隔离的落版 | **已降级(见 D-B/D-E)**:不再"与 v0.7.0 并行 / v0.8 落地";排到 C 之后作为可选 **P5**,可能不做。 |
| M3 | P0–P5 次序(本轮重排) | Sourcing 优先:**D(spot)=P1、A(吞吐)=P2、C(异构)=P3、E(workflow)=P4、B(共置)=P5 可选**。A 可任意时点并行插入。 |
