# ③ 实验与验证方法

> 目标:用**可信、可复现、能对外**的方式证明 AIBrix Batch 的两个核心主张——
> **(a) 用弹性算力把成本打下来;(b) 价格能对标甚至低于 OpenAI/其他厂商。**
> 关键纪律:每个主张都要有 **诚实基线 + 消融 + 置信区间**,否则会被人用 cherry-pick 反驳。

---

## 1. 北极星指标与成本模型

**北极星:`$ / 1M tokens`**,prompt(输入)与 generated(输出)**分开计**(因为竞品就是分开定价),
且是**端到端真实成本**,不是只算 GPU:

```
$/1M_tok = ( Σ 实例$/hr × 实例-hr  +  egress$  +  对象存储$  +  控制面摊销$ )  /  (总 token 数 / 1e6)

实例-hr 拆成:  dedicated GPU-hr  +  harvested GPU-hr(边际计价,见下)  +  CPU-hr(offload/Tier-2)
```

- **harvested(收割)算力如何计价**——这是和闭源 batch 最不同、也最需要讲清楚的点。给两种口径,都报:
  1. **边际成本口径**:收割的是在线机队本来就空闲的 GPU,batch 的边际成本 ≈ 电费 + 机会成本 ≈ **接近 0**。
     这是"为什么能低于 5 折"的物理来源。
  2. **完全摊销口径**:把 GPU 全价按 (在线占用 + batch 占用) 的时间/算力比例分摊给 batch。
     这是更保守、更能服人的口径。**对外宣传用口径 2,内部优化看口径 1。**
- **egress / 存储**必须计入(否则跨区 spot 的对比不诚实)——SkyNomad 的教训:egress 跨区差 7×。

### 次级指标(解释力)
| 指标 | 为什么要 |
|---|---|
| goodput = tokens/s/GPU(有效产出) | A/C 的吞吐杠杆,$/tok 的分母 |
| JCT(job completion time)p50/p99 vs completion window | batch 的真实 SLO 是"窗口内完成" |
| harvested ratio = 收割算力 token / 总 token | B 的弹性程度;价格曲线的 x 轴 |
| 在线 SLO 影响:ΔTTFT / ΔTPOT(p50/p99) | colocation 的"不伤在线"硬约束 |
| 抢占延迟 & 抢占率 | Valve 式保障的健康度 |
| spot 占比 & spot 节省比 & 迁移次数/迁移损失 | D 的成本来源与代价 |
| 成本归因分解(GPU/CPU/egress/idle) | 每个里程碑贡献了多少,可独立验证 |
| 结果等价性(vs 在线全价同模型同参) | "便宜但答案一样" —— 必须证伪"偷工减料" |

---

## 2. 基线(诚实对标)

| 代号 | 基线 | 作用 |
|---|---|---|
| **B0** | 今天的 AIBrix batch(专属 vanilla vLLM + 逐条回放) | **诚实内部基线**:证明每个里程碑相对"我们自己的起点"提升了多少 |
| **B1** | vLLM 离线 `LLM.generate`(专属、调好) | **专属硬件的吞吐上限**:看我们离单引擎极限还有多远 |
| **B2** | OpenAI / Anthropic Batch = 在线 list price 5 折 | **市场价格 bar**(绝对 $ + 比值两种口径)。注:Anthropic 还能叠 prompt caching(命中 ~9 折)——对标时要说明我们的 A 家族是**自动**做这件事 |
| **B3** | on-demand GPU vs spot GPU 的云价 | C/D 的硬件价差来源 |
| **B4** | **SkyPilot managed-spot**(跑同一 deadline-bound job) | **D 的对打基线**;再加 SkyNomad 复现(若可)做上界参考 |

> 报告纪律:**任何"X× 提升"都必须写清 vs 哪个基线**。对外默认用 B2(市场)与 B0(自身);
> 切勿用 BatchLLM 的 10.8× / Halo 的 18.6× 这种 cherry-pick 数字做营销。

---

## 3. 负载(覆盖每个家族的卖点)

| 代号 | 负载 | 验证家族 | 来源 |
|---|---|---|---|
| **W1** | 共享系统 prompt × N(企业 prompt 复用) | A(全局 prefix) | 合成:固定长 prefix + 变化尾部 |
| **W2** | 计算/访存互补混合(长 prompt 短输出 + 短 prompt 长输出) | A(BlendServe 混排) | 合成 |
| **W3** | 摘要(arXiv / CNN-DailyMail)、MMLU 分类、代码生成 | 通用真实 batch | 公开数据集 |
| **W4** | 长上下文(LongBench) | C(offload) | 公开 |
| **W5** | **在线 trace 回放(Azure LLM Inference / Mooncake)+ batch 填充** | B(colocation) | 公开 trace |
| **W6** | deadline-bound 大 job,真实多区 spot trace | D(spot,对标 SkyPilot) | AWS/GCP spot trace |
| **W7** | 多步 RAG / agent DAG 批 | E(workflow) | 合成 DAG + 文档集 |

模型矩阵:至少 Llama-3.1-8B / Qwen-2.5-14B / 一个 70B(覆盖小/中/大 + GQA + 量化 INT8 变体)。
硬件矩阵:H100(参照)、A100/A10G、T4、商用 RTX5090/L20、CPU-rich 节点(覆盖 C/D)。

---

## 4. A/B 协议与统计纪律

1. **同硬件、同负载、同模型**下,逐个里程碑/技术做 A/B;**每配置 ≥3 seed**,报 p50/p99 + 95% CI。
2. **逐技术消融**:M1 = 重排 on/off、显存预算 vs 计数;M2 = 准入 on/off、抢占粒度;M3 = offload on/off;以隔离每项贡献。
3. **结果等价性校验**:对同一批输入,batch 路径 vs 在线全价路径,核对输出分布/任务指标(准确率、ROUGE 等)在容差内一致——堵住"便宜=降质"的质疑。
4. **预热与稳态分离**:报稳态吞吐,同时单列冷启动/迁移开销(尤其 D)。
5. **全部配置 + trace + 脚本入库**,复用 AIBrix 既有 benchmarking 工具(v0.3 引入),保证可复现。

---

## 5. 两张"杀手图"(直接支撑价格主张)

这两张图是对外材料和 cost-calculator 的核心论据:

- **图 ①:有效价格 vs 收割比例。** x = harvested ratio(在线波谷被 batch 填充的比例),y = $/1M tok(口径 2)。
  叠一条水平线 = OpenAI batch 价。**结论句**:"当在线机队有 ≥X% 波谷可收割时,AIBrix batch = OpenAI batch 价格的 Z%。"
  (用 W5 在线 trace 回放产生真实的波谷分布,而不是假设。)
- **图 ②:有效价格 vs spot 占比。** x = spot/跨区算力占比,y = $/1M tok,叠 on-demand 线与 SkyPilot 线(B4)。
  **结论句**:"在 Y% spot 占比下,AIBrix 跨区 batch 比 SkyPilot 便宜 W%,且 deadline 100% 满足。"

辅以**成本瀑布图**:从 B0 出发,M1(−a%)→ M2(−b%)→ M3(−c%)→ M4(−d%),展示成本如何**乘性叠加**降到 B2 以下。

---

## 6. 弹性 colocation 专项(M2 的硬验证)

W5 在线 trace 回放下:
- **横扫干扰容忍比 / 价格档位**(如 5% / 20% / 50%),每档报:在线 ΔTTFT/ΔTPOT(p99)**是否守住档位** + batch goodput + $/tok。
- 验证"**不伤在线**":在线 SLO 退化必须 ≤ 档位上限,否则该档无效。
- 验证 Valve 式保障(M2.b):抢占延迟分布(目标亚毫秒)、抢占率(目标"每在线请求 ≤1 次")、MIAD 显存回收对 batch 吞吐的影响(对比 FIFO 驱逐)。
- **结论句**:"在保证在线 p99 退化 <5% 的前提下,AIBrix 用收割算力跑完 batch,有效价格 = OpenAI 的 Z%。"

---

## 7. SkyPilot Bake-off 专项(M4 / 对打 SkyPilot 的硬验证)

W6,真实多区 spot trace,同一个 deadline-bound 大 batch job:
- **参赛方**:① AIBrix 跨区(推理原生 checkpoint)② SkyPilot managed-spot(B4)③(可选)SkyNomad 复现做上界。
- **报**:总 $(含 egress)、deadline 是否满足、**迁移次数 + 单次迁移损失(冷启动/重算)**、被抢占恢复时间。
- **差异化的证据点**:AIBrix 因"请求队列 + prefix-KV 秒级 checkpoint",**单次迁移损失应显著低于 SkyPilot 的整机 ~6min**;
  在抢占频繁的区,这转化为更低总成本 + 更稳的 deadline。
- **结论句**:"在 GPU 紧俏/抢占频繁的多区环境,AIBrix 比 SkyPilot managed-spot 省 W%,且迁移损失低一个量级——因为我们是推理原生,不是整机搬运。"

---

## 8. 与 cost-calculator 联动(让工具变可信销售资产)

- 把 [batch-cost-calculator](https://aibrix.github.io/tools/batch-cost-calculator/) 的**吞吐/价格假设替换成本套实测值**
  (按 模型×硬件×负载 给 goodput 表;按 收割比例/spot 占比 给价格曲线)。
- 让计算器输出与第 5 节两张图**口径一致**:用户输入"我有多大在线机队、多少波谷、能用多少 spot",计算器直接给出
  "你的 batch 有效价格 = OpenAI 的 Z%"。
- 参考 [aiconfigurator](https://github.com/ai-dynamo/aiconfigurator) 的配置→性能/成本映射做法,把"配置→$/tok"做成可复现的查表+公式。

---

## 9. 验收门槛(建议写进 v0.7.0 release 标准)

- M0:1000-job 混合负载稳定,成本账单可信。
- M1:W1/W2 上 goodput vs B0 **≥1.3×**,结果等价性通过。
- M2.a:W5 上在线 p99 退化守住 5% 档,batch goodput>0,**图 ① 成立**(给出至少一个"= OpenAI Z%"的点,Z 显著<100)。
- (后续)M3:某长上下文负载 $/tok vs 纯 H100 专属 **↓≥2×**。
- (后续)M4:W6 上 **$ < SkyPilot(B4)** 且 deadline 100% 满足。

> 所有对外数字都同时给 **绝对 $/1M tok** 和 **vs OpenAI 比值**,并标注硬件/负载/口径——可信度比"大倍数"更重要。
