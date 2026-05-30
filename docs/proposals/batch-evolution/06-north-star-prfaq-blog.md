# ⑥ North-Star PRFAQ + Blog — "AIBrix Batch, fully realized"

> This is the **end-state** narrative: what AIBrix Batch looks like once **all of P0–P6 have landed** (see `02-evolution-roadmap.md`). It is deliberately aspirational but grounded in the roadmap. Use it to align long-term direction and as the **v1.0** launch material. The near-term **v0.7.0** PRFAQ/blog lives in `04-v0.7.0-prfaq-blog.md`. Numbers in `[brackets]` are placeholders to fill from `03-experiments.md`. **Publish no number you have not measured.**

---

# Part A — Internal PRFAQ (v1.0 / fully realized)

## A.1 Press Release

**FOR IMMEDIATE RELEASE**

### AIBrix Batch: tell it your deadline and budget — it runs your batch on the cheapest GPUs on Earth, and proves online stays untouched

**Subhead:** AIBrix Batch turns batch inference into a **cost SLO**. Submit an OpenAI-compatible batch with a completion deadline and (optionally) a price target; AIBrix completes it at minimum cost across every GPU you can reach — idle troughs of your own online fleet, neoclouds, and spot across regions and providers — and gives you a bounded, demonstrable guarantee that your online traffic was not harmed.

Today the AIBrix community announced the v1.0 batch stack. Batch inference has been stuck between a closed API that charges a flat 50%-of-list on a black box, and do-it-yourself clusters pinned to one expensive cloud. AIBrix Batch replaces both with a **cost-optimal control plane** for batch inference.

You submit a batch the way you already do — `/v1/files`, `/v1/batches`, JSONL — and add two things closed APIs never let you express: a **deadline** and an optional **cost target / online-impact tier**. From there AIBrix:

- **Sources the cheapest capacity, automatically.** It places work across a unified pool: idle capacity harvested from your own online serving fleet (at near-marginal cost), neocloud GPUs (Lambda, RunPod, and more), and spot instances across regions and providers — using a live model of price, availability, and data-egress cost. When spot is reclaimed, it **migrates in seconds, not minutes**, because it checkpoints at request-queue and KV-cache granularity, not whole-VM.
- **Protects online with a guarantee, not a hope.** When batch shares GPUs with your online services, a bounded-preemption isolation layer keeps online latency degradation within the tier you chose — and reports the actual bound it held — across your *multi-model* fleet.
- **Squeezes every GPU.** Whole-job prefix reuse and compute/memory-balanced scheduling raise tokens-per-GPU; for agentic pipelines, a workflow-batch mode deduplicates shared computation across thousands of instances.
- **Tells you the price up front and hits it.** You get an estimated `$/1M tokens` and completion time before you run, and a cost SLO afterward.

In production, early adopters report `[X%]` lower cost per million tokens than their prior closed-Batch-API spend — and `[Y%]` lower than single-cloud on-demand — while meeting 100% of deadlines and holding online p99 degradation within `[ε]`.

"Closed batch gives you a discount on a black box; SkyPilot gives you a CLI that launches anything anywhere but doesn't know what an inference engine is," said an AIBrix maintainer. "AIBrix Batch is the cloud-native, inference-native front door: you declare intent — deadline, budget, don't-hurt-online — and it finds the cheapest safe way to run it. That's a cost SLO, and nobody else offers it."

AIBrix is open source under Apache 2.0.

## A.2 External FAQ

**Q: What is a "cost SLO"?**
Online serving has a *latency* SLO (e.g., p99 TTFT < 200ms). Batch has the opposite shape: latency is relaxed, so the thing worth guaranteeing is **cost under a deadline**. You declare "finish by T, ideally under $B, don't degrade online by more than ε," and AIBrix guarantees minimum-cost completion within those constraints — or tells you up front it can't.

**Q: How is it so much cheaper than a 50%-off closed API?**
Closed APIs discount a fixed list price. AIBrix lowers the *actual cost basis* and stacks the savings: (1) cheapest sourcing — your own idle GPUs at near-marginal cost, neoclouds 50–80% under hyperscalers, spot across regions/providers; (2) higher throughput per GPU; (3) for workflows, fewer tokens via dedup. These compound, so effective price can sit well below 50%-of-online — and you can see and tune every input.

**Q: If batch runs on my online GPUs, how do I know it won't hurt my users?**
The isolation layer bounds both how fast batch yields and how often it's preempted, and reclaims memory without faulting online — across different models on the same GPUs. You pick a tier (e.g., "≤5% p99"), and AIBrix enforces and reports it. If you want zero colocation, run batch on dedicated/neocloud/spot pools only.

**Q: Spot gets reclaimed — won't that blow my deadline or waste work?**
AIBrix checkpoints batch state (pending queue + prefix KV) to object storage continuously, so a reclaimed instance resumes elsewhere in seconds without recomputing prefills. A deadline safety-net falls back to on-demand when slack runs low. Deadlines are a guarantee, not best-effort.

**Q: How is this different from SkyPilot?**
SkyPilot launches arbitrary jobs on many clouds via a CLI. AIBrix Batch is the **cloud-native, inference-native, API-first** control plane for *batch inference*: it speaks OpenAI Batch end-to-end, understands KV cache / prefill / routing, migrates at inference-state granularity (seconds vs whole-VM minutes), and fuses cross-region spot with online-fleet harvesting — which a workload-agnostic launcher cannot. We provision neoclouds natively; we don't depend on SkyPilot.

**Q: Can I submit multi-step / agentic pipelines?**
Yes. Workflow-batch mode takes a DAG of model + tool calls and a set of inputs, and optimizes the whole batch as one query — sharing KV across steps and deduplicating repeated tool/model calls — instead of you firing thousands of independent API calls.

**Q: Does my data leave my environment?**
Only to the providers you authorize, inside your accounts and object store. Self-hosted means your compliance boundary.

## A.3 Internal FAQ

**Q: What's the actual moat — why can't OpenAI or SkyPilot just do this?**
Closed providers won't expose a cost SLO or run on *your* idle fleet/cheap clouds — it cannibalizes their pricing. SkyPilot is workload-agnostic: it can't checkpoint at KV granularity, can't harvest an online fleet with an inference-aware isolation guarantee, can't dedup workflow KV. Our moat is the **intersection: cloud-native (K8s) × inference-native (engine/KV/routing) × API-first (OpenAI Batch) × cost-optimal across all capacity.** Each competitor has one or two; none has all four.

**Q: What makes the cost SLO *credible* rather than marketing?**
It's enforced by the optimizer (placement + deadline safety-net), bounded by the isolation guarantee (online impact), and measured by per-job cost accounting. We publish the dual-cost methodology (`03-experiments.md`) and the reproducible price-vs-capacity curves. The SLO is "minimum cost s.t. deadline ∧ online-degradation ≤ ε," with on-demand fallback guaranteeing the deadline.

**Q: What's the single biggest technical risk to the end-state?**
Multi-model isolation guarantee (the research bet) and cross-provider spot reliability at scale. If the isolation guarantee only holds empirically (not analytically) across models, the harvesting tier ships with a tight empirical bound rather than a proof — still useful, but framed honestly.

**Q: How do we sequence to get here without overpromising?**
Exactly the P0–P6 order in `02`: foundation+neocloud (v0.7.0) → isolation guarantee → spot/cross-region → throughput → harvesting feature → offload tiers → workflow → unified optimizer (v1.0). Each phase ships standalone value; the cost SLO is the capstone that ties them.

---

# Part B — Public Blog Draft (v1.0, AIBrix voice)

## Batch Inference, Reimagined: A Cost SLO for LLMs

AIBrix is a composable, cloud-native LLM inference infrastructure designed to deliver high performance and low cost at scale. Two years ago, batch inference meant renting a black box at a fixed discount. Today, with the v1.0 batch stack, you tell AIBrix your **deadline and your budget**, and it runs your batch on the cheapest GPUs you can reach — provably without hurting your online traffic.

### From "discount" to "cost SLO"

Online serving is governed by a latency SLO. Batch is the mirror image: latency is relaxed (hours), so the quantity worth controlling is **cost under a deadline**. AIBrix makes that a first-class contract — a *cost SLO*: *"complete this batch by T, at minimum cost, degrading online by no more than ε."* You declare intent; the system finds the cheapest safe execution.

### One pool, every GPU

Under the hood, AIBrix unifies capacity that used to be separate silos:
- **Harvested online troughs** — your peak-provisioned serving fleet is idle most of the day; batch fills it at near-marginal cost.
- **Neoclouds** — Lambda, RunPod, and more, natively provisioned, often 50–80% under hyperscaler on-demand.
- **Spot across regions and providers** — chosen by a live cost model (price × availability × egress), with seconds-scale migration on reclamation.

A two-level optimizer harvests locally first, then spills to spot/neocloud — under your deadline and online-impact tier. *(Figure: effective $/1M tokens vs OpenAI Batch and single-cloud on-demand, across capacity mixes.)*

### The guarantee that makes harvesting safe

Running batch on your online GPUs is only acceptable if online is protected. AIBrix's isolation layer bounds preemption latency and rate and reclaims memory without faulting — and does it across a *multi-model* fleet. You set the tier; AIBrix enforces and reports the bound it held. *(Figure: online p99 with/without batch colocation.)*

### Inference-native, not VM-native

Because AIBrix checkpoints at **request-queue + KV-cache** granularity (not whole-VM), a reclaimed spot instance resumes elsewhere in **seconds with no prefill recompute** — the difference between "spot is scary" and "spot is the default." This is what a workload-agnostic launcher can't do, and it's why AIBrix — not a CLI on top of VMs — is the entry point for batch.

### Squeeze, then dedup

Whole-job prefix reuse and compute/memory-balanced scheduling lift tokens-per-GPU; workflow-batch mode treats a batch of agentic pipelines as one query, deduplicating shared model/tool calls across instances. *(Figure: GPU-hours per job, naive vs AIBrix.)*

### Why only AIBrix

| | Closed Batch API | SkyPilot | **AIBrix Batch** |
|---|---|---|---|
| Cost model | fixed 50% off, black box | per-VM, you optimize | **cost SLO, optimized for you** |
| Your idle fleet | ✗ | ✗ | **✓ harvested, with guarantee** |
| Cheapest GPUs / spot | ✗ | ✓ (VM-level) | **✓ inference-native, sec-scale migration** |
| Inference-aware | n/a | ✗ | **✓ KV / prefix / P-D / routing** |
| Interface | API | CLI | **OpenAI-compatible API, K8s-native** |

### What's beyond v1.0

Tighter analytical isolation guarantees, more providers, learned cost/availability prediction, and richer workflow optimization. The destination stays the same: **the lowest-cost, safest place to run batch inference on infrastructure you control.**

*Try it: point your OpenAI SDK `base_url` at AIBrix, add a deadline, submit. Docs / roadmap: (links). Contributors: (list).*

---

# Part C — One-liners (v1.0)

- **The thesis:** *"Batch inference should be a cost SLO, not a black-box discount — declare your deadline and budget, run on the cheapest GPUs on Earth, and keep online untouched."*
- **vs OpenAI/Anthropic:** *"They discount a black box. AIBrix gives you a cost SLO and runs it on your own idle GPUs, neoclouds, and spot — all yours, all visible."*
- **vs SkyPilot:** *"SkyPilot launches any job anywhere. AIBrix is the inference-native control plane for batch — it migrates KV in seconds, harvests your online fleet safely, and speaks OpenAI Batch."*
