# ⑧ A / D 收益量化 walk-through(用一个具体 batch 文件模拟)

> 目的:用**一个具体的 batch 输入文件 + 明确的 prompt 长度分布**,把 A(offline 感知)和 D(跨云 spot)的收益
> **一步步算出来,看清收益来自哪**,以评估「值不值得做 + 后期能不能落地」。
> **所有吞吐/价格均为示意值,标注来源或假设;落地时用实测替换**(这正是 Paper C / cost-calculator 的活)。
> 价格锚点(2026-05):OpenAI Batch 4o 档 $1.25/$5 每 1M(in/out);neocloud H100 on-demand ~$2.5/hr、spot ~$1.3/hr;超大厂 H100 on-demand ~$10/hr。

---

## 1. 具体 batch 输入文件 + 长度分布

一个真实味道的「企业隔夜 batch」,`N = 200,000` 条,model = 自托管 **Llama-3.1-70B**,OpenAI batch JSONL 格式。
三个 bucket(刻意覆盖"计算受限↔访存受限"光谱 + 含一个大共享前缀):

| Bucket | 占比 | 条数 | 共享前缀 tok | 唯一输入 tok | 输出 tok | 资源画像 |
|---|---|---|---|---|---|---|
| **B1 分类/抽取** | 60% | 120,000 | **1,800(全员共享:instruction+JSON schema+5-shot)** | 800(文档块) | 120(JSON 标签) | prefill 重、**计算受限** |
| **B2 摘要** | 25% | 50,000 | 150(短指令) | 6,000(唯一长文档) | 400 | prefill 极重、**几乎无共享** |
| **B3 推理/生成** | 15% | 30,000 | 400(共享 persona) | 200(短问题) | 2,000(长 CoT) | decode 重、**访存受限** |

**实际 JSONL 长这样**(共享前缀用 `<<...>>` 略写,真实是定长文本;关键是 B1 的 system 块**逐字相同**):

```jsonl
{"custom_id":"cls-000001","method":"POST","url":"/v1/chat/completions","body":{"model":"llama-3.1-70b-instruct","messages":[{"role":"system","content":"<<1,800-tok 固定: 抽取任务说明 + JSON schema + 5 个 few-shot 例子>>"},{"role":"user","content":"<<800-tok 文档块 A>>"}],"max_tokens":128,"temperature":0}}
{"custom_id":"cls-000002","method":"POST","url":"/v1/chat/completions","body":{"model":"llama-3.1-70b-instruct","messages":[{"role":"system","content":"<<同一 1,800-tok 固定块,逐字相同>>"},{"role":"user","content":"<<800-tok 文档块 B>>"}],"max_tokens":128,"temperature":0}}
{"custom_id":"sum-000001","method":"POST","url":"/v1/chat/completions","body":{"model":"llama-3.1-70b-instruct","messages":[{"role":"system","content":"<<150-tok: 用三句话总结>>"},{"role":"user","content":"<<6,000-tok 唯一长文档>>"}],"max_tokens":512,"temperature":0.3}}
{"custom_id":"rsn-000001","method":"POST","url":"/v1/chat/completions","body":{"model":"llama-3.1-70b-instruct","messages":[{"role":"system","content":"<<400-tok 固定 persona/CoT 指令>>"},{"role":"user","content":"<<200-tok 数学题>>"}],"max_tokens":2048,"temperature":0.7}}
```

## 2. token 账(全批)

| | prefill 总 | 其中**共享(可去重)** | 唯一 | 输出(decode) |
|---|---|---|---|---|
| B1 | 120k×2,600 = **312.0M** | 120k×1,800 = **216.0M** | 96.0M | 14.4M |
| B2 | 50k×6,150 = **307.5M** | 50k×150 = 7.5M | 300.0M | 20.0M |
| B3 | 30k×600 = **18.0M** | 30k×400 = 12.0M | 6.0M | 60.0M |
| **合计** | **637.5M** | **235.5M** | 402.0M | **94.4M** |

两个关键比例:**可去重共享前缀 = 235.5M / 637.5M = 37% 的 prefill**;**prefill : decode = 637.5M : 94.4M ≈ 6.75 : 1**(token 计)。

---

## 3. 家族 A 的收益:拆成 A1(prefix 共享)+ A2(混排),看清来自哪

> 先把基线说清楚:**现代引擎(vLLM 连续批 + chunked prefill + 自动前缀缓存 APC)已经做了一部分**——
> 所以 A 的收益是**在这个强基线之上的增量**,不是相对"傻乎乎逐条重算"。这点必须诚实,否则数字虚高。

**用 GPU 时间(不是 token)来谈吞吐更诚实**。示意速率(70B / 4×H100):prefill ~12,000 tok/s、decode ~3,000 tok/s。
- prefill 时间 = 637.5M / 12,000 = **53,125 s**(占 62.8%)
- decode 时间 = 94.4M / 3,000 = **31,467 s**(占 37.2%)
- 合计 ≈ **84,592 s = 23.5 节点-小时 = 94 H100-小时**(这是"算账基数")

### A1 — 全局 prefix 共享(BatchLLM):收益**几乎全来自 B1**
- 可去重共享 prefill = 235.5M,其中 **B1 占 216.0M(92%)**;B2 文档唯一(7.5M 可忽略),B3 仅 12M。
- 机制:把共享同一前缀的请求**成组**,前缀**算一次、整组复用**,而不是被 LRU 在大批量下提前淘汰再重算。
- **收益上界**(若基线把共享前缀反复重算):去掉 235.5M = prefill 的 37% → prefill 时间 53,125→33,500 s → 全批 **1.30×**。
- **现实收益**(相对已开 APC 的 vLLM):取决于 APC 驻留是否被其他 bucket 挤掉;BatchLLM 真实业务实测 **1.26–1.30×**。本例 B1 前缀 1,800-tok × 12 万条,共享度极高、最容易被挤,**偏上界一侧**。
- **一句话来源**:A1 的钱 = "B1 那 216M 共享前缀本来要重算多少次"。**前缀越长、共享越广、批越大、eviction 越凶 → A1 越值。**

### A2 — 资源混排(BlendServe):收益来自 prefill/decode 资源互补
- 本批 decode 时间占 37%(虽然 token 只占 13%,但 decode 慢)。**计算受限(B1/B2 prefill)与访存受限(B3 decode)同框**,可让 GPU 的计算单元与 HBM 带宽**同时被吃满**。
- 但基线的 chunked prefill **已经在混** prefill+decode;A2 是**在其之上**靠"资源感知排序"再榨残差 → BlendServe 实测 **1.2–1.44×**。
- **一句话来源**:A2 的钱 = "把 B3 的访存活塞进 B1/B2 计算活的资源空隙"。**计算/访存越均衡 → A2 越值;本批 prefill 偏重,A2 是次要项。**

### A 合计(本批,诚实区间)
- A1 与 A2 **机制不同、并非完全相乘**(A1 砍掉 prefill 计算,会缩小 A2 用来掩盖 decode 的"计算池")。
- **本批现实区间 ≈ 1.3–1.8× 吞吐 → $/token 降 ~25–45%**。**主要来自 A1(B1 的共享前缀)**,A2 为辅。

### 反例(A 几乎没用):
把这批换成**纯 B2 形态**(每条唯一长文档、无共享前缀、输出齐整)→ 可去重共享 ≈ 0、资源画像单一 → **A ≈ 1.0–1.05×**。
**A 的收益旋钮 = 共享前缀占比 × 资源画像方差。** 评测/分类/抽取/合成数据(大固定指令)→ 高;一次性唯一长文档 → 低。

---

## 4. 家族 D 的收益:成本阶梯 + spot 抢占模型

### 4.1 成本阶梯(本批 = 94 H100-小时,**未叠加 A**)
| 方案 | 单价(示意) | 本批成本 | vs OpenAI |
|---|---|---|---|
| **OpenAI Batch**(4o 档)| in $1.25 / out $5 每 1M | 637.5M×1.25 + 94.4M×5(每 1e6)= $797 + $472 = **$1,269** | 1× |
| 超大厂 H100 **on-demand** | ~$10/GPU-hr | 94×$10 = **$940** | 0.74× |
| **Neocloud** H100 on-demand | ~$2.5/GPU-hr | 94×$2.5 = **$235** | 0.19× |
| **Spot** H100 | ~$1.3/GPU-hr | 94×$1.3 = **$122** | **0.10×** |

**两个诚实结论**:
1. **在超大厂 on-demand 自托管,只比 OpenAI 便宜 ~1.35×**——光自托管、用贵机器,赢得不多。
2. **D 的真正价值在 sourcing**:on-demand→neocloud→spot = $940→$235→$122,即在自托管基础上**再省 4×→7.7×**;相对 OpenAI 到 **10.4×**。
3. **叠加 A**(GPU-小时 ~−30% → 94→~66 H100-hr):spot 成本 ~$86。**A×D 相乘**。

### 4.2 spot 抢占 / 迁移开销模型(D 的护城河在这)
开销% = `抢占率 λ(次/小时) × 单次恢复耗时(节点-小时)`,**与 job 大小无关**:

| 抢占率 λ | 整机 checkpoint(~6min=0.1hr)开销 | 推理原生(~5s≈0.0014hr)开销 |
|---|---|---|
| 0.25/hr(每 4h)| 2.5% | 0.035% |
| 0.5/hr(每 2h)| 5% | 0.07% |
| 1/hr | 10% | 0.14% |
| 2/hr(volatile 廉价区)| **20%** | **0.28%** |

**关键洞察:最便宜的 spot = 最 volatile 的 spot(λ 大)。** 整机 checkpoint 在 λ=2/hr 时浪费 20%,几乎把 spot 折扣吐回去;
**推理原生(请求队列+prefix-KV 级、秒级、不丢在飞、不重算 prefill)始终 <0.3%** → **敢吃最便宜最 volatile 的 spot**。
这就是相对 SkyPilot/SkyNomad(工作负载无关、整机)**唯一结构性的护城河**,也是 research #1 的落点。

**破平衡例**:若 volatile 区比稳定区便宜 30% 但 λ=2/hr → 整机净省 ≈ 30%−20% = 10%(还冒丢活风险);推理原生净省 ≈ 30%−0.3% ≈ **30%**。**这个机制把"太抖不敢用"的池子变成可用的省钱池。**

---

## 5. A × D 协同(为什么一起做更香)
- A 抬**每 GPU-小时产出的 token**(吞吐),D 压**每 GPU-小时单价**(neocloud/spot)→ 在 $/token 上**相乘**(本批:A ~1.3–1.8× × D 4–7.7× = 综合 5–14× 优于超大厂 on-demand 自托管)。
- A 的 prefix 共享**缩小 KV 足迹** → D 跨区迁移的 **checkpoint 更小、egress 更省、恢复更快**。

---

## 6. 落地可行性 + 要测什么来坐实

| | 收益来源(本批) | 现实区间 | 适用边界 | 落地可行性 | 主要工作 |
|---|---|---|---|---|---|
| **A** | B1 共享前缀(A1)为主 + 资源混排(A2)为辅 | 吞吐 1.3–1.8×(高共享批);~1.05×(无共享批)| 有大固定前缀/混合画像的批 | **高 · 低风险 · 不碰在线 SLO** | 控制面 reorder pass(`batch/optimizer/`)+ 喂已有 APC/prefix cache + 显存预算成批;`scheduler.py:256` 执行单元改"组" |
| **D** | sourcing 阶梯(neocloud/spot)+ 抢占恢复机制 | 比超大厂 on-demand 再省 4–7.7×;比 OpenAI 到 10× | 大、deadline 松、KV 不巨、可跨区 | **中**(控制面易;**推理原生 checkpoint 是真正的工程量,也是差异化**)| `planner.Schedule` 成本模型(catalog 定价/预测已有)+ 探活 + deadline 安全网 + **KV/队列 checkpoint↔object store** |

**要测什么来把"示意"换成"确定"(= Paper C / cost-calculator 的活)**:
1. 70B(及 8B/14B)在 H100 / neocloud GPU 上的**实测 prefill & decode 吞吐**(填 §3/§4 的速率)。
2. 上面这个 batch(或你的真实 batch)在**开/关 A1、开/关 A2** 下的实测吞吐(坐实 1.3–1.8×)。
3. Lambda/RunPod 的**实测 $/GPU-hr + spot 价格/抢占率时间序列**(填 §4 阶梯与 λ)。
4. 推理原生 checkpoint 的**实测单次恢复耗时**(坐实 <0.3% 开销)。

> 这 4 项一旦实测,§3/§4 的区间就变成确定数字,既能判"值不值",又直接喂 cost-calculator 和 Paper C。
