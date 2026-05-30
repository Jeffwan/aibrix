# ⑦ 从实现沉淀 Research Paper:brainstorm + novelty 验证 + 文献

> 目标:如果你在 AIBrix Batch 上投入了大量实现工作,**能沉淀出什么 paper?哪些点真有 novelty?**
> 方法:5 个候选贡献点,每个都让独立 agent 对抗式地查了 arXiv + OSDI/SOSP/NSDI/MLSys/ASPLOS/EuroSys/SoCC/ATC(2023–2026)。
> 下面是**判决 + 推荐的论文组合 + 必做实验 + 审稿人反驳与辩护 + 目标会议 + 必引文献 + 风险登记册**。

---

## 0. 结论(TL;DR)

**别想着"一篇大而全"——拆成一个组合,风险递进:**

| 推荐 | Paper | 核心 novelty | 判决 | 风险 | 目标 |
|---|---|---|---|---|---|
| **先发** | **C. Measurement**:跨云/neocloud/spot/收割 的 batch 推理**成本实测** | 学术界**没有** neocloud $/token + spot 可用性的严肃测量 | 白空间确认 | 低 | SoCC/IMC/MLSys(或 workshop 首发) |
| **主攻** | **B. 多模型在线↔离线隔离保障**(#3) | 异构多模型共置的**有界在线影响**无人做 | NOVEL | 中 | OSDI/SOSP/MLSys/EuroSys |
| **旗舰** | **A. 推理原生、成本最优的 batch 控制面**(#1+#2+#4) | 跨 hyperscaler+neocloud+spot+收割 的**推理原生统一控制面** | 白空间确认,但需打透交互效应 | 高 | OSDI/SOSP/NSDI |

一句话:**C 打底(数据+成本模型)→ B 是干净的研究贡献(也正是你定的 P1 研究重点)→ A 是大旗舰(把 #1 的强机制 + #2 的调度交互 + #4 的 cost-SLO 框架合一)。**

---

## 1. 怎么操作:把实现变成 systems paper(流程)

1. **Claims-first(先写主张,再补系统)**。用 CARD 钉死:**C**ontext(问题/为什么现在)· **A**ttack(为什么难、为什么现有方法不行)· **R**esolution(那个**不显然的洞察**)· **D**emonstration(系统 + 评测)。顶会买的是"一个新抽象""一个让 X 变可行的非显然机制""一个反直觉的测量结论"——**纯工程拼装过不了**。
2. **每个 claim 算 novelty delta**:找最接近的 2–3 篇,一句话说清差别(见 §3 已经做好)。
3. **实验先为"堵最强反驳"设计**:对每个 paper,先列审稿人最可能的"这不就是 X 吗",再设计正好反驳它的实验(见 §4 各 paper 的"必做实验")。
4. **强基线 + 消融 + 诚实口径**:vLLM 离线 / SpotServe / SkyNomad / SageServe / Valve 等对应 SOTA;逐技术消融;$/token 双口径(见 `03-experiments.md`)。
5. **低风险首发**:先发 **measurement paper(C)** 或 workshop(MLSys/NeurIPS workshop、HotInfra/HotNets 类),占坑 + 攒数据 + 建立 cost model,再投顶会主系统(A/B)。
6. **节奏**:MLSys(秋投/次年初)、OSDI(冬投/夏发)、SOSP(春投/秋发)、EuroSys、ATC、SoCC。measurement 可投 IMC/SoCC。**先用 arXiv 占旗**(尤其这领域 2025–2026 抢得很凶,见 §6 风险登记册)。

---

## 2. AIBrix 的天然优势(为什么你来写最合适)

- **真实大规模系统 + 已有 arXiv 主系统**(AIBrix arXiv:2504.03648)——顶会偏爱"有真实部署的系统贡献"。
- **推理原生**:你能拿到 KV/prefix/PD/路由内部状态——这是 SkyPilot/SkyNomad(工作负载无关)和云调度器拿不到的,正是多篇 novelty 的根。
- **OpenAI-Batch 端到端 + K8s 原生**:能在真实多云/neocloud(Lambda/RunPod)上跑端到端实验,产出别人没有的跨 provider 数据。

---

## 3. Novelty 判决总表(5 角度,已对抗式查证)

| 角度 | 判决 | 最接近前作(必须区分) | 一句话 delta |
|---|---|---|---|
| **#1 推理原生跨区/跨云 checkpoint 迁移(batch)** | **NOVEL(caveat)** | SpotServe(单区/在线)、SkyNomad(整机/单云)、ServerlessLLM(到达端重算)、ConServe(单机内) | 首个"请求队列+prefix-KV 粒度 × 跨区跨 provider × 免重算 × 成本最优放置 × batch"的合取;前作没一个覆盖超过两点 |
| **#2 统一两级联合调度(收割⊕spot⊕异构)** | **部分新颖(最易被攻击)** | **SageServe(覆盖~2.5 轴!)**、SkyServe、SkyNomad、Valve/ConServe/HyGen(各单轴) | 唯有"推理引擎原生 + 跨 provider 外部 spot 采购 + 可证轴间交互(harvest-first/spill-to-spot > per-axis 贪心)"才算贡献 |
| **#3 多模型在线↔离线隔离保障** | **NOVEL** | Valve(单模型/经验界)、OOCO(单模型)、MuxServe(纯在线多模型) | 首个"异构多模型(不同架构/并行度)共置的有界在线影响 SLO";诚实贡献=跨模型对紧致经验界 + 条件性解析充分条件 |
| **#4 Cost-SLO 抽象 (T,B,ε)** | **部分新颖** | Can't-Be-Late、SkyNomad(均 cost+deadline,无 ε)、HyGen(ε 仅经验旋钮) | 三方耦合 cost×deadline×在线干扰 + **ε→价格档位机制**在 LLM batch 上无人形式化;但**抽象需挂在能强制执行它的系统上** |
| **landscape** | **白空间确认** | SkyServe(在线)、SkyNomad(通用 batch/单云)、WVA/HeteroScale/SageServe(单集群) | "推理原生 × 跨 hyperscaler+neocloud × spot+收割 × LLM batch 语义 × 成本最优"的统一控制面**无人发表** |

---

## 4. 推荐论文组合(逐篇)

### Paper C —《The Economics of Batch LLM Inference across Clouds, Neoclouds, Spot, and Harvested Capacity》(measurement,先发)

- **白空间(已确认)**:现有成本论文(*Price of Progress*、*Tiered Super-Moore's Law*、*Beyond Benchmarks*)只测**API 价格趋势**;**没有**学术工作测 **基础设施级 $/token 跨资源类型(spot vs on-demand vs neocloud vs 收割)**,也没有 **neocloud(Lambda/RunPod/CoreWeave/Crusoe/Vast)的 spot 可用性/抢占率/价格**的严肃测量(只有 Cast AI 等行业博客)。
- **claim**:首个跨 hyperscaler + neocloud + spot + 收割 的 batch LLM 推理**成本分解与实测**,给出 `$/1M tok = f(资源类型, 模型, 负载)` 的经验模型 + neocloud spot 可用性特征。
- **必做**:在 Lambda/RunPod/(+1 hyperscaler)上真实跑代表性 batch 负载,实测 goodput 与价格;采集多区 spot 价格/可用性时间序列;给出成本分解(GPU/CPU/egress/idle)。
- **为什么先发**:数据真、争议小、快;**它产出的 cost model 正好被 A/B 引用**;也直接喂 `03-experiments.md` 的两张"杀手图"和 cost-calculator。
- **venue**:SoCC / IMC / MLSys(measurement track),或先 workshop。**风险:低。**

### Paper B —《Bounded Online-Impact Isolation for Multi-Model Online–Offline GPU Co-location》(主攻,= 你的 P1 研究重点)

- **novelty(NOVEL)**:无人提供**异构多模型**(在线/离线跑不同架构、不同并行度,共享 GPU)的**有界在线影响 SLO**。Valve 是单模型且经验界;OOCO 单模型;MuxServe 纯在线。
- **claim(最小)**:"一个隔离机制 + 在异构多模型对上成立的有界在线影响(TTFT/TPOT 退化 ≤ ε)的刻画——前作无此界。"
- **最强版本**:做一个**模型架构无关、可证跨模型进程组合**的抢占原语(类比 Valve 的 CUDA channel,但证明能跨不同模型进程组合),再配跨模型 KV 显存干扰分析。
- **必做实验(为堵反驳)**:
  - 反驳"Valve 已做" → 证明 Valve 的 MIAD 是按单模型 KV 足迹标定的,**换不同架构对就失效/需重标**;给出 ≥3 个在线-离线模型对 × ≥2 并行度 × ≥2 硬件代的紧致经验界。
  - 反驳"分区跑两份就行(MPS/MIG)" → 证明**空间分区浪费利用率**,而**时间共享 + 有界保障**才是贡献。
  - 给一个**条件性充分条件定理**(即使 loose):"若离线抢占延迟 ≤δ、率 ≤ρ,则 TTFT 退化 ≤ f(δ,ρ,模型对)"。
- **诚实边界**:若拿不到解析保证,就老实写"跨 N 个模型对的紧致经验界 + 条件性解析包络"——Valve 自己也是经验界且被接收。
- **venue**:OSDI/SOSP(若有解析界或大规模生产证据)/ MLSys/EuroSys(若纯系统+经验界)。**风险:中。最干净、最不"glue"。**

### Paper A —《An Inference-Native, Cost-Optimal Control Plane for Batch LLM Inference across Harvested, Neocloud, and Spot Capacity》(旗舰)

- **白空间(已确认)**:无单一系统统一 (a) batch/offline LLM 语义 (b) 跨多 provider+neocloud (c) spot (d) 收割 (e) 成本最优,且**推理原生**。SkyServe=在线;SkyNomad=通用 batch/单云/工作负载无关;WVA/HeteroScale/SageServe=单集群。
- **三块合一**:
  - **机制核心 = #1(最强 novelty)**:推理原生、请求队列+prefix-KV 粒度的**跨区跨 provider 迁移,免 prefill 重算**。
  - **算法核心 = #2(需打透交互)**:两级联合调度,**必须证明 harvest-first/spill-to-spot 的轴间交互 > per-axis 贪心**(目标 >15%,最好加一个**联合调度 NP-hard 而单轴可解**的 hardness 结果)。
  - **抽象/框架 = #4**:以 **cost-SLO (T,B,ε)** 为设计主线(**别以"我们提了个抽象"开头**,要以"用户需要联合表达 cost×deadline×在线干扰,而无系统支持,所以我们造了能强制执行它的系统"开头)。
- **必做实验(为堵最强反驳"这不就是 SageServe + SpotServe + SkyNomad 拼一起")**:
  - **直接对比 SageServe**:证明它是 **VM 路由/单 CSP/内部 spot donation**,而你是**引擎原生 + 跨 provider 外部 spot 采购 + token/请求级决策**;给出引擎原生能做、VM 级做不到的具体收益(如 prefill 放收割 A100、decode 放 spot H100 的**跨层联合分派**收益)。
  - **真实跨云实验**:至少 AWS + GCP 或 + 一个 neocloud;给 deadline-miss 率 + 成本分解。
  - **消融**:joint vs harvest-only / spot-only / hetero-only / SageServe 式两级 ILP——joint 在"deadline 下成本"上**显著且非平凡领先**。
  - **迁移开销**:免重算 + 秒级 vs SkyNomad 整机 ~6min 的实测对比。
- **venue**:OSDI/SOSP/NSDI。**风险:高(范围大、SageServe 近),但白空间真,且推理原生是 SageServe/SkyNomad 结构上给不了的。**

---

## 5. 必引 & 必区分文献(closest threats)+ 不要 claim 的"已占领地"

**必须正面区分(最危险):**
- **SageServe**(arXiv:2502.14617,Microsoft)——覆盖~2.5 轴的两级 ILP;A 的头号对手。
- **SkyServe**(EuroSys'25,2411.01438)——跨云+spot,但**仅在线**。
- **SkyNomad**(2601.06520)——多区 spot batch,但**整机/单云/工作负载无关**。
- **SpotServe**(ASPLOS'24,2311.15566)——spot 迁移,但**单区/在线**。
- **Valve**(2604.07874)——有界抢占,但**单模型/经验界**(B 的头号对手)。
- **WVA**(2603.09730,IBM)/ **HeteroScale**(2508.19559)——成本/异构控制面,但**单集群、无 spot/跨云**。
- **MuxServe**(2404.02015)、**OOCO**(2511.21862)——多模型/在线-离线,但分别"纯在线""单模型"。

**其余必引**:ConServe(2410.01228)、HyGen(2501.14808)、Glinthawk(2501.11779)、BatchLLM(2412.03594)、BlendServe(2411.16102)、NEO、PipeMax、Halo(2509.02121)、Mélange(2404.14527)、Mooncake(2407.00079)、Llumnix、Aegaeon(SOSP'25)、ThunderServe(2502.09334)、ServerlessLLM(2401.14351)、Can't-Be-Late(NSDI'24)、Sarathi-Serve(2403.02310)、InferSave(2504.11816)、AIBrix(2504.03648)。
**成本测量必引**:Price of Progress(2511.23455)、Tiered Super-Moore's Law(2603.28576)、Beyond Benchmarks(2510.26136)、Systematic Characterization of LLM Inference on GPUs(2512.01644)、Cast AI GPU pricing(行业)。

**不要 claim 的"已占领地"(否则被秒拒):**
1. "首个 spot 上服务 LLM" → SpotServe 已占。
2. "首个跨云/多区 spot 服务 LLM" → SkyServe 已占(在线)。
3. "首个 deadline 感知多区 spot batch 调度" → SkyNomad 已占(通用 batch)。
4. "首个在线↔离线收割共置" → ConServe/Valve/HyGen 已占(单模型)。
5. "首个异构 GPU 自动扩缩/解耦服务" → HeteroScale/SageServe 已占(单集群)。
6. "LLM token 价格年降 5–10×" → 测量论文已占。
→ 所以 claim 必须**带上各自的差异化定语**(推理原生 / 跨 provider / 多模型 / batch 语义 / 交互效应)。

---

## 6. Novelty 风险登记册(要持续盯的 preprint)

这领域 2025–2026 抢得很凶,以下可能**缩小你的 novelty,需持续监控并尽快 arXiv 占旗**:
- **Prefill-as-a-Service: KVCache cross-datacenter**(2604.15039)——跨 DC 的 KV,可能逼近 #1。**重点盯。**
- **SageServe v3**(2502.14617 修订)——若补了多 provider spot + 异构实测,#2 空间进一步缩。
- **Coral**(2605.04357)——异构云 GPU 服务联合资源分配;若含 spot/收割则与 A 相关。
- **SkyNomad 后续/camera-ready**——若加多云,#1/#4 的"跨 provider"差异变窄。
- **GFS**(2509.11134)、**Aegaeon**(SOSP'25)、**WVA**(2603.09730)——单集群/单云,但若扩展到跨云需重新评估。

**行动**:A/B 一旦有可展示结果,**尽快 arXiv 占旗**;measurement(C)可最快产出,先把"跨 neocloud 成本/可用性"这块数据和结论锁定。

---

## 7. 一页纸建议(给你拍板)

- **最稳的两步**:先 **C(measurement)** 快速占住"neocloud batch 成本"空白并建 cost model;再 **B(多模型隔离)** 作为干净的顶会主攻(也正是 P1 研究重点)。
- **最大回报但最难**:**A(推理原生成本最优 batch 控制面)**——白空间真,但要靠 **#1 的迁移机制 + #2 的交互效应证明 + 直接打 SageServe** 才立得住;建议在 B/C 之后、系统更成熟时投。
- **共同前提**:所有 paper 的可信度都依赖 `03-experiments.md` 的诚实口径与强基线;**先把评测脚手架搭好**(这也和你"先把事情了清楚、暂不写功能代码"的节奏一致——评测/测量先行,实现随 roadmap P0→ 推进)。
