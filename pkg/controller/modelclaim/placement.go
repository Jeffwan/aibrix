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

package modelclaim

import (
	"fmt"
	"strings"

	modelv1alpha1 "github.com/vllm-project/aibrix/api/model/v1alpha1"
	"github.com/vllm-project/aibrix/pkg/controller/modelclaim/kvbudget"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// AdditionalConfig keys understood by the controller/runtime for KV budgets.
const (
	ConfigKeyKVMin = "kv-min"
	ConfigKeyKVMax = "kv-max"
)

// servedModelName returns the model name clients address, defaulting to the
// object name when Spec.ModelName is unset.
func servedModelName(pm *modelv1alpha1.ModelClaim) string {
	if pm.Spec.ModelName != nil && *pm.Spec.ModelName != "" {
		return *pm.Spec.ModelName
	}
	return pm.Name
}

// desiredReplicas resolves the target number of active instances.
func desiredReplicas(pm *modelv1alpha1.ModelClaim) int32 {
	if pm.Spec.Replicas != nil {
		return *pm.Spec.Replicas
	}
	return 1
}

// ipcNameFor derives the kvcached shared-memory segment name for a model. It
// must be unique per GPU so co-tenant engines do not collide on /dev/shm, and
// it is sanitized to match kvcached's own normalization (it replaces characters
// like '.' and '/' with '-'); otherwise kvctl operations would target a
// different segment name than the engine actually created.
func ipcNameFor(pm *modelv1alpha1.ModelClaim) string {
	return "kvc_" + sanitizeIPCName(pm.Name)
}

// sanitizeIPCName maps any character outside [A-Za-z0-9_-] to '-', matching how
// kvcached normalizes the KVCACHED_IPC_NAME (verified on real hardware: a name
// like "kvc_qwen3-0.6b" becomes "kvc_qwen3-0-6b").
func sanitizeIPCName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// kvBudgetBytes extracts the KV floor/ceiling (bytes) from the first-class
// Spec.KV fields, falling back to the AdditionalConfig kv-min/kv-max hints for
// back-compat. Returns 0 for any value that is absent (runtime picks a default).
func kvBudgetBytes(pm *modelv1alpha1.ModelClaim) (kvMin int64, kvMax int64) {
	if pm.Spec.KV != nil {
		if pm.Spec.KV.Min != nil {
			kvMin = pm.Spec.KV.Min.Value()
		}
		if pm.Spec.KV.Max != nil {
			kvMax = pm.Spec.KV.Max.Value()
		}
	}
	if kvMin == 0 {
		if v, ok := pm.Spec.AdditionalConfig[ConfigKeyKVMin]; ok {
			if q, err := resource.ParseQuantity(v); err == nil {
				kvMin = q.Value()
			}
		}
	}
	if kvMax == 0 {
		if v, ok := pm.Spec.AdditionalConfig[ConfigKeyKVMax]; ok {
			if q, err := resource.ParseQuantity(v); err == nil {
				kvMax = q.Value()
			}
		}
	}
	return kvMin, kvMax
}

// ToModelDemand maps a ModelClaim's KV configuration to a kvbudget.ModelDemand
// for the elastic resource manager. demandBytes is the model's current observed
// KV usage (the node-local runtime supplies it from kvtop/dev-shm).
func ToModelDemand(pm *modelv1alpha1.ModelClaim, demandBytes int64) kvbudget.ModelDemand {
	kvMin, kvMax := kvBudgetBytes(pm)
	policy := kvbudget.PolicyGuaranteed
	shares := int64(1)
	if pm.Spec.KV != nil {
		if pm.Spec.KV.Policy != "" {
			policy = kvbudget.SharingPolicy(pm.Spec.KV.Policy)
		}
		if pm.Spec.KV.Shares > 0 {
			shares = pm.Spec.KV.Shares
		}
	}
	return kvbudget.ModelDemand{
		Name:     servedModelName(pm),
		Policy:   policy,
		Min:      kvMin,
		Max:      kvMax,
		Shares:   shares,
		Priority: pm.Spec.Priority,
		Demand:   demandBytes,
	}
}

// LocalityProvider estimates the relative cost of loading a model's weights on
// a node. 0 means "already hot on this node" (cheapest); a larger value means
// costlier to bring up there (staged from local NVMe, or fetched from a remote/
// HF source). It lets placement prefer nodes where the weights are already
// cached. Layer 2's node weight-cache DaemonSet supplies a real implementation;
// until then uniformLocality reports 0 for every node, so placement reduces to
// load-based bin-packing exactly as before.
type LocalityProvider interface {
	Cost(model, nodeName string) float64
}

// uniformLocality is the default: every node looks equally cheap, so locality
// never tips the decision and placement falls back to load + name ordering.
type uniformLocality struct{}

func (uniformLocality) Cost(model, nodeName string) float64 { return 0 }

// selectPodForActivation picks a warm pod to attach the model to. Among pods not
// already hosting this model, it chooses the lowest-cost pod, ranked
// lexicographically by (locality cost, current model load, name): prefer a node
// where the weights are already hot, then the least-loaded pod for density
// spread, breaking remaining ties by name for determinism.
//
// node-awareness comes for free: locality.Cost is keyed on pod.Spec.NodeName, so
// the same function generalizes from "pick a GPU on this node" (Layer 2) to
// "pick a node, then a GPU" (Layer 3) — only the LocalityProvider changes. A nil
// provider is treated as uniform (load-only), preserving Layer-1 behavior.
func selectPodForActivation(candidates []corev1.Pod, alreadyOn map[string]bool, load map[string]int, model string, locality LocalityProvider) (*corev1.Pod, error) {
	if locality == nil {
		locality = uniformLocality{}
	}
	var best *corev1.Pod
	var bestLoc float64
	var bestLoad int
	for i := range candidates {
		pod := &candidates[i]
		if alreadyOn[pod.Name] {
			continue
		}
		loc := locality.Cost(model, pod.Spec.NodeName)
		l := load[pod.Name]
		if best == nil || rankLess(loc, l, pod.Name, bestLoc, bestLoad, best.Name) {
			best, bestLoc, bestLoad = pod, loc, l
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no candidate warm pod with capacity for model")
	}
	return best, nil
}

// rankLess reports whether candidate (loc,load,name) ranks before
// (bLoc,bLoad,bName): lower locality cost first, then lower load, then lower name.
func rankLess(loc float64, load int, name string, bLoc float64, bLoad int, bName string) bool {
	if loc != bLoc {
		return loc < bLoc
	}
	if load != bLoad {
		return load < bLoad
	}
	return name < bName
}
