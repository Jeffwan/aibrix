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

"""Tests for the node-level KV fast-reclaim loop (block C), with fake agents."""

from aibrix.runtime.node_kv_reclaim import (
    demands_from_listing,
    reconcile_gpu,
    run_node_reclaim_once,
)

GiB = 1 << 30


class FakeAgent:
    """In-memory stand-in for PodAgentClient (one warm pod = one GPU)."""

    host = "fake"
    port = 0

    def __init__(self, models):
        self.models = models
        self.budget_calls = []
        self.evict_calls = []

    def list_models(self):
        return self.models

    def set_kv_budget(self, model_name, kv_max, kv_min=0):
        self.budget_calls.append((model_name, kv_max, kv_min))
        for m in self.models:  # reflect the change like the real agent does
            if m["model_name"] == model_name:
                m["kv_max_bytes"] = kv_max

    def evict(self, model_name):
        self.evict_calls.append(model_name)


def listing(name, *, used=0, kv_min=0, kv_max=0, policy="Guaranteed", shares=1,
            priority=0, phase="active"):
    return {
        "model_name": name,
        "port": 20000,
        "ipc_name": f"kvc_{name}",
        "phase": phase,
        "kv_min_bytes": kv_min,
        "kv_max_bytes": kv_max,
        "kv_used_bytes": used,
        "kv_total_bytes": 0,
        "kv_policy": policy,
        "kv_shares": shares,
        "priority": priority,
    }


def test_demands_from_listing_maps_fields_and_skips_sleeping():
    ds = demands_from_listing(
        [
            listing("a", used=5, kv_min=1, kv_max=10, policy="Shared", shares=3, priority=7),
            listing("gone", phase="sleeping"),
        ]
    )
    assert len(ds) == 1
    d = ds[0]
    assert (d.name, d.policy, d.min_bytes, d.max_bytes) == ("a", "Shared", 1, 10)
    assert (d.shares, d.priority, d.demand) == (3, 7, 5)


def test_reconcile_posts_only_changed_budgets():
    # Two Guaranteed models, floors fit, demand under capacity: each should get
    # floor + burst toward its demand. Model "a" already has the right ceiling.
    agent = FakeAgent(
        [
            listing("a", used=2 * GiB, kv_min=1 * GiB, kv_max=2 * GiB),
            listing("b", used=3 * GiB, kv_min=1 * GiB, kv_max=8 * GiB),
        ]
    )
    res = reconcile_gpu(agent, capacity_bytes=8 * GiB)
    # a: floor 1Gi + burst to demand 2Gi = 2Gi == current -> no call.
    # b: floor 1Gi + burst to demand 3Gi = 3Gi != current 8Gi -> one call.
    assert [c[0] for c in agent.budget_calls] == ["b"]
    assert res.budgets_set == {"b": 3 * GiB}
    assert res.evicted == []
    # Second tick after convergence: all reads, no writes.
    agent.budget_calls.clear()
    res2 = reconcile_gpu(agent, capacity_bytes=8 * GiB)
    assert agent.budget_calls == [] and res2.budgets_set == {}


def test_reconcile_evicts_lowest_priority_when_floors_do_not_fit():
    agent = FakeAgent(
        [
            listing("vip", kv_min=3 * GiB, priority=100),
            listing("besteffort", kv_min=2 * GiB, priority=1),
        ]
    )
    res = reconcile_gpu(agent, capacity_bytes=4 * GiB)
    assert agent.evict_calls == ["besteffort"]
    assert res.evicted == ["besteffort"]
    assert "vip" in res.budgets_set or agent.budget_calls  # vip gets a budget


def test_reconcile_handles_agent_failure_gracefully():
    class Boom(FakeAgent):
        def list_models(self):
            raise RuntimeError("down")

    res = reconcile_gpu(Boom([]), capacity_bytes=GiB)
    assert res.error is not None
    assert res.budgets_set == {} and res.evicted == []


def test_run_node_reclaim_once_covers_every_gpu():
    a = FakeAgent([listing("a", used=GiB, kv_min=0, kv_max=4 * GiB)])
    b = FakeAgent([listing("b", used=GiB, kv_min=0, kv_max=4 * GiB)])
    results = run_node_reclaim_once([a, b], capacity_bytes=2 * GiB)
    assert len(results) == 2
    # Each GPU reconciled independently: budget clamped to demand (1Gi) != 4Gi.
    assert a.budget_calls and b.budget_calls
