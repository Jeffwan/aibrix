# ① 从 10 篇 paper 提炼的 5 类突破口

> 目标:看清楚哪些**高级能力**能让 AIBrix Batch 具备竞争力,而不是一个直白的 OpenAI Batch 实现。
> 每一类都回答三件事:**机制是什么** / **AIBrix 已有什么可以接** / **净新增 + 不该照搬什么**。
> 标签:💰 降单价 · ⚡ 弹性算力 · 🚀 提吞吐 · 🆕 新产品面

先放一个判断:**这 10 篇里,真正的差异化几乎全在数据面/资源面**。今天 AIBrix batch 的数据面
等于「原生 vLLM + 逐条回放」(`scheduler.py:256` 明确写了"executing unit is one request";
`inference_client.py:60` 就是 `httpx.post`),所以这些 paper 对我们几乎都是**净增量**,而不是
"我们已经做了一点点"。这既是差距,也是机会面。

---

## 家族 A — Offline 感知调度 (🚀 → 💰)

**代表:** BatchLLM (MLSys'26, Microsoft) · BlendServe (ASPLOS'26, Berkeley Sky)

### 核心机制
两篇都吃「**整批已知 + 松 SLO**」这个 offline 独有的红利,做在线引擎做不了的**全局重排**:

- **BatchLLM = 全局 prefix 共享 + 显存预算成批。**
  - *提前(ahead-of-time)* 对整批 prompt 建 radix 树,用 DP 把多层 prefix 合并成单层组
    (合并判据 `(leaves-1)·tokens(grandchild) > tokens(child)`),拿到 ~99% 的最优复用,预处理开销 <0.01%。
  - **按组调度**:共享同一 prefix 的请求一起发,prefix KV 算一次、整组生命周期内常驻、用完释放
    —— 直接消灭 vLLM/SGLang 那种「LRU 把长 prefix 提前淘汰 → 重算」的浪费。
  - **显存预算成批**:用 KV 显存上限 `M_threshold` 取代 vLLM 的 256-请求计数上限,消掉每迭代的吞吐"山谷"。
  - 真实业务负载 **1.26–1.30×** vs vLLM-best,KV 复用 58% vs 36%。(16K-prefix 那个 10.8× 是 cherry-pick,**不要拿去对外宣传**。)
- **BlendServe = 资源感知"混排"。**
  - 给每个请求算**计算密度** ρ = 计算时间/访存时间;输出越长越偏访存受限。
  - 把 prefix 树节点**按密度排序**(计算密集左、访存密集右),再用**双扫描器 (dual-scanner)** 从两端同时取,
    动态切分显存 `M_L/M_R`,凑出"计算+访存都打满"的混合批;同时保留 >97% 的 prefix 复用。
  - 单 A100 上 **1.44×** vs vLLM/SGLang,达最优的 86.5%。输出长度靠 ~1% 采样预测。

### AIBrix 已有什么可以接
- **分布式 prefix cache / KVCache V1 connector** (v0.3/v0.4):重排后的请求序列喂给它,prefix 复用直接兑现。
- **`SchedulePolicy` 已经预留了扩展位** (`scheduler.py:39,190` 明确 TODO 抽象化)。

### 净新增 + 借鉴落点
- 一个**控制面 batch optimizer pass**:job 提交后构 prefix 树 + 资源感知重排 → 产出有序/分组的请求流
  + prefix manifest。这是 `O(N log N)`、stateless、按 job 并行的纯控制面操作。 💰🚀
- 数据面:把 prefix 分组作为**调度 hint** 传给引擎,并把成批判据从"请求计数"换成"显存预算"
  (天然适配异构/spot 节点的不同 VRAM)。 ⚡
- 落点:新增 `batch/optimizer/`,在 job_driver 提交前介入;`M_threshold` 按节点可用 VRAM 动态设。

### ⚠️ 不该照搬
- BatchLLM 的 Triton 融合 kernel 有 split-K bank-conflict、且 upstream PR 已死(KV 双重释放 bug)——**只借思路,kernel 用 FlashInfer/FlashAttention**。
- 两者都假设**输出长度同质**;混合任务(代码生成 + 摘要同批)要么先按任务切分,要么上 per-request 输出长度预测。
- 严格 offline-only:**只用于 batch 队列,不要碰在线路径**。

---

## 家族 B — 在线↔离线 colocation / GPU 收割 (⚡ → 💰) 【最贴合"弹性算力"】

**代表:** HyGen (NeurIPS'25, UIUC) · ConServe (Berkeley/UCLA) · Valve (生产系统, 8054 GPU, 2026)

这是**弹性算力的核心家族**:在线机队为峰值 over-provision,>70% 时间是空的;把 batch 当**填充料**
塞进空隙,GPU 的边际成本≈电费——这是能把价格打到 5 折以下的最大杠杆。三篇是一条**成熟度递进线**。

### 核心机制(由易到难)
- **HyGen = 延迟预算准入 (最易落地)。**
  每个迭代两段:先按策略装在线请求,再用剩余的**延迟预算 t** + 显存预算把 offline 请求"装进同一个批张量"
  (chunked prefill 混排)。准入闸是一个 **~18µs 的线性回归延迟预测器**;**SLO-aware profiler** 用二分查找
  离线标定出安全预算 `t*`(=按 SLO 档位标定,直接对应**定价档位**)。offline 队列用 **prefix-trie 排序 (PSM)** 提复用。
  整体 **3.87×**、offline **5.84×**,仅 ~1300 LOC on vLLM/Sarathi。
  **产品化金句:干扰容忍比 (interference tolerance) 就是一个价格旋钮**——经济档=高容忍,优先档=低容忍。
- **ConServe = 层级抢占 (更细、更狠)。**
  在 transformer 层间插 **safepoint**,监控线程发现在线请求要违反 TTFT 就置抢占位;worker 在层边界检查,
  把 offline 部分从该层起丢弃。端到端抢占 5.41ms(单卡)~13ms(TP4)。**增量 KV checkpoint**:每步只把
  *新生成的那一个 token* 的 KV 异步存到 host(常数大小,与序列长无关)——offline 状态"极易驱逐",
  非常契合 spot/被收割 GPU。harvest >70% 空闲,offline 吞吐 **2.2×**。
- **Valve = 生产级"有界抢占"(最该学的产品化范式)。**
  把抢占当**二维控制问题**联合有界:
  - **抢占延迟有界**:用 CUDA **channel disable** 触发硬件上下文保存 → 亚毫秒;一个**1 行驱动补丁**绕过多卡共享锁,把 8 卡抢占从 >5ms 压到 <1ms。
  - **抢占率有界**:`T_cool = 2×G`(G=实测迭代间隙)冷却期 → **每个在线请求最多被抢占一次**(而非每迭代)。
  - **显存**:子层粒度回收 + **MIAD 反馈控制器**(类 AIMD)调回收率 + 贪心选择"边际 token 成本最小"的 KV 驱逐(比 FIFO 省 22.9–40.1%)。
  - **集群放置模型** `吞吐 = P_compute × P_memory × P_multi`,多卡 job 要求所有卡 Jaccard 空闲重叠 ≥0.95。
  - 生产 8054 卡,集群利用率 **+34.6%**、省 **2170 卡**,在线 TTFT<5% / TPOT<2%,**仅 ~20 行框架补丁 + 1 行驱动**。

### AIBrix 已有什么可以接
- **负载感知路由 + mixed-workload routing** (v0.6):天然就是"把请求按工作负载特征分流到不同 pod"的位置。
- **autoscaler** + `BasicCongestionControl` 资源池(`scheduler.py:57`)+ `update_job_pool_size`(已留 TODO 接资源监控,`scheduler.py:97`)。
- **`FederalInferenceEngineClient`**(`inference_client.py:72`)已经会"把请求路由到一个空闲的已发现 endpoint 并故障转移"——这就是"把 batch 塞到在线 pod 池"的雏形(虽然粒度粗:每 endpoint 同时仅一在飞)。

### 净新增 + 借鉴落点
- **路线建议:先 HyGen 后 Valve。** v0.7.0 先做 HyGen 式 *迭代级* 准入(改动小、复用 vLLM 的 `_schedule()`),
  把 batch 请求经 gateway 路由进低负载的在线 pod;成熟后再上 Valve 式 *channel 抢占 + cooldown + MIAD* 做生产硬保障。 ⚡💰
- **价格/SLO 档位** = HyGen 的干扰容忍比 / Valve 的抢占率上限,落到 `BatchJobSpec.aibrix`(`batch_job.py:155` 已是扩展位)。
- **新增生命周期态** `PAUSED/PREEMPTED`(当前 `BatchJobState` 没有,`batch_job.py:47`)。
- 一个**节点级 colocation daemon (DaemonSet)** 订阅在线 SLO 信号,管 channel/准入;一个**放置控制器**用 Valve 三因子模型。

### ⚠️ 不该照搬
- **"同模型 + 单引擎"是 ConServe/HyGen 的硬假设,不要当公理**:公共 batch API 的 job 模型五花八门。AIBrix 需要在其上加**集群级、多模型**的路由与放置。
- ConServe >5000 LOC 引擎 fork 是采用风险;**Valve 的 ~20 行 + 1 行驱动**才是产品化范式——但那 1 行驱动补丁要 bare-metal/可改内核驱动,**托管云上拿不到多卡亚毫秒保证**(channel disable 仍可用,延迟放宽)。
- HyGen/ConServe 的延迟预测器/标定是**按硬件**的;异构机队要么按硬件维护系数,要么上 FLOP/token 归一化特征。
- 抢占保留 KV 到 CPU,长上下文(100K+)swap 可能吃掉 colocation 收益——要和家族 C 的 offload 一起设计。

---

## 家族 C — 内存层级 / 异构与商用硬件 (💰, 兼 ⚡) 【最贴合"价格对标"】

**代表:** NEO (MLSys'25) · Glinthawk (MIT/MSR, 2025) · PipeMax (SYSU, 2025)

共同思想:**解码注意力是访存受限的**,把"KV 存储 + attention 计算"从昂贵的 HBM 挪到便宜资源
(CPU / 商用 PCIe GPU),用流水线掩盖搬运。batch 的松 SLO 让"能挪多少挪多少"。

### 核心机制
- **NEO = CPU attention + KV 下沉 + 非对称流水。** 不只是把 KV 存 CPU(那样 PCIe 成瓶颈),而是把*一部分请求*
  的 attention **整个放 CPU 算**(自研 ISPC AVX2/512/NEON kernel)——因为 CPU/GPU 的*带宽*差距(~1:3,Graviton4 近 1:1.1)
  远小于*算力*差距。GPU 线性层与 CPU attention 在两个子批上重叠;每层 KV 流水搬运不上关键路径。
  T4 上 **+6.6×**,A10G +6.4%~79.3%(取决于 CPU 带宽),H100 +14%。**batch 的关键:松 SLO 让你能比在线多卸载得多 → 直接降单价。**
- **Glinthawk = 两层 attention 解耦。** Tier-1(GPU)只做非 attention 矩阵乘(权重常驻);Tier-2(便宜、大内存 CPU 节点)
  存全部 KV 并算 attention。层间只需 **<50 Gbps**、容忍数十 ms 延迟(普通以太网够)。**5.9× 吞吐 / 2.8× 成本**
  (长序列 16.3× / 2.4×);自带**仿真式配置优化器**(K/K′/B)。
- **PipeMax = 商用 PCIe 机的 PP + KV offload。** 在无 NVLink 的 8×RTX5090/L20 上,利用**流水并行天然的"空闲 KV 窗口"**
  把非活跃批的 KV 卸到 host;**block-first KV 布局**把 PCIe 带宽利用率从 ~30% 提到 ~90%;执行时间感知的预取调度。
  **最高 2.45×** vs vLLM-TP;消费级 GPU 每 FLOP 便宜 ~3×。

### AIBrix 已有什么可以接
- **KV offloading framework** (`docs/source/designs/aibrix-kvcache-offloading-framework.rst`) + **KVCache V1 connector** (v0.4)。
- **P/D 解耦 / StormService** (v0.4):Glinthawk 的两层、PipeMax 的 PP 都能复用 P/D 的编排骨架。
- **GPU Optimizer / 异构服务**(架构文档里已有"heterogeneous serving"概念)。

### 净新增 + 借鉴落点
- **batch 专属的异构/商用节点池**:把 8×RTX5090、CPU-rich 节点登记为**一等 batch worker**;节点类型(NVLink vs PCIe vs CPU)成为调度维度。 💰⚡
- **CPU/attention 下沉执行**:作为引擎的 attention backend 插件(NEO 思路)或两层 dispatcher(Glinthawk 思路)。
- **成本感知 planner**:Glinthawk 的仿真器思路——给定异构机队,解出"达到目标 $/token 的最优节点配比"。这也喂家族 D。 💰
- 落点:`ModelDeploymentTemplate` 的 accelerator/parallelism schema 已能描述 tp/pp/dp/ep 与 SKU(samples/batch),扩成异构池 + offload 开关。

### ⚠️ 不该照搬
- **都是 offline-only、且要 profiling**;对在线路径无效。
- Glinthawk Tier-2 是**有状态**的(KV 在上面),spot 收割需要 KV checkpoint/restore(和家族 D 合流)。
- 自研 CPU kernel(NEO)是长期维护负担;PCIe 在 70B+ 是硬上限,要先 profile。
- 量化(INT4/8 KV)会缩小这些方法的相对收益——要把量化作为基线一起评。

---

## 家族 D — 跨区/跨云 spot (Sky Computing) (💰⚡) 【正面对标 SkyPilot】

**代表:** SkyNomad (Berkeley Sky, arXiv 2601.06520, 投 OSDI'26;承接 NSDI'24 "Can't Be Late")

### 核心机制
面向**有 deadline 的 AI batch job**(训练/批量推理/分析),吃**跨区 spot 的时空异质性**
(同型号 GPU 跨区价差最高 5×、可用性差异巨大、egress 价差 7×、≥4 区时同时被抢占变罕见):

- **统一货币成本模型**:每个候选区一个效用 `U_s = V(t)·η_s − C − E/L̄`(进度价值×冷启动有效性 − 算力费 − egress 按预测寿命摊销),
  把"便宜+可用+赶 deadline"塌缩成一个可比数;超过当前区 Δ 阈值才迁移(滞回防抖)。
- **生存分析预测 spot 寿命**(Nelson-Aalen 累积风险 + 波动倍子 γ),与 oracle 差 <5%,选区重合 95–99%。
- **轻量探活**:每 2h 真起一台立即销毁来探可用性,$1–3/job。
- **两段流水跨区迁移 + deadline 安全网**:provisioning 时并行拷 checkpoint;`剩余 slack < 剩余工作 + 2d` 时强制回退 on-demand。
- 真实部署省 **1.25–3.96×**;仿真内 oracle 10–12%。

### 与 SkyPilot 的关系(这就是对打点)
SkyNomad **建在 SkyPilot 之上(~6k LOC)**;它打的 baseline `UP(S)` 就是 **SkyPilot 生产 failover**(被动、不预测、不主动迁移、不算迁移成本)。
SkyNomad 比它好 **44%(A100)~ 4.6×(H100 仿真)**。

| 能力 | SkyPilot managed-spot | SkyNomad |
|---|---|---|
| 多区 spot | 抢占后顺序 failover | 持续**主动**再平衡 |
| 抢占预测 | 无 | 生存分析 + 波动检测 |
| 迁移成本感知 | 无 | egress 按预测寿命摊销 |
| 选区指标 | 最便宜可用 | 统一效用(价格+可用+迁移+deadline) |

### AIBrix 能做得比 SkyPilot/SkyNomad 更好的地方 —— **推理原生**(护城河)
- **按"请求队列 + prefix-KV"粒度 checkpoint**,而非整机:SkyNomad 整机 checkpoint 几百 GB、~6 分钟冷启动;
  AIBrix 只需存待处理请求队列(token id + 元数据,KB 级)+ 在飞 prompt 的 prefix KV(至多 GB)→ **秒级**恢复,不重算 prefill。
  (这就是 **SpotServe** 在*单区在线*做的 token 级 KV save/restore,AIBrix 把它**泛化到跨区 batch**。) 💰🚀
- **以解耦 P/D 单元为迁移粒度**:把*状态小的 decode worker* 迁到便宜 spot 区,prefill 稳定——SkyNomad 把整 job 当原子,做不到。 ⚡
- **两级融合**:先在本区**收割在线机队**(家族 B),本区收割耗尽 + 本区 spot 也贵时才触发 SkyNomad 式跨区——SkyPilot/SkyNomad 都没有这一层。 🆕⚡
- **队列感知安全网**:用"剩余工作量 + egress ≤ deadline slack"(我们知道队列深度和 per-request 处理时间),比固定时间公式更省 on-demand。 💰

### 净新增 + 借鉴落点
- 一个 **batch 跨区 spot 放置控制器**(K8s controller):跑 SkyNomad 成本模型,迁移单位是**推理 job group / P-D 单元**。
- **探活 CronJob** + spot 可用性库(云无关),同时喂 batch 放置和在线容量规划。
- 新增态 `MIGRATING`;checkpoint 走 object store(已有 storage 抽象 S3/TOS/GCS)。

### ⚠️ 不该照搬
- 整机冷启动(6min):AIBrix 用**预烤镜像 + 权重持久卷/远端 model store**,冷启动只剩起机时间(秒~分钟)。
- 单卡被抢占就 gang 抢占整组:AIBrix 应支持**降并行度部分恢复**。
- SkyNomad 固定实例组、需事先给执行时长 P:batch 用**队列进度**而非单一 P 估计;并补**弹性伸缩**(SkyNomad 自己列为 future work,正好我们填)。
- 真·跨云(AWS+GCP+Azure 同时)有 API/网络/peering 复杂度,先单云多区,跨云审慎。

---

## 家族 E — Agentic / workflow batch (🆕💰🚀) 【新产品面】

**代表:** Halo (PVLDB, NUS) + Helium (SIGMOD'26, 同组扩展)

### 核心机制
把**一批 agentic workflow(多步、含工具调用的 DAG)当作一个查询**来优化:
- **GraphSpec IR**:把 workflow 编译成带类型节点(LLM 算子 / Tool 算子)的 DAG,工具调用从 prompt 里抽成一等可调度节点。
- **成本感知 DP 放置器**:把"哪个 DAG 节点放哪个 GPU worker"建成依赖感知放置问题,DP 求解(比 MILP 快 **2322×**、近最优);成本含"模型切换=0 若已驻留、有效 prefill=全量−已缓存前缀"。
- **KV 血缘亲和路由**:把 DAG 子节点路由到**持有其父 KV** 的 worker;支持 KV 迁移。
- **工具调用合并 (CSE)**:规范化签名后合并相同的待执行工具调用(去掉它会 +154% 延迟,单项贡献最大)。
- 比 naive 最高 **18.6×**,比 vLLM+LMCache **1.1–2.1×**,省 **2× GPU-秒**。

### AIBrix 已有什么可以接 / 净新增
- 接:gateway 路由、分布式 prefix cache(KV 血缘路由就是在 prefix cache 上加"会话/血缘亲和")。
- 新增产品面:`POST /v1/workflow-batch`,body = `workflow_spec`(DAG)+ `queries[]`。用户提交"在 1 万篇文档上跑这个 5 步 RAG"为**一个 job**,而不是客户端编排上万次调用。 🆕
- 控制面 **DAG planner** + 数据面 **KV 血缘路由** + **tool 去重缓存中间件**。

### ⚠️ 不该照搬
- **只支持静态 DAG**:真实 ReAct/条件分支 agent 是动态拓扑 —— 当作研究 gap,别宣称已解决。
- 要求 YAML/GraphSpec 是开发者负担:实际产品要能从 LangGraph/DSPy 自动抽取,或限定内置模板。
- 只做语法级精确去重;近似/模板级共享看 Helium(Templated Radix Tree)。
- benchmark 是合成 7–10 节点 DAG;对外用 **1.1–2.1×(vs 已开 prefix cache 的 vLLM)** 这个诚实的下界。

---

## 跨家族的两个总结判断

1. **batch 是落地数据面创新的最佳载体。** A/C/E 严格 offline-only,B/D 要"能随时被打断/迁移"——
   这些前提**只有 batch 的松 SLO 才安全满足**。所以把这些能力先在 batch 落地,既是差异化,也是给在线路径试水。
2. **成本下降是可叠加的。** A(吞吐↑)× B(边际成本算力)× C(便宜硬件)× D(跨区 spot)在 $/token 上是**乘性叠加**的:
   `$/tok = 实例$每小时 / (吞吐 token每小时)`,A/C 抬分母,B/C/D 压分子。这就是为什么 AIBrix 能可信地走到"5 折以下"。

---

## 附录:10 篇 paper 速查表

| Paper | 出处 | 家族 | 一句话机制 | 头条数字(诚实口径) | 关键前提 / 不要照搬 |
|---|---|---|---|---|---|
| **BatchLLM** | MLSys'26, Microsoft | A | 全局 prefix 树(AOT+DP)分组 + 显存预算成批 | 真实业务 **1.26–1.30×** vs vLLM-best(10.8× 是 cherry-pick) | offline-only;输出长度同质;Triton kernel 别照搬 |
| **BlendServe** | ASPLOS'26, Berkeley | A | 计算密度排序的 prefix 树 + 双扫描器混排 | **1.44×** vs vLLM/SGLang,达最优 86.5% | 整批已知;单 GPU 验证;输出长度靠 1% 采样 |
| **NEO** | MLSys'25, UCB/UCD | C | CPU 跑 attention + KV 下沉 + 非对称流水 | T4 **+6.6×**;A10G 视 CPU 带宽 +6~79% | 受 CPU 带宽/PCIe 约束;自研 CPU kernel 维护重 |
| **HyGen** | NeurIPS'25, UIUC | B | 延迟预算准入把 offline 当填充料 + PSM | 整体 **3.87×**/offline 5.84×,~1300 LOC | 单模型/单实例;预测器按硬件;无集群调度 |
| **ConServe** | arXiv 2410, UCB/UCLA | B | 层级 safepoint 抢占 + 增量单 token KV checkpoint | offline **2.2×**,harvest >70%,抢占 5–13ms | 同模型/单引擎;>5000 LOC fork;长 prefill 抢不动 |
| **Valve** | arXiv 2604, 生产 8054 GPU | B | channel 亚毫秒抢占 + cooldown 限率 + MIAD | 利用率 **+34.6%**/省 2170 卡,TTFT<5%,~20 LOC+1 行驱动 | 1 行驱动需可改内核;MIAD 参数要调;软 SLO |
| **Glinthawk** | arXiv 2501, MIT/MSR | C | 两层:GPU 算矩阵乘 / CPU 存 KV+算 attention | **5.9× 吞吐 / 2.8× 成本**(长序列 16.3×/2.4×) | offline-only;Tier-2 有状态难迁;未量化 |
| **PipeMax** | arXiv 2605, SYSU | C | 商用 PCIe 机 PP + KV offload + block-first 布局 | **2.45×** vs vLLM-TP;消费 GPU 每 FLOP 便宜 ~3× | offline;闭批;单机 8 卡;同构 |
| **Halo / Helium** | PVLDB / SIGMOD'26, NUS | E | 一批 workflow DAG 当查询:DP 放置 + KV 血缘 + 工具 CSE | naive **18.6×** / vLLM+LMCache 1.1–2.1×,省 2× GPU-秒 | 静态 DAG;需 GraphSpec;合成负载 |
| **SkyNomad** | arXiv 2601, Berkeley Sky (OSDI'26 投) | D | 统一成本模型主动跨区迁 spot + 生存分析预测 | 真实省 **1.25–3.96×**;比 SkyPilot-UP 好 44%~4.6× | 需 checkpoint;整机冷启动 6min;gang 抢占;固定实例组 |

> 完整精读(每篇含作者/venue/URL、机制细节、量化表、适用前提、可借鉴落点、不该照搬)保存在调研归档中;
> 上表为决策用速查。对外材料引用数字时**一律用"诚实口径"那一列**,避免被人用 cherry-pick 反驳。
