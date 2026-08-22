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

### 2.4 与最接近工作的 delta(简表;agent 查新回来后细化)

| 前作 | 它做什么 | 新论文的 delta |
|---|---|---|
| **SageServe** (2502.14617) | 单 CSP、VM 级、forecast+ILP、deadline | 多 provider 外部容量获取;**在线校准**取代静态 profile;引擎级放置 |
| **Mélange** (2404.14527) / 自家 gpu_optimizer | 离线 profile + ILP,online serving 延迟 SLO | 在线 productive 校准;batch+window;多云。**内部 baseline** |
| **CherryPick** (NSDI'17) / **Ernest** (NSDI'16) | 云配置搜索/预测,跑**专门的** benchmark/样本 | probe = 真实请求、**计入完成度(零浪费)**;LLM shape 感知;deadline 耦合;持续校准而非一次性搜索 |
| **Vidur** (MLSys'24) | 模拟器预测 LLM 性能 | 实测而非仿真;跨 provider/驱动 ground truth;零额外成本 |
| **Can't-Be-Late** (NSDI'24) / **SkyNomad** (2601) | deadline-aware spot,job 为不透明 blob、大小已知 | job 大小内生(输出长度)在线修正;shape→吞吐依赖;多 provider 价格/容量联合 |
| **AQP(eddies/mid-query re-opt)** | "查询自己的执行教优化器"(概念祖先,必引) | 单引擎算子 vs 跨 provider GPU 市场 + $ + deadline + LLM shape |
| **HyGen/ConServe/Valve** | 共置收割 | cite-not-compete(我们不做共置,决策 D-B/D-E) |
| **自家 temporal P/D 论文** | P/D 时间解耦 + KV staging | **不 re-claim**;作为可选执行机制引用 |

### 2.5 关键实验与"杀手图"

Baselines:静态 profile+ILP(自家 melange 求解器)/ oracle profile(上界)/ CherryPick 式非生产搜索 / 单 provider / uniform 放置。
指标:$/job、窗口命中率、模型误差随时间、probe 开销(≈0 vs 独立 benchmark 成本)、对价格/容量漂移与输出长度重尾的鲁棒性。

1. **静态 profile 过期曲线**:同一 (模型,GPU) 的吞吐预测误差随引擎版本/驱动/provider/时间漂移增长 → 离线矩阵不可持续(C1 的立论图)。
2. **零浪费**:productive probing 的额外成本 ≈0 vs CherryPick 式 benchmark 的 $ 与时间(C2 的立论图)。
3. **cost-vs-window frontier**:各策略在"窗口命中 ∧ 最低 $"平面上的位置;ours 逼近 oracle(C3 主图)。
4. **漂移/抢占注入**:价格跳变、容量消失、输出长度重尾下,重规划把窗口命中拉回 100% 而静态方案 miss 或超支。

### 2.6 系统落点 = 已预留的工程缺口(人力有限约束成立)

| 论文组件 | 工程缺口(本 clone 可见的 seam;你们 workspace 已推进一部分) |
|---|---|
| C1 矩阵 + 漂移序列 | catalog `ListPricingPredictions/ListResourcePredictions` **已声明未实现**(`catalog/interface.go`);RunPod/Lambda catalog 待填 |
| C2 采样/校准 | 新增 `batch/optimizer/{sampler,calibrator}`;`scheduler.py` 的 `SchedulePolicy` 扩展位(:39/:190 TODO) |
| C3 planner | planner `Schedule()` 的 capacity-aware hook(`backend.go` TODO 注明);**非 24h completion window 目前 "accepted but deferred"**(`registry.py:207/229` 的 deferred-fields 模式)→ 放开它本身就是产品 feature |
| C4 ledger | `job_manager` 的 `_request_progress_bits` 已是雏形 → 持久化 + 幂等提交 |
| 多 provider | 你们 workspace 已有 `provider/{runpod,lambdacloud}` 与 `job_driver/runtime/`(本 clone 尚无)→ 即 P0 的延续 |

### 2.7 风险与待验证(agent 进行中)

- **头号 novelty 风险**:"productive profiling 是 CherryPick/AQP 换皮" → 防御靠三点:probe 生产性(零浪费)、LLM shape 依赖(prefill/decode/输出长度)、deadline 耦合(探索预算=slack)。对抗式查新 agent 正在验证,结论回来后更新本节。
- **SageServe 正面对比**不可回避(§2.4 第一行)。
- **可复现性 objection**(neocloud 价格波动)→ 记录价格时间序列 + trace-driven replay(价格作为输入回放)。
- **分层抽样偏差**(重尾输出把估计带偏)→ 乐观/悲观区间 + 置信度晋升 + reserved 兜底,把"估不准"转化为"多花一点 reserved",不 miss 窗口。

---

## 3. 一页纸结论

**写一篇,不是三篇**:`Productive Profiling for Deadline-Bounded Batch LLM Inference on Heterogeneous Clouds`(工作题)。
- 你的三点收敛为:**② profiling = 核心机制(升级为 productive)、① window optimizer = 决策层、③ 容错 = 地基**。
- temporal P/D、共置、KV 迁移全部出局(已成文/已降级/已消耗)。
- blog 是 motivation + C1 种子;melange 是内部 baseline;所有组件都是 repo 已预留的 TODO。
- venue:EuroSys/SoCC 主投;工程每一步同时是产品改进。
