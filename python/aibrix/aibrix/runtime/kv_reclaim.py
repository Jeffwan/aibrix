# Copyright 2025 The Aibrix Team.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# 	http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Node-local KV reclaim decision core for ModelClaim runtime models.

Given each resident model's guarantee/ceiling/priority/shares and current
demand, plus the GPU's KV capacity, compute the per-model KV budget to enforce
(via kvctl) and which models' weights to evict (via vLLM sleep) when protected
floors do not fit. Reclaim is cost-asymmetric: shrink KV via budgets first
(cheap), evict weights only when floors do not fit (expensive).

This mirror runs in the runtime sidecar's fast reclaim loop. Pure / no I/O, so
fully unit-testable.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Dict, List

POLICY_EXCLUSIVE = "Exclusive"
POLICY_SHARED = "Shared"
POLICY_GUARANTEED = "Guaranteed"


@dataclass
class ModelDemand:
    name: str
    policy: str = POLICY_GUARANTEED
    min_bytes: int = 0
    max_bytes: int = 0  # 0 => capacity
    shares: int = 1
    priority: int = 0
    demand: int = 0


@dataclass
class Plan:
    budgets: Dict[str, int] = field(default_factory=dict)
    evict: List[str] = field(default_factory=list)


def _effective(m: ModelDemand, capacity: int) -> ModelDemand:
    max_bytes = m.max_bytes if 0 < m.max_bytes <= capacity else capacity
    min_bytes = m.min_bytes
    if m.policy == POLICY_EXCLUSIVE:
        if min_bytes <= 0:
            min_bytes = max_bytes
        max_bytes = min_bytes
    min_bytes = max(0, min(min_bytes, max_bytes))
    return ModelDemand(
        name=m.name,
        policy=m.policy,
        min_bytes=min_bytes,
        max_bytes=max_bytes,
        shares=m.shares if m.shares > 0 else 1,
        priority=m.priority,
        demand=max(0, m.demand),
    )


def _fit_guarantees(models: List[ModelDemand], avail: int):
    floors = sum(m.min_bytes for m in models)
    if floors <= avail:
        return models, []
    # Evict lowest priority first; break ties by larger floor (frees more).
    order = sorted(
        range(len(models)), key=lambda i: (models[i].priority, -models[i].min_bytes)
    )
    evicted_idx = set()
    evicted: List[str] = []
    for i in order:
        if floors <= avail:
            break
        evicted_idx.add(i)
        evicted.append(models[i].name)
        floors -= models[i].min_bytes
    kept = [m for i, m in enumerate(models) if i not in evicted_idx]
    return kept, evicted


def _distribute_burst(
    models: List[ModelDemand], budgets: Dict[str, int], pool: int
) -> None:
    if pool <= 0:
        return
    want: Dict[str, int] = {}
    shares: Dict[str, int] = {}
    for m in models:
        target = min(m.demand, m.max_bytes)
        w = target - budgets[m.name]
        if w > 0:
            want[m.name] = w
            shares[m.name] = m.shares

    while pool > 0 and want:
        total_shares = sum(shares[n] for n in want)
        if total_shares == 0:
            break
        granted = 0
        progressed = False
        for name in sorted(want):
            grant = pool * shares[name] // total_shares
            if grant <= 0:
                continue
            grant = min(grant, want[name])
            budgets[name] += grant
            want[name] -= grant
            granted += grant
            if want[name] <= 0:
                del want[name]
            progressed = True
        pool -= granted
        if not progressed:
            # Remainder too small to split by shares; give it to the highest-share
            # unsatisfied model to avoid stranding bytes.
            best = max(want, key=lambda n: shares[n], default=None)
            if best is None:
                break
            grant = min(pool, want[best])
            budgets[best] += grant
            pool -= grant
            break


def compute(capacity: int, headroom: int, models: List[ModelDemand]) -> Plan:
    """Allocate KV budgets across models on one GPU. Returns a budget for every
    non-evicted model (sum <= capacity-headroom) plus the eviction list."""
    avail = max(0, capacity - headroom)
    eff = [_effective(m, capacity) for m in models]
    kept, evicted = _fit_guarantees(eff, avail)

    budgets: Dict[str, int] = {m.name: m.min_bytes for m in kept}
    reserved = sum(m.min_bytes for m in kept)
    _distribute_burst(kept, budgets, avail - reserved)
    return Plan(budgets=budgets, evict=evicted)
