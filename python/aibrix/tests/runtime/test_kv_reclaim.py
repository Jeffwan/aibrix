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

"""Tests for the node-local KV reclaim core (mirror of the Go kvbudget tests)."""

from aibrix.runtime.kv_reclaim import (
    POLICY_EXCLUSIVE,
    POLICY_GUARANTEED,
    POLICY_SHARED,
    ModelDemand,
    compute,
)


def sum_budgets(plan):
    return sum(plan.budgets.values())


def test_exclusive_pinned_no_borrow():
    plan = compute(
        100,
        0,
        [
            ModelDemand("excl", POLICY_EXCLUSIVE, min_bytes=30, demand=100),
            ModelDemand("shared", POLICY_SHARED, max_bytes=100, demand=100),
        ],
    )
    assert plan.budgets["excl"] == 30
    assert plan.budgets["shared"] == 70
    assert sum_budgets(plan) <= 100
    assert plan.evict == []


def test_shares_split():
    plan = compute(
        100,
        0,
        [
            ModelDemand("a", POLICY_SHARED, max_bytes=100, demand=100, shares=2),
            ModelDemand("b", POLICY_SHARED, max_bytes=100, demand=100, shares=1),
        ],
    )
    assert plan.budgets["a"] == 67
    assert plan.budgets["b"] == 33
    assert sum_budgets(plan) == 100


def test_headroom_reserved():
    plan = compute(
        100, 10, [ModelDemand("s", POLICY_SHARED, max_bytes=100, demand=100)]
    )
    assert plan.budgets["s"] == 90


def test_guaranteed_floor_plus_burst():
    plan = compute(
        100,
        0,
        [ModelDemand("g", POLICY_GUARANTEED, min_bytes=10, max_bytes=100, demand=40)],
    )
    assert plan.budgets["g"] == 40


def test_eviction_lowest_priority():
    plan = compute(
        100,
        0,
        [
            ModelDemand("a", POLICY_GUARANTEED, min_bytes=50, demand=50, priority=10),
            ModelDemand("b", POLICY_GUARANTEED, min_bytes=50, demand=50, priority=5),
            ModelDemand("c", POLICY_GUARANTEED, min_bytes=50, demand=50, priority=1),
        ],
    )
    assert plan.evict == ["c"]
    assert "c" not in plan.budgets
    assert plan.budgets["a"] == 50
    assert plan.budgets["b"] == 50


def test_idle_model_gets_only_its_demand():
    plan = compute(
        100,
        0,
        [
            ModelDemand("busy", POLICY_SHARED, max_bytes=100, demand=100),
            ModelDemand("idle", POLICY_SHARED, max_bytes=100, demand=5),
        ],
    )
    assert plan.budgets["idle"] == 5
    assert plan.budgets["busy"] == 95
