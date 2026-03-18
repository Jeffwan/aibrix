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

package conductor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/metrics"
	"github.com/vllm-project/aibrix/pkg/types"
	"github.com/vllm-project/aibrix/pkg/utils"
	"github.com/vllm-project/aibrix/pkg/utils/prefixcacheindexer"
	"github.com/vllm-project/aibrix/pkg/utils/tokenizer"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- Prediction tests ---

func TestEstimatePrefillTime(t *testing.T) {
	cfg := DefaultConfig() // A=0.001, B=1.5, C=5.0

	assert.InDelta(t, 5.0, estimatePrefillTimeMs(cfg, 0), 0.01)
	assert.InDelta(t, 36.62, estimatePrefillTimeMs(cfg, 1000), 1.0)
	assert.InDelta(t, 5.0, estimatePrefillTimeMs(cfg, -10), 0.01)
}

func TestEstimateQueueTimeMs(t *testing.T) {
	assert.Equal(t, 0.0, estimateQueueTimeMs(0, 50.0))
	assert.InDelta(t, 150.0, estimateQueueTimeMs(3, 50.0), 0.01)
}

func TestEstimateTBTMs(t *testing.T) {
	tbt := estimateTBTAfterAddingRequest(10.0, 5)
	assert.Greater(t, tbt, 10.0)
	assert.Less(t, tbt, 15.0)

	tbt = estimateTBTAfterAddingRequest(10.0, 0)
	assert.Greater(t, tbt, 10.0)
}

// --- Scoring tests ---

func TestScorePrefillPod(t *testing.T) {
	cfg := DefaultConfig()

	ttft := scorePrefillPod(cfg, 1000, 500, 2, 30.0)
	assert.Greater(t, ttft, 0.0)

	ttftBetter := scorePrefillPod(cfg, 1000, 800, 2, 30.0)
	assert.Less(t, ttftBetter, ttft)

	ttftBusy := scorePrefillPod(cfg, 1000, 500, 10, 30.0)
	assert.Greater(t, ttftBusy, ttft)
}

func TestScoreDecodePod(t *testing.T) {
	cfg := DefaultConfig()

	tbt := scoreDecodePod(cfg, 10.0, 5, 0.5)
	assert.Greater(t, tbt, 10.0)

	tbtPressure := scoreDecodePod(cfg, 10.0, 5, 0.95)
	assert.Greater(t, tbtPressure, tbt)

	// More running requests = less marginal impact per added request
	tbtMore := scoreDecodePod(cfg, 10.0, 20, 0.5)
	assert.Less(t, tbtMore, tbt)
}

// --- Rejection tests ---

func TestShouldRejectDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EnableEarlyRejection = false
	reject, _ := shouldReject(cfg, 50000, 300, 5, 10.0)
	assert.False(t, reject)
}

func TestShouldRejectTTFTExceeded(t *testing.T) {
	cfg := DefaultConfig()
	reject, reason := shouldReject(cfg, 35000, 100, 0, 10.0)
	assert.True(t, reject)
	assert.Contains(t, reason, "TTFT")
}

func TestShouldRejectTBTExceeded(t *testing.T) {
	cfg := DefaultConfig()
	reject, reason := shouldReject(cfg, 5000, 250, 0, 10.0)
	assert.True(t, reject)
	assert.Contains(t, reason, "TBT")
}

func TestShouldRejectDecodeOverload(t *testing.T) {
	cfg := DefaultConfig()
	reject, reason := shouldReject(cfg, 5000, 100, 9, 10.0)
	assert.True(t, reject)
	assert.Contains(t, reason, "decode load")
}

func TestShouldAccept(t *testing.T) {
	cfg := DefaultConfig()
	reject, _ := shouldReject(cfg, 5000, 100, 2, 20.0)
	assert.False(t, reject)
}

// --- Router tests ---

func makePod(name, ip, role, roleset string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"model.aibrix.ai/name":   "test-model",
				"model.aibrix.ai/port":   "8000",
				"model.aibrix.ai/engine": "vllm",
				roleLabel:                role,
				roleSetLabel:             roleset,
			},
		},
		Status: v1.PodStatus{
			PodIP: ip,
			Conditions: []v1.PodCondition{
				{Type: v1.PodReady, Status: v1.ConditionTrue},
			},
		},
	}
}

func TestClassifyPods(t *testing.T) {
	pods := []*v1.Pod{
		makePod("prefill-0", "10.0.0.1", "prefill", "rs-0"),
		makePod("prefill-1", "10.0.0.2", "prefill", "rs-0"),
		makePod("decode-0", "10.0.0.3", "decode", "rs-0"),
		makePod("decode-1", "10.0.0.4", "decode", "rs-0"),
	}

	r := &conductorRouter{}
	prefill, decode := r.classifyPods(pods)
	assert.Len(t, prefill, 2)
	assert.Len(t, decode, 2)
}

func TestClassifyPodsSkipsMissingLabels(t *testing.T) {
	pods := []*v1.Pod{
		makePod("prefill-0", "10.0.0.1", "prefill", "rs-0"),
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "no-roleset",
				Labels: map[string]string{
					roleLabel: "prefill",
				},
			},
		},
	}
	r := &conductorRouter{}
	prefill, decode := r.classifyPods(pods)
	assert.Len(t, prefill, 1)
	assert.Len(t, decode, 0)
}

func TestResolveConfigDefault(t *testing.T) {
	r := &conductorRouter{defaultConfig: DefaultConfig()}
	ctx := types.NewRoutingContext(
		context.Background(), RouterConductor, "model", "", "id", "",
	)
	defer ctx.Delete()
	cfg := r.resolveConfig(ctx)
	assert.Equal(t, float64(30000), cfg.TTFTSLOMs)
}

func TestResolveConfigFromProfile(t *testing.T) {
	r := &conductorRouter{defaultConfig: DefaultConfig()}
	ctx := types.NewRoutingContext(
		context.Background(), RouterConductor, "model", "", "id", "",
	)
	defer ctx.Delete()
	ctx.ConfigProfile = &types.ResolvedConfigProfile{
		RoutingConfig: []byte(`{"ttftSloMs": 5000, "tbtSloMs": 50}`),
	}
	cfg := r.resolveConfig(ctx)
	assert.Equal(t, float64(5000), cfg.TTFTSLOMs)
	assert.Equal(t, float64(50), cfg.TBTSLOMs)
}

func TestRouteSelectsLeastLoadedPods(t *testing.T) {
	pods := []*v1.Pod{
		makePod("prefill-0", "10.0.0.1", "prefill", "rs-0"),
		makePod("decode-0", "10.0.0.3", "decode", "rs-0"),
		makePod("decode-1", "10.0.0.4", "decode", "rs-0"),
	}

	model := "test-model"
	podMetrics := map[string]map[string]metrics.MetricValue{
		"decode-0": {
			metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 10},
			metrics.InterTokenLatencySeconds:   &metrics.SimpleMetricValue{Value: 0.05},
			metrics.GPUCacheUsagePerc:          &metrics.SimpleMetricValue{Value: 0.5},
		},
		"decode-1": {
			metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 1},
			metrics.InterTokenLatencySeconds:   &metrics.SimpleMetricValue{Value: 0.01},
			metrics.GPUCacheUsagePerc:          &metrics.SimpleMetricValue{Value: 0.2},
		},
		"prefill-0": {
			metrics.RequestPrefillTimeSeconds: &metrics.SimpleMetricValue{Value: 0.05},
		},
	}
	c := cache.NewWithPodsMetricsForTest(pods, model, podMetrics)

	r := &conductorRouter{
		cache:          c,
		tokenizer:      tokenizer.NewCharacterTokenizer(),
		prefixIndexer:  prefixcacheindexer.NewPrefixHashTable(),
		prefillTracker: NewPrefillTracker(),
		httpClient:     &http.Client{Timeout: 100 * time.Millisecond},
		defaultConfig:  DefaultConfig(),
		prefixUpdateCh: make(chan prefixUpdateJob, 10),
	}

	ctx := types.NewRoutingContext(
		context.Background(), RouterConductor, model, "hello world", "req-1", "",
	)
	defer ctx.Delete()
	ctx.ReqPath = "/v1/chat/completions"
	ctx.ReqHeaders = map[string]string{}
	ctx.ReqBody = []byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`)

	podList := &utils.PodArray{Pods: c.ListPods()}

	_, err := r.Route(ctx, podList)
	require.Error(t, err) // prefill HTTP fails (no server)
	assert.Equal(t, "prefill-0", ctx.RespHeaders["prefill-target-pod"])
}

func TestPrefillTracker(t *testing.T) {
	tracker := NewPrefillTracker()

	tracker.Add("req-1", "pod-a")
	tracker.Add("req-2", "pod-a")
	tracker.Add("req-3", "pod-b")

	assert.Equal(t, 2, tracker.CountForPod("pod-a"))
	assert.Equal(t, 1, tracker.CountForPod("pod-b"))
	assert.Equal(t, 3, tracker.TotalInFlight())

	tracker.Remove("req-1")
	assert.Equal(t, 1, tracker.CountForPod("pod-a"))
	assert.Equal(t, 2, tracker.TotalInFlight())

	tracker.Remove("req-99") // safe no-op
	assert.Equal(t, 2, tracker.TotalInFlight())
}
