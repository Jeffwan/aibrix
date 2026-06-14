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

// Package kvbudget is the pure decision core of the elastic GPU-memory resource
// manager: given each model's guarantee/ceiling/priority/shares and current
// demand plus the GPU's KV capacity, it computes the per-model KV budget to
// enforce (via kvctl) and, when guarantees cannot all fit, which models' weights
// must be evicted (via vLLM sleep).
//
// The model mirrors cgroup v2 memory (min = protected floor, max = ceiling),
// HTB-style borrowing (idle headroom shared by `shares` weight), and Kubernetes
// QoS/priority preemption (lowest priority evicted first). Reclaim is
// cost-asymmetric: KV is shrunk through budgets first (cheap, ms); only when
// protected floors do not fit are weights evicted (expensive, seconds).
//
// This package is intentionally pure (no I/O) so it is fully unit-testable
// without a GPU. The node agent applies the resulting plan via kvctl/sleep.
package kvbudget

import "sort"

// SharingPolicy selects how a model shares KV with co-tenants.
type SharingPolicy string

const (
	// PolicyExclusive hard-partitions a fixed KV slice (min == max, no borrow).
	PolicyExclusive SharingPolicy = "Exclusive"
	// PolicyShared borrows from the shared pool up to the ceiling, fully elastic
	// and reclaimable (min is 0 unless set).
	PolicyShared SharingPolicy = "Shared"
	// PolicyGuaranteed protects min and bursts into the shared pool up to max.
	PolicyGuaranteed SharingPolicy = "Guaranteed"
)

// ModelDemand describes one model's KV requirements for a single GPU.
type ModelDemand struct {
	Name   string
	Policy SharingPolicy
	// Min is the guaranteed KV floor in bytes (never reclaimed).
	Min int64
	// Max is the KV ceiling in bytes; 0 means "up to capacity".
	Max int64
	// Shares weights the division of the burst pool (default 1 when <= 0).
	Shares int64
	// Priority orders eviction: lower priority is evicted first.
	Priority int32
	// Demand is the model's current desired KV in bytes; it will not use more.
	Demand int64
}

// Plan is the computed allocation for one GPU.
type Plan struct {
	// Budgets is the KV ceiling (bytes) to enforce per model via kvctl.
	Budgets map[string]int64
	// Evict lists models whose weights must be evicted (sleep) because the
	// protected floors of all models do not fit in capacity. Ordered by the
	// eviction sequence (lowest priority first).
	Evict []string
}

// effective returns a copy with defaults applied (ceiling resolved, shares >= 1,
// min clamped to [0, ceiling]).
func (m ModelDemand) effective(capacity int64) ModelDemand {
	if m.Max <= 0 || m.Max > capacity {
		m.Max = capacity
	}
	if m.Policy == PolicyExclusive {
		// Exclusive pins the slice at its floor (or ceiling if no floor set).
		if m.Min <= 0 {
			m.Min = m.Max
		}
		m.Max = m.Min
	}
	if m.Min < 0 {
		m.Min = 0
	}
	if m.Min > m.Max {
		m.Min = m.Max
	}
	if m.Shares <= 0 {
		m.Shares = 1
	}
	if m.Demand < 0 {
		m.Demand = 0
	}
	return m
}

// Compute allocates KV budgets across models on one GPU. capacity is the total
// KV-able bytes; headroom is reserved and never allocated (mirrors kvcached's
// (1-gpu_utilization) headroom). The returned Plan gives a budget for every
// non-evicted model such that the sum of budgets never exceeds capacity-headroom
// (no overcommit), each model gets at least min(floor, demand-aware) and bursts
// by share up to its ceiling and demand.
func Compute(capacity, headroom int64, models []ModelDemand) Plan {
	avail := capacity - headroom
	if avail < 0 {
		avail = 0
	}

	eff := make([]ModelDemand, len(models))
	for i, m := range models {
		eff[i] = m.effective(capacity)
	}

	// Evict lowest-priority models until the protected floors fit.
	kept, evicted := fitGuarantees(eff, avail)

	budgets := make(map[string]int64, len(kept))
	var reserved int64
	for _, m := range kept {
		budgets[m.Name] = m.Min // floor is always granted
		reserved += m.Min
	}

	// Distribute the burst pool (avail - sum floors) by shares, capped at each
	// model's remaining demand and ceiling. Iterate so capped models release
	// their leftover to the others (water-filling).
	pool := avail - reserved
	distributeBurst(kept, budgets, pool)

	return Plan{Budgets: budgets, Evict: evicted}
}

// fitGuarantees drops the lowest-priority models until the sum of protected
// floors fits in avail. Returns the kept models and the evicted names (in
// eviction order: lowest priority first).
func fitGuarantees(models []ModelDemand, avail int64) ([]ModelDemand, []string) {
	var floors int64
	for _, m := range models {
		floors += m.Min
	}
	if floors <= avail {
		return models, nil
	}

	// Evict lowest priority first; break ties by larger floor (frees more).
	order := make([]int, len(models))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ma, mb := models[order[a]], models[order[b]]
		if ma.Priority != mb.Priority {
			return ma.Priority < mb.Priority
		}
		return ma.Min > mb.Min
	})

	evictedIdx := map[int]bool{}
	var evicted []string
	for _, idx := range order {
		if floors <= avail {
			break
		}
		evictedIdx[idx] = true
		evicted = append(evicted, models[idx].Name)
		floors -= models[idx].Min
	}

	kept := make([]ModelDemand, 0, len(models)-len(evicted))
	for i, m := range models {
		if !evictedIdx[i] {
			kept = append(kept, m)
		}
	}
	return kept, evicted
}

// distributeBurst water-fills `pool` bytes across models in proportion to their
// shares, capped at each model's remaining want (min(demand, ceiling) - floor).
// budgets is seeded with each model's floor and grown in place.
func distributeBurst(models []ModelDemand, budgets map[string]int64, pool int64) {
	if pool <= 0 {
		return
	}
	// want[name] = additional bytes the model can still use beyond its floor.
	want := make(map[string]int64, len(models))
	shares := make(map[string]int64, len(models))
	for _, m := range models {
		ceiling := m.Max
		target := m.Demand
		if target > ceiling {
			target = ceiling
		}
		w := target - budgets[m.Name]
		if w > 0 {
			want[m.Name] = w
			shares[m.Name] = m.Shares
		}
	}

	// Iterate: each round splits the remaining pool by shares of unsatisfied
	// models; models that hit their want are removed and their leftover recycles.
	for pool > 0 && len(want) > 0 {
		var totalShares int64
		for name := range want {
			totalShares += shares[name]
		}
		if totalShares == 0 {
			break
		}

		granted := int64(0)
		// Deterministic order for stable rounding.
		names := make([]string, 0, len(want))
		for name := range want {
			names = append(names, name)
		}
		sort.Strings(names)

		progressed := false
		for _, name := range names {
			grant := pool * shares[name] / totalShares
			if grant <= 0 {
				continue
			}
			if grant >= want[name] {
				grant = want[name]
			}
			budgets[name] += grant
			want[name] -= grant
			granted += grant
			if want[name] <= 0 {
				delete(want, name)
			}
			progressed = true
		}
		pool -= granted
		if !progressed {
			// Pool too small to split by shares; hand the remainder to the
			// highest-share unsatisfied model to avoid stranding bytes.
			best := ""
			for _, name := range names {
				if best == "" || shares[name] > shares[best] {
					best = name
				}
			}
			if best == "" {
				break
			}
			grant := pool
			if grant > want[best] {
				grant = want[best]
			}
			budgets[best] += grant
			pool -= grant
			break
		}
	}
}
