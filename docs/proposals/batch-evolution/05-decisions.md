# ⑤ 决策日志 (Decision Log)

> 经过与 maintainer 的几轮对齐,v0.7.0 与 batch 长期演进的方向已收敛。本文件是**唯一事实来源**:
> `02-roadmap` 与 `04-PRFAQ` 以此为准。每条决策含:**结论 / 理由 / 被否决的备选 / 影响**。

---

## 总框架:两根支柱

便宜的 batch = **便宜的专属算力 (Sourcing)** + **便宜的共享算力 (Isolation)**。

- **Sourcing** —— 把活跑在最便宜的算力上:neocloud → spot/跨区 → 异构/offload。
- **Isolation** —— 安全地和在线共享算力:online↔offline 隔离*保障* → 收割 (harvesting)。

**v0.7.0 先打 Sourcing 的地基**(低风险、立竿见影、且是后续一切的底座);**Isolation 作为关键研究赌注**并行推进,
其交付物是"机制 + 有界的在线影响",而不是急于把 harvesting 做成 feature。

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

## D-B — colocation 单模型,但**不作近期重点**;**isolation 保障 = 研究重点**

- **结论**:colocation 若做,走**单模型**;但它**风险最高、近期可落地性最低,不作为重点**。真正值得投入研究的是
  **online↔offline 隔离保障——确保在线不被影响**。
- **理由**:HyGen/ConServe/Valve 全部假设单模型单引擎,而 AIBrix 现实是多模型机队;且 harvesting 直接碰付费在线 SLO。
  先把"在线不被伤害"的**保障**做扎实,harvesting 才配做成 feature。"有界的在线影响"也是比"我们会收割"更可信的卖点。
- **被否决**:① 近期就上多模型 colocation(开放难题,过早);② 直接把 harvesting 当 v0.7.0/v0.8 头条 feature(没有隔离保障兜底)。
- **影响**:
  - roadmap 把家族 B 拆成 **"P1 隔离保障(研究)→ P4 harvesting(feature,门槛=P1 成立)"**。
  - 隔离保障的技术内核 = Valve(联合有界抢占延迟+率、MIAD 显存、channel 隔离)+ ConServe(层级抢占);交付物 = **机制 + 可证/有界的在线影响**,用 `03` 的 W5 在线-trace 实验验"在线 p99 不受影响"。

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

## Feature 优先级(P0–P6,据上述决策定稿)

| 优先级 | Feature / 故事 | 支柱 | 风险 | 何时 |
|---|---|---|---|---|
| **P0** | **可移植多 provider batch**:重构(MDS/planner/RM)+ 原生 neocloud launch(Lambda/RunPod)+ 定价/容量感知 planner(基础) | Sourcing | 低 | **v0.7.0 主线** |
| **P1** | **online↔offline 隔离保障**(机制 + 有界在线影响,研究为主) | Isolation | 高(研究) | 并行研究,先于任何 harvesting |
| **P2** | **成本感知 spot/跨 provider 放置**(SkyNomad 式 + 推理原生 checkpoint)—— **替代 SkyPilot 的差异化点** | Sourcing | 中 | P0 之后 |
| **P3** | **batch 感知吞吐**(prefix 重排 + 资源混排) | — | 中低 | 任意时点的纯 $/token win |
| **P4** | **harvesting 做成 feature**(单模型) | Isolation | 高 | **门槛 = P1 保障成立** |
| **P5** | offload / 异构档位(CPU/attention 下沉、两层) | Sourcing | 中高 | later |
| **P6** | workflow batch(新产品面) | — | 中 | later / 研究线 |

---

## v0.7.0 范围(In / Out)

- **In**:重构 MDS/planner/RM;**原生 neocloud backend(Lambda/RunPod)**;定价/容量感知 planner(基础版);保持 OpenAI 兼容;成本计量;可观测(队列深度/JCT/$/1M tok)。
- **Out(明确不在 v0.7.0)**:harvesting/colocation、prefix 重排、spot/跨区、offload、workflow——这些是 what's-next。
- **并行(研究,不一定随 v0.7.0 发)**:online↔offline 隔离保障 (P1)。
- **一句话**:*v0.7.0 = "AIBrix 成为云原生 batch 的入口,能在最便宜的(neo)云上跑 OpenAI 兼容的 batch,不依赖 SkyPilot。"*
