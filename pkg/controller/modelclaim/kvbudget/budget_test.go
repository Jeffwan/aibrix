/*
Copyright 2025 The Aibrix Team.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kvbudget

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// sumBudgets is the no-overcommit invariant helper.
func sumBudgets(p Plan) int64 {
	var s int64
	for _, b := range p.Budgets {
		s += b
	}
	return s
}

func TestExclusiveIsPinnedNoBorrow(t *testing.T) {
	plan := Compute(100, 0, []ModelDemand{
		{Name: "excl", Policy: PolicyExclusive, Min: 30, Demand: 100},
		{Name: "shared", Policy: PolicyShared, Max: 100, Demand: 100},
	})
	// Exclusive is pinned at its slice regardless of high demand.
	assert.Equal(t, int64(30), plan.Budgets["excl"])
	// Shared takes the rest.
	assert.Equal(t, int64(70), plan.Budgets["shared"])
	assert.LessOrEqual(t, sumBudgets(plan), int64(100))
	assert.Empty(t, plan.Evict)
}

func TestGuaranteedFloorPlusBurst(t *testing.T) {
	plan := Compute(100, 0, []ModelDemand{
		{Name: "g", Policy: PolicyGuaranteed, Min: 10, Max: 100, Demand: 40},
	})
	// Floor 10 + burst up to demand 40 (not the whole pool).
	assert.Equal(t, int64(40), plan.Budgets["g"])
}

func TestSharedSplitsByShares(t *testing.T) {
	plan := Compute(100, 0, []ModelDemand{
		{Name: "a", Policy: PolicyShared, Max: 100, Demand: 100, Shares: 2},
		{Name: "b", Policy: PolicyShared, Max: 100, Demand: 100, Shares: 1},
	})
	// ~2:1 split of the pool, no bytes stranded.
	assert.Equal(t, int64(67), plan.Budgets["a"])
	assert.Equal(t, int64(33), plan.Budgets["b"])
	assert.Equal(t, int64(100), sumBudgets(plan))
}

func TestCeilingCaps(t *testing.T) {
	plan := Compute(100, 0, []ModelDemand{
		{Name: "capped", Policy: PolicyShared, Max: 25, Demand: 100},
		{Name: "open", Policy: PolicyShared, Max: 100, Demand: 100},
	})
	assert.Equal(t, int64(25), plan.Budgets["capped"], "never exceeds ceiling")
	assert.LessOrEqual(t, sumBudgets(plan), int64(100))
}

func TestHeadroomReserved(t *testing.T) {
	plan := Compute(100, 10, []ModelDemand{
		{Name: "s", Policy: PolicyShared, Max: 100, Demand: 100},
	})
	assert.Equal(t, int64(90), plan.Budgets["s"], "capacity minus headroom")
}

func TestNoOvercommitInvariant(t *testing.T) {
	plan := Compute(1000, 50, []ModelDemand{
		{Name: "a", Policy: PolicyGuaranteed, Min: 100, Max: 800, Demand: 800, Shares: 3},
		{Name: "b", Policy: PolicyShared, Max: 800, Demand: 800, Shares: 1},
		{Name: "c", Policy: PolicyExclusive, Min: 200, Demand: 50},
	})
	assert.LessOrEqual(t, sumBudgets(plan), int64(950), "sum of budgets <= capacity - headroom")
	assert.GreaterOrEqual(t, plan.Budgets["a"], int64(100), "guaranteed floor honored")
	assert.Equal(t, int64(200), plan.Budgets["c"], "exclusive pinned")
}

func TestEvictsLowestPriorityWhenFloorsDoNotFit(t *testing.T) {
	plan := Compute(100, 0, []ModelDemand{
		{Name: "a", Policy: PolicyGuaranteed, Min: 50, Demand: 50, Priority: 10},
		{Name: "b", Policy: PolicyGuaranteed, Min: 50, Demand: 50, Priority: 5},
		{Name: "c", Policy: PolicyGuaranteed, Min: 50, Demand: 50, Priority: 1},
	})
	// floors 150 > 100: evict the single lowest-priority model to fit.
	assert.Equal(t, []string{"c"}, plan.Evict)
	assert.NotContains(t, plan.Budgets, "c")
	assert.Equal(t, int64(50), plan.Budgets["a"])
	assert.Equal(t, int64(50), plan.Budgets["b"])
}

func TestEvictsMultipleUntilFits(t *testing.T) {
	plan := Compute(80, 0, []ModelDemand{
		{Name: "a", Policy: PolicyGuaranteed, Min: 50, Demand: 50, Priority: 10},
		{Name: "b", Policy: PolicyGuaranteed, Min: 50, Demand: 50, Priority: 5},
		{Name: "c", Policy: PolicyGuaranteed, Min: 50, Demand: 50, Priority: 1},
	})
	// floors 150 > 80: evict c (prio 1) then b (prio 5), keep a.
	assert.Equal(t, []string{"c", "b"}, plan.Evict)
	assert.Equal(t, int64(50), plan.Budgets["a"])
	assert.Len(t, plan.Budgets, 1)
}

func TestIdleModelGetsOnlyItsDemand(t *testing.T) {
	plan := Compute(100, 0, []ModelDemand{
		{Name: "busy", Policy: PolicyShared, Max: 100, Demand: 100, Shares: 1},
		{Name: "idle", Policy: PolicyShared, Max: 100, Demand: 5, Shares: 1},
	})
	assert.Equal(t, int64(5), plan.Budgets["idle"], "idle model does not hoard the pool")
	assert.Equal(t, int64(95), plan.Budgets["busy"], "busy model uses the freed headroom")
}
