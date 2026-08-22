# ⑨ 新论文收敛方案:Productive Profiling × Completion Window × 云容量

> **重审输入**:与 OpenAI 的完整讨论(temporal P/D 成文过程 + checkpoint 证伪 + productive profiling 收敛)。
> **前提锁定**:
> 1. **Temporal P/D 已单独成文** —— 新论文不写它、不 re-claim KV staging;只作"团队已有能力"引用。
> 2. **不做 online↔offline 共置**(见 `05-decisions.md` D-B/D-E),容量来源 = dedicated elastic(reserved + neocloud + spot)。
> 3. **目标 venue:EuroSys / SoCC(主),ASPLOS/ATC(备)**;不按 OSDI 倒推系统。
> 4. **人力有限**:论文必须是系统改进的副产品 —— 每个论文组件都对应 repo 里已预留的工程缺口。
> 本文取代 `07` 中的论文组合建议(07 保留作调研档案)。

---

## 1. 对 OpenAI 讨论的重审

### 1.1 认同并采纳

| OpenAI 讨论的结论 | 采纳后的动作 |
|---|---|
| completion window 是**合同/约束**,不是 novelty 本身;headline 不能是 "deadline scheduling"(撞 Jockey/Morpheus/3Sigma/Can't-Be-Late/SkyNomad) | window 降为**约束层**,不做标题 |
| **checkpoint 训练类比被你自己证伪**:短请求下 per-request commit 平凡,"exactly-once visible output" 是语义地基不是创新 | 容错从"三支柱之一"**降级为 substrate**(一段 + 一个 ablation);只有 trace 显示重尾请求/大 KV/高回收时才展开成"adaptive progress granularity"小机制 |
| 机制/策略分层纪律(L0 语义 / L1 状态 / L2 机制 / L3 策略),别把策略包装成机制 | 新论文核心 = L2 机制(productive profiling + 在线校准),L3(window planner)明示为 policy |
| 那场讨论真正的增量产出 = **productive profiling**(batch 自己当自己的 benchmark) | **升格为论文核心机制**(见 §2) |

### 1.2 纠正与补充(OpenAI 线程缺失、我此前已验证的)

1. **SageServe 缺席是个洞**。OpenAI 线程从头到尾没提 SageServe(arXiv 2502.14617, Microsoft):two-level ILP + 小时级 deadline + 异构(formulation 里)for LLM at DC scale。任何"deadline+异构+成本"claim 必须先过它。可辩护 delta = **多 provider 外部容量获取**(SageServe 单 CSP、spot 是内部 surplus donation)+ **在线校准**(SageServe 是 forecast + 静态 profile)+ **引擎级放置**(它是 VM 路由)。
2. **Mélange 就在自己 repo 里**(`gpu_optimizer/optimizer/solver/melange/`)。这既是必须 self-cite 的前作,也是**完美的内部 baseline**:静态离线 profile + ILP、面向 online serving 延迟 SLO。新论文 = 把知识层从"离线静态"换成"在线 productive",场景从 serving 换到 batch+window+多云。这是优势不是威胁——审稿人最信"作者打赢了自己的旧方案"。
3. **temporal P/D 成文消耗了 KV 迁移的 novelty**:我此前验证的"推理原生跨区 checkpoint (#1)"不再可 claim。新论文的容错只谈 ledger/幂等/重跑,不谈 KV 级迁移。
4. **我此前三点方案的修正**:① completion window optimizer → 保留为**决策层**;② 跨硬件 profiling → **升级为核心**,但从"静态知识层"变为 **productive profiling 在线校准**;③ 容错 → **降级为 substrate**。

### 1.3 cost-study blog(2026-06-22)的双重角色

- **作为种子**:已有实测($0.10/1M tok on H100/Qwen3.6-27B;开源自托管 vs API 5–35×;20K 请求 $1.7 vs $16),C1 characterization 有了地基。
- **作为动机(更重要)**:blog 的方法论恰好是论文要攻击的对象——**单一 shape(800-in/150-out)、手工 benchmark、静态单时点报价**。真实 batch 是 shape 异构的、价格/容量在漂移、驱动/引擎版本在变 → 静态 profile 必然过期。**"我们自己做 cost study 都得手工重测"就是最诚实的 motivation。**

---

## 2. 收敛后的论文

### 2.1 Thesis(一句话)

> **Batch 有两个结构性事实:全量 workload 前置可见 + 小时级完成窗口。它们让控制面可以"边干边学"成本面——按 shape 分层抽样,把真实请求的执行本身当作测量(零浪费),在线校准每个 (provider × GPU × config) 的吞吐/成本模型,并在窗口约束下持续重规划剩余工作;slack 不足时回退 reserved 保窗口。**

对比对象:离线 benchmark 矩阵(贵、跨 provider/驱动/引擎版本过期)与模拟器(跨异构环境失准)。

### 2.2 CARD

- **C(问题)**:batch LLM 的 $/token 强依赖 (workload shape × GPU × provider × 引擎配置),且 neocloud 容量/价格动态漂移;用户只有一个合同——完成窗口。
- **A(为什么现有不行)**:静态 profile+ILP(Mélange/SageServe/自家 gpu_optimizer)假设 profile 准确且不过期;通用云配置搜索(CherryPick/Ernest)烧的是**非生产性** benchmark;deadline-spot 系统(Can't-Be-Late/SkyNomad)把 job 当不透明 blob,不懂 shape 依赖,也不学吞吐。
- **R(洞察)**:batch 独有的"全量可见 + 松窗口"使 **profiling 可以是生产性的**——最先执行的请求既是进度也是测量;窗口给了探索预算,也给了兜底红线。
- **D(系统)**:AIBrix Batch 上的三个组件 + 一个地基(§2.3)。

### 2.3 贡献结构

| # | 贡献 | 层级 | 内容 |
|---|---|---|---|
| **C1** | Characterization | 测量 | shape × GPU × provider 的 $/token 矩阵(blog 单点 → 矩阵);neocloud 容量/价格漂移时间序列;静态 profile 的过期速率(同一配置随驱动/引擎/时间的误差增长) |
| **C2** | **Productive profiling(核心机制)** | L2 机制 | 按 shape 分层抽样 → 把 probe 切片分配到候选 target 真实执行(计入完成度,浪费≈0)→ 在线校准吞吐/成本模型(含输出长度不确定性的乐观/悲观界)→ 置信度驱动的 target 晋升/淘汰 |
| **C3** | Completion-window planner | L3 策略 | 校准模型 + 实时容量/价格 → 剩余工作的放置/并行度/供给计划;漂移/抢占触发重规划;latest-safe-start + reserved 兜底**保证窗口** |
| **C4** | 可靠性地基(不做 headline) | L1 状态 | request ledger + `custom_id` 幂等提交 + 便宜重跑,使 spot/preemptible 可入池;一段正文 + 一个 ablation |

### 2.4 与最接近工作的 delta(已对抗式查新验证,按威胁度排)

| 前作 | 它做什么 | 新论文的 delta(活下来的差异) |
|---|---|---|
| ⚠️ **Task-Sampling for Cluster Job Scheduling** (arXiv 2108.10464) | **最近的结构性祖先,此前所有讨论都没发现它**:采样一小部分**真实任务**执行(productive!),学习该 job 的 runtime 分布,再调度其余任务("learning in space") | 它是**同构单集群**、只决定 *when* 不选 *where*;只优化 JCT,无 $/deadline;**无分层**(同构算子的偏斜 ≠ LLM 的 shape 异质);非 LLM。我们的 delta = 异构多目标选择(provider×GPU×config)+ shape 分层 + $×窗口耦合 |
| **SageServe** (2502.14617) | 单 CSP、VM 级、**traffic-forecast**+ILP、在线混合 SLA | 多 provider 外部容量获取;**用本 job 的 productive probe 在线校准**取代历史流量预测;deadline-bound 有限 batch 而非持续 serving;引擎级放置 |
| **SkyNomad** (2601.06520) / **Can't-Be-Late** (NSDI'24) | deadline+spot batch;SkyNomad 有"轻量探测 + reserved 兜底" | **关键区分:SkyNomad 探的是"可用性"**(submit-and-cancel 二值信号,Spot-and-Scoot 2604.16457 同源),**不是"本负载在此硬件上的吞吐"**;job 为不透明 blob、进度率已知;无 shape/输出长度内生不确定;Can't-Be-Late 只决定单目标 spot↔OD 的 *when* |
| **CherryPick** (NSDI'17) | BO 搜云配置,probe 是**代理 run**(子采样数据/短跑),产出的是计时不是交付物;靠 **recurring** job 摊销探索成本 | 我们的 probe 是**交付物本身(零浪费)**——一次性 24h batch 没有 recurrence 可摊销,**零浪费是 load-bearing 而非锦上添花**;持续校准而非一次性搜索;多 provider $ 动态 |
| **Mélange** (2404.14527) / 自家 gpu_optimizer | 离线 profile + bin-packing,online serving 延迟 SLO,单 deployment | 在线 productive 校准;batch+window;多 provider。**内部 baseline(自家 melange 求解器)** |
| **2512.20967**(deadline-aware fine-tuning on spot) | 训练场景的 spot+OD deadline 分配,有正式 online-algorithm 分析 | 推理非训练;多 provider/SKU 异构(它单市场);吞吐靠 productive 校准而非价格/可用性预测。**C3 最近的概念类比,必须正面 engage 其理论框架** |
| **Vidur** (MLSys'24) | LLM 性能模拟器(<5% 误差) | **当 pre-planning oracle 引入而非对手**;我们补"跨 provider/驱动/版本的实测 ground truth" |
| **Ernest** (NSDI'16) | 实验设计采样→预测 Spark 伸缩 | **为我们所用**:其最小样本实验设计理论直接为分层 probe 配额辩护 |
| **Morphling** (SoCC'21) | 跨服务 meta-prior + 少样本适配 | 诚实 limitation:我们 per-job 校准丢弃了跨 job 先验 → future work |
| **AQP / eddies** (SIGMOD'00) | "查询自己的 tuple 教优化器",完全 productive——**最深的概念根,必引** | 单引擎算子计划 vs 跨 provider GPU 市场 + $ + deadline + LLM shape |
| **HyGen/ConServe/Valve** | 共置收割 | cite-not-compete(不做共置,决策 D-B/D-E);若未来用被收割容量,引用其抢占机制而非重造 |
| **自家 temporal P/D 论文** | P/D 时间解耦 + KV staging | **不 re-claim**;作为可选执行机制引用 |

### 2.5 关键实验与"杀手图"

Baselines:静态 profile+ILP(自家 melange 求解器)/ oracle profile(上界)/ CherryPick 式非生产搜索 / 单 provider / uniform 放置。
指标:$/job、窗口命中率、模型误差随时间、probe 开销(≈0 vs 独立 benchmark 成本)、对价格/容量漂移与输出长度重尾的鲁棒性。

1. **静态 profile 过期曲线**:同一 (模型,GPU) 的吞吐预测误差随引擎版本/驱动/provider/时间漂移增长 → 离线矩阵不可持续(C1 的立论图;注意 2502.00722 已做静态异构成本测量,**C1 必须靠漂移时间序列差异化**)。
2. **零浪费 ablation(C2 立论图)**:同样的分层 probe,"计入完成度" vs "纯开销(CherryPick 式)"对比 $ 与 deadline——量化一次性(无 recurrence 可摊销)场景下零浪费买到了什么。
3. **分层 vs i.i.d. probe ablation**:重尾输出/长上下文尾部下,无分层采样漏测 rare-but-costly shape → 校准偏差 → 错误放置;分层修正之。
4. **可用性风险 ≠ 吞吐风险(打 SkyNomad 的图)**:构造 spot **可用**(SkyNomad 不会兜底)但本 shape-mix 的校准吞吐**仍会 miss 窗口**的场景,展示我们提前降级 reserved——把两种风险实证解耦。
5. **cost-vs-window frontier**:各策略在"窗口命中 ∧ 最低 $"平面上的位置;ours 逼近 oracle(C3 主图)。
6. **漂移/抢占注入**:价格跳变、容量消失、输出长度重尾下,重规划把窗口命中拉回 100% 而静态方案 miss 或超支;含"故意注入有偏早期估计仍靠重规划+兜底保窗口"的鲁棒性实验。

### 2.6 系统落点 = 已预留的工程缺口(人力有限约束成立)

| 论文组件 | 工程缺口(本 clone 可见的 seam;你们 workspace 已推进一部分) |
|---|---|
| C1 矩阵 + 漂移序列 | catalog `ListPricingPredictions/ListResourcePredictions` **已声明未实现**(`catalog/interface.go`);RunPod/Lambda catalog 待填 |
| C2 采样/校准 | 新增 `batch/optimizer/{sampler,calibrator}`;`scheduler.py` 的 `SchedulePolicy` 扩展位(:39/:190 TODO) |
| C3 planner | planner `Schedule()` 的 capacity-aware hook(`backend.go` TODO 注明);**非 24h completion window 目前 "accepted but deferred"**(`registry.py:207/229` 的 deferred-fields 模式)→ 放开它本身就是产品 feature |
| C4 ledger | `job_manager` 的 `_request_progress_bits` 已是雏形 → 持久化 + 幂等提交 |
| 多 provider | 你们 workspace 已有 `provider/{runpod,lambdacloud}` 与 `job_driver/runtime/`(本 clone 尚无)→ 即 P0 的延续 |

### 2.7 Novelty 判决与审稿反驳(两轮对抗式查新已完成)

**判决:部分新颖——合取无人占。** 没有任何已发表工作同时具备:全量前置可见 → shape 分层 → **productive(零浪费)probe** → 在线校准 per-(provider×GPU×config) 吞吐/成本模型(含不确定性界)→ 窗口约束下持续重规划 → 跨 provider(reserved+neocloud+spot)→ reserved 兜底。每个单点都有先例(§2.4),合取没有。

**Minimal claim(直接可用于 intro)**:
> *"A batch LLM inference control plane can convert a known-upfront, deadline-bound workload's own request-shape strata into zero-waste probes — every probe result is a delivered response — to calibrate per-(provider×GPU×config) throughput/cost models online and continuously replan the remaining requests; a capability unavailable to online-serving systems (no future workload visibility) and unused by prior cloud-config-search work (which pays for probing via proxy/offline runs)."*

**审稿反驳清单(合并两轮查新,按危险度排;每条配 defusal 实验)**:
1. *"就是 Can't-Be-Late/SkyNomad 贴 LLM 标签"* → 把 Can't-Be-Late switch policy 实装为单目标退化基线;§2.5-4 的"可用性≠吞吐"图正面拆 SkyNomad。
2. *"SageServe 已做 deadline+异构 ILP"* → forecast-only ILP 限单 provider 作基线;漂移下静态 forecast 失效而在线校准纠正。
3. *"Task-Sampling (2108.10464) 已做 productive 采样"* → 强调它同构单集群/JCT-only/无分层/非 LLM;用 §2.5-3 分层 ablation 展示 shape 异质使无分层失效。
4. *"CherryPick/Ernest/AQP 换皮"* → §2.5-2 零浪费 ablation(一次性场景无 recurrence 可摊销,零浪费 load-bearing);AQP 无外部异构市场/无 $/deadline;reserved 兜底(市场风险对冲)无 AQP 类比。
5. *"多 provider 是工程不是研究"* → 消融隔离"校准算法 + 窗口保证机制"于连接器层之外(合成 provider 上复跑);给窗口保证性质的 proof sketch。
6. *"neocloud 价格波动不可复现"* → 发布带时间戳的价格/容量 trace,trace-driven replay,多历史窗口重复 + 波动敏感性图。
7. *"分层抽样 trivial / 有偏"* → 展示这是**统计×调度联合问题**(校准最优配额 ≠ 零浪费约束下的配额,调度器须在线调和二者);乐观/悲观界 + 置信晋升 + 兜底把"估不准"转化为"多花点 reserved",不 miss 窗口。

**Freshness 风险登记册(持续盯,尽快 arXiv 占旗)**:
- **Demystifying Cost-Efficiency over Heterogeneous GPUs (2502.00722)** —— 直接压 C1 的静态部分;C1 必须以**漂移时间序列 + profile 过期速率**差异化。
- **ShuntServe (2606.18600)**:spot+异构 LLM serving(单集群、无 deadline)——中威胁。
- **Coral (2605.04357)**、**Scheduling the Unschedulable (2604.06970)**:相邻但分别是 online serving / 请求级调度——中低。
- **Budgeted Multi-Objective Bandits for LLM Config Evaluation (2608.04333)**:方法论近邻(预算化配置评估的 bandit/regret 框架)——**当 related-work 锚点用**,也是把 probe 配额做成算法结果的理论参照。
- **SkyNomad camera-ready / SageServe v3**:若前者加吞吐感知、后者加多 provider,delta 收窄。

**两个抬高上限的建议(查新给出的)**:
1. 把 **"分层 probe 配额分配 × 零浪费约束 × deadline"** 发展成一个真正的算法结果(定理或 regret 界)——这是从"系统+测量"升到"系统+算法"的关键一步,2608.04333 是理论对话对象。
2. C1 的"profiles go stale"测量是整篇的证据地基,趁早开始采集(价格/容量/吞吐三条时间序列)。

**Venue 校准(比此前更准)**:**SoCC / ATC 最贴**(云资源经济学 + 测量支撑的机制,CherryPick/Selecta/Can't-Be-Late 一脉);**EuroSys 可行但需真实多云部署**;**ASPLOS 是 stretch**(这是调度/控制面/测量贡献,非体系结构协同设计)——除非把建议 1 的算法核做硬。

---

## 3. 一页纸结论

**写一篇,不是三篇**:`Productive Profiling for Deadline-Bounded Batch LLM Inference on Heterogeneous Clouds`(工作题)。
- 你的三点收敛为:**② profiling = 核心机制(升级为 productive)、① window optimizer = 决策层、③ 容错 = 地基**。
- temporal P/D、共置、KV 迁移全部出局(已成文/已降级/已消耗)。
- blog 是 motivation + C1 种子;melange 是内部 baseline;所有组件都是 repo 已预留的 TODO。
- novelty 判决:**部分新颖,合取无人占**;最近祖先 = Task-Sampling (2108.10464),已列 defusal。
- venue:**SoCC / EuroSys 主投,ATC 备**;ASPLOS 仅当把"分层 probe 配额 × 零浪费 × deadline"做成硬算法核才够得着。工程每一步同时是产品改进。
