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

"""Node-level KV fast-reclaim loop (ModelClaim Layer-2 block C).

The per-node DaemonSet is the reclaim BRAIN; the warm pods' agents are the
sensors and actuators. Each warm pod owns exactly one GPU, so one pod's model
listing is one GPU's co-tenant set. Per tick, for every warm pod on the node:

  observe   GET /v1/runtime/models   (per-model used/total KV from the kvcached
            /dev/shm MemInfoStruct, plus the CRD-derived policy/min/max/shares/
            priority carried at activation)
  decide    kv_reclaim.compute(capacity, headroom, demands) — the same pure
            decision core as the Go controller's kvbudget package
  actuate   POST /v1/runtime/models/kv_budget for budgets that changed
            (kvctl, cheap),
            POST /v1/runtime/models/deactivate mode=evict for floors that don't fit
            (vLLM sleep, expensive — cost-asymmetric by design)

Running the brain in the DaemonSet (not the sidecar) gives it an independent
lifecycle and the whole-node view Layer 3 builds on; the agents stay dumb
executors, mirroring the controller<->agent split one level down.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Sequence

from aibrix.runtime import kv_reclaim

logger = logging.getLogger(__name__)


class PodAgentClient:
    """Minimal HTTP client for one warm pod's agent (httpx; injectable in tests)."""

    def __init__(self, host: str, port: int, timeout: float = 5.0) -> None:
        self.host = host
        self.port = port
        self.timeout = timeout

    def _url(self, path: str) -> str:
        return f"http://{self.host}:{self.port}{path}"

    def list_models(self) -> List[dict]:
        import httpx

        resp = httpx.get(self._url("/v1/runtime/models"), timeout=self.timeout)
        resp.raise_for_status()
        return resp.json().get("models", [])

    def set_kv_budget(self, model_name: str, kv_max: int, kv_min: int = 0) -> None:
        import httpx

        httpx.post(
            self._url("/v1/runtime/models/kv_budget"),
            json={
                "model_name": model_name,
                "kv_max_bytes": kv_max,
                "kv_min_bytes": kv_min,
            },
            timeout=self.timeout,
        ).raise_for_status()

    def evict(self, model_name: str) -> None:
        import httpx

        httpx.post(
            self._url("/v1/runtime/models/deactivate"),
            json={"model_name": model_name, "mode": "evict", "sleep_level": 1},
            timeout=self.timeout,
        ).raise_for_status()


def demands_from_listing(models: Sequence[dict]) -> List[kv_reclaim.ModelDemand]:
    """Rebuild the per-GPU demand set from one agent's model listing.

    Evicted models are excluded: their KV is already released and their floor
    must not crowd out resident tenants (they re-enter the demand set when the
    controller wakes them).
    """
    demands: List[kv_reclaim.ModelDemand] = []
    for m in models:
        if m.get("phase") in ("sleeping", "evicted"):
            continue
        demands.append(
            kv_reclaim.ModelDemand(
                name=m["model_name"],
                policy=m.get("kv_policy") or kv_reclaim.POLICY_GUARANTEED,
                min_bytes=int(m.get("kv_min_bytes") or 0),
                max_bytes=int(m.get("kv_max_bytes") or 0),
                shares=int(m.get("kv_shares") or 1),
                priority=int(m.get("priority") or 0),
                demand=int(m.get("kv_used_bytes") or 0),
            )
        )
    return demands


@dataclass
class GPUReclaimResult:
    """What one tick decided + actually dispatched for one GPU (= one pod)."""

    agent: str
    budgets_set: Dict[str, int] = field(default_factory=dict)
    evicted: List[str] = field(default_factory=list)
    error: Optional[str] = None


def reconcile_gpu(
    agent: PodAgentClient, capacity_bytes: int, headroom_bytes: int = 0
) -> GPUReclaimResult:
    """One reclaim tick for one GPU: observe -> decide -> actuate diffs only.

    A budget is re-posted only when it differs from the model's current ceiling,
    so a converged node is all reads and no writes.
    """
    result = GPUReclaimResult(agent=f"{agent.host}:{agent.port}")
    try:
        models = agent.list_models()
    except Exception as exc:
        result.error = f"list failed: {exc}"
        return result

    demands = demands_from_listing(models)
    if not demands:
        return result
    plan = kv_reclaim.compute(capacity_bytes, headroom_bytes, demands)

    current = {m["model_name"]: int(m.get("kv_max_bytes") or 0) for m in models}
    floors = {m["model_name"]: int(m.get("kv_min_bytes") or 0) for m in models}
    for name, budget in sorted(plan.budgets.items()):
        if budget == current.get(name):
            continue
        try:
            agent.set_kv_budget(name, budget, floors.get(name, 0))
            result.budgets_set[name] = budget
        except Exception as exc:
            logger.warning("set_kv_budget %s on %s failed: %s", name, result.agent, exc)
    for name in plan.evict:
        try:
            agent.evict(name)
            result.evicted.append(name)
        except Exception as exc:
            logger.warning("evict %s on %s failed: %s", name, result.agent, exc)
    return result


def run_node_reclaim_once(
    agents: Sequence[PodAgentClient], capacity_bytes: int, headroom_bytes: int = 0
) -> List[GPUReclaimResult]:
    """Reclaim every GPU on the node (one warm pod each, independently)."""
    return [reconcile_gpu(a, capacity_bytes, headroom_bytes) for a in agents]
