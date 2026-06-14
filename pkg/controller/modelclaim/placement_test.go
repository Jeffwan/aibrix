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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	modelv1alpha1 "github.com/vllm-project/aibrix/api/model/v1alpha1"
	"github.com/vllm-project/aibrix/pkg/controller/modelclaim/kvbudget"
)

func namedPod(name string) corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestSelectPodForActivation_LeastLoaded(t *testing.T) {
	cands := []corev1.Pod{namedPod("a"), namedPod("b"), namedPod("c")}
	load := map[string]int{"a": 2, "b": 0, "c": 1}
	got, err := selectPodForActivation(cands, map[string]bool{}, load, "m", uniformLocality{})
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name)
}

func TestSelectPodForActivation_SkipsAlreadyOn(t *testing.T) {
	cands := []corev1.Pod{namedPod("a"), namedPod("b")}
	load := map[string]int{"a": 0, "b": 5}
	got, err := selectPodForActivation(cands, map[string]bool{"a": true}, load, "m", uniformLocality{})
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name, "a is excluded even though least loaded")
}

func TestSelectPodForActivation_TieBreakByName(t *testing.T) {
	cands := []corev1.Pod{namedPod("z"), namedPod("a")}
	load := map[string]int{"z": 0, "a": 0}
	got, err := selectPodForActivation(cands, map[string]bool{}, load, "m", uniformLocality{})
	require.NoError(t, err)
	assert.Equal(t, "a", got.Name)
}

func TestSelectPodForActivation_NoCapacity(t *testing.T) {
	cands := []corev1.Pod{namedPod("a")}
	_, err := selectPodForActivation(cands, map[string]bool{"a": true}, map[string]int{}, "m", uniformLocality{})
	assert.Error(t, err)
}

func TestKVBudgetBytes(t *testing.T) {
	pm := &modelv1alpha1.ModelClaim{Spec: modelv1alpha1.ModelClaimSpec{
		AdditionalConfig: map[string]string{ConfigKeyKVMin: "2Gi", ConfigKeyKVMax: "8Gi"},
	}}
	kvMin, kvMax := kvBudgetBytes(pm)
	assert.Equal(t, int64(2*1024*1024*1024), kvMin)
	assert.Equal(t, int64(8*1024*1024*1024), kvMax)
}

func TestKVBudgetBytes_AbsentOrBad(t *testing.T) {
	pm := &modelv1alpha1.ModelClaim{Spec: modelv1alpha1.ModelClaimSpec{
		AdditionalConfig: map[string]string{ConfigKeyKVMin: "not-a-size"},
	}}
	kvMin, kvMax := kvBudgetBytes(pm)
	assert.Zero(t, kvMin)
	assert.Zero(t, kvMax)
}

func TestServedModelName(t *testing.T) {
	pm := &modelv1alpha1.ModelClaim{ObjectMeta: metav1.ObjectMeta{Name: "foo"}}
	assert.Equal(t, "foo", servedModelName(pm))
	name := "bar"
	pm.Spec.ModelName = &name
	assert.Equal(t, "bar", servedModelName(pm))
}

func TestIpcNameFor(t *testing.T) {
	pm := &modelv1alpha1.ModelClaim{ObjectMeta: metav1.ObjectMeta{Name: "foo"}}
	assert.Equal(t, "kvc_foo", ipcNameFor(pm))

	// Sanitized to match kvcached's normalization (verified on real hardware):
	// '.' and '/' become '-', existing '-' is kept.
	dotted := &modelv1alpha1.ModelClaim{ObjectMeta: metav1.ObjectMeta{Name: "qwen3-0.6b"}}
	assert.Equal(t, "kvc_qwen3-0-6b", ipcNameFor(dotted))
	slashed := &modelv1alpha1.ModelClaim{ObjectMeta: metav1.ObjectMeta{Name: "Qwen/Qwen2-7B"}}
	assert.Equal(t, "kvc_Qwen-Qwen2-7B", ipcNameFor(slashed))
}

func TestDesiredReplicas(t *testing.T) {
	pm := &modelv1alpha1.ModelClaim{}
	assert.Equal(t, int32(1), desiredReplicas(pm))
	three := int32(3)
	pm.Spec.Replicas = &three
	assert.Equal(t, int32(3), desiredReplicas(pm))
}

func TestKVBudgetBytes_FromSpecKV(t *testing.T) {
	kvMin := resource.MustParse("2Gi")
	kvMax := resource.MustParse("8Gi")
	pm := &modelv1alpha1.ModelClaim{Spec: modelv1alpha1.ModelClaimSpec{
		KV: &modelv1alpha1.KVConfig{Min: &kvMin, Max: &kvMax},
	}}
	gotMin, gotMax := kvBudgetBytes(pm)
	assert.Equal(t, int64(2*1024*1024*1024), gotMin)
	assert.Equal(t, int64(8*1024*1024*1024), gotMax)
}

func TestKVBudgetBytes_SpecKVOverridesAdditionalConfig(t *testing.T) {
	kvMin := resource.MustParse("1Gi")
	pm := &modelv1alpha1.ModelClaim{Spec: modelv1alpha1.ModelClaimSpec{
		KV:               &modelv1alpha1.KVConfig{Min: &kvMin},
		AdditionalConfig: map[string]string{ConfigKeyKVMin: "9Gi", ConfigKeyKVMax: "16Gi"},
	}}
	gotMin, gotMax := kvBudgetBytes(pm)
	assert.Equal(t, int64(1*1024*1024*1024), gotMin, "Spec.KV.Min wins")
	assert.Equal(t, int64(16*1024*1024*1024), gotMax, "falls back to AdditionalConfig for unset max")
}

func TestToModelDemand(t *testing.T) {
	kvMin := resource.MustParse("1Gi")
	kvMax := resource.MustParse("4Gi")
	name := "served"
	pm := &modelv1alpha1.ModelClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "pm1"},
		Spec: modelv1alpha1.ModelClaimSpec{
			ModelName: &name,
			Priority:  7,
			KV: &modelv1alpha1.KVConfig{
				Policy: modelv1alpha1.KVPolicyShared, Min: &kvMin, Max: &kvMax, Shares: 3,
			},
		},
	}
	d := ToModelDemand(pm, 123)
	assert.Equal(t, "served", d.Name)
	assert.Equal(t, kvbudget.PolicyShared, d.Policy)
	assert.Equal(t, int64(1<<30), d.Min)
	assert.Equal(t, int64(4*(1<<30)), d.Max)
	assert.Equal(t, int64(3), d.Shares)
	assert.Equal(t, int32(7), d.Priority)
	assert.Equal(t, int64(123), d.Demand)
}

func TestToModelDemand_Defaults(t *testing.T) {
	pm := &modelv1alpha1.ModelClaim{ObjectMeta: metav1.ObjectMeta{Name: "pm1"}}
	d := ToModelDemand(pm, 0)
	assert.Equal(t, "pm1", d.Name)
	assert.Equal(t, kvbudget.PolicyGuaranteed, d.Policy, "defaults to Guaranteed")
	assert.Equal(t, int64(1), d.Shares, "defaults to 1 share")
}

// fakeLocality maps nodeName -> load cost for tests (0 = weights already hot).
type fakeLocality map[string]float64

func (f fakeLocality) Cost(model, nodeName string) float64 { return f[nodeName] }

func podOnNode(name, node string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.PodSpec{NodeName: node},
	}
}

func TestSelectPodForActivation_LocalityDominatesLoad(t *testing.T) {
	// "hot" sits on a node whose store already has the weights (cost 0) but is
	// busier; "cold" is idle but on a node that must stage weights (cost 5).
	cands := []corev1.Pod{podOnNode("cold", "n-cold"), podOnNode("hot", "n-hot")}
	load := map[string]int{"cold": 0, "hot": 3}
	loc := fakeLocality{"n-hot": 0, "n-cold": 5}
	got, err := selectPodForActivation(cands, map[string]bool{}, load, "m", loc)
	require.NoError(t, err)
	assert.Equal(t, "hot", got.Name, "lower locality cost wins over lower load")
}

func TestSelectPodForActivation_LoadBreaksEqualLocality(t *testing.T) {
	// Two nodes equally hot (cost 0): fall back to least-loaded.
	cands := []corev1.Pod{podOnNode("a", "n1"), podOnNode("b", "n2")}
	load := map[string]int{"a": 2, "b": 1}
	loc := fakeLocality{"n1": 0, "n2": 0}
	got, err := selectPodForActivation(cands, map[string]bool{}, load, "m", loc)
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name)
}

func TestSelectPodForActivation_NilLocalityIsUniform(t *testing.T) {
	// A nil provider must not panic and must behave like load-only selection.
	cands := []corev1.Pod{podOnNode("a", "n1"), podOnNode("b", "n2")}
	load := map[string]int{"a": 5, "b": 0}
	got, err := selectPodForActivation(cands, map[string]bool{}, load, "m", nil)
	require.NoError(t, err)
	assert.Equal(t, "b", got.Name)
}

func TestUniformLocality_AlwaysZero(t *testing.T) {
	assert.Zero(t, uniformLocality{}.Cost("m", "any-node"))
}

func TestPruneDeadInstances(t *testing.T) {
	pm := &modelv1alpha1.ModelClaim{}
	pm.Status.Instances = []modelv1alpha1.ModelClaimInstance{
		{Pod: "alive", Port: 20000},
		{Pod: "gone", Port: 20001},
	}
	pruneDeadInstances(pm, []corev1.Pod{namedPod("alive")})
	require.Len(t, pm.Status.Instances, 1)
	assert.Equal(t, "alive", pm.Status.Instances[0].Pod,
		"instance on a vanished warm pod must be dropped so re-activation can run")

	// No candidates at all: every instance is stale.
	pruneDeadInstances(pm, nil)
	assert.Empty(t, pm.Status.Instances)
}
