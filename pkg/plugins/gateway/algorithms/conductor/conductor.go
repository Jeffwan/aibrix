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

// Package conductor implements the Conductor routing algorithm for prefill-decode
// disaggregated LLM serving, inspired by the Mooncake architecture.
//
// Reference:
//
//	Qin et al., "Mooncake: Trading More Storage for Less Computation —
//	A KVCache-centric Architecture for Serving LLM Chatbot", FAST'25.
//	https://www.usenix.org/conference/fast25/presentation/qin
//
// Mooncake's Conductor is a global scheduler that selects a (prefill, decode) instance
// pair for each request. The key scheduling objectives are:
//
//   - Prefill stage: maximize KVCache reuse (prefix cache hits) while meeting TTFT SLO
//     and balancing load across prefill instances (Mooncake §4.1, Algorithm 1).
//   - Decode stage: maximize throughput by packing tokens into decode batches while
//     keeping TBT within SLO and GPU KVCache memory within VRAM limits (Mooncake §3.1, Figure 2).
//
// The routing flow follows Mooncake's four-step workflow (§3.1, Figure 3):
//  1. KVCache Reuse — match prefix cache on prefill candidates, load reusable KVCache.
//  2. Incremental Prefill — execute prefill on the selected instance, store new KVCache.
//  3. KVCache Transfer — stream KVCache layer-by-layer to the decode instance.
//  4. Decoding — the request joins continuous batching on the decode instance.
//
// This implementation adapts the paper's algorithm to AIBrix's gateway plugin model,
// where the conductor runs as a routing algorithm inside the Envoy ext_proc gateway
// rather than as a standalone central scheduler.
package conductor

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/metrics"
	"github.com/vllm-project/aibrix/pkg/types"
	"github.com/vllm-project/aibrix/pkg/utils"
	"github.com/vllm-project/aibrix/pkg/utils/prefixcacheindexer"
	"github.com/vllm-project/aibrix/pkg/utils/tokenizer"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

var tokenizerType = utils.LoadEnv("AIBRIX_PREFIX_CACHE_TOKENIZER_TYPE", "character")

type conductorRouter struct {
	cache          cache.Cache
	tokenizer      tokenizer.Tokenizer
	prefixIndexer  *prefixcacheindexer.PrefixHashTable
	prefillTracker *PrefillTracker
	httpClient     *http.Client
	prefixUpdateCh chan prefixUpdateJob
	defaultConfig  *ConductorConfig
}

type prefixUpdateJob struct {
	hashes []uint64
	model  string
	pod    string
}

// NewConductorRouter creates a new conductor router implementing types.Router.
func NewConductorRouter() (types.Router, error) {
	var tok tokenizer.Tokenizer
	if tokenizerType == "tiktoken" {
		tok = tokenizer.NewTiktokenTokenizer()
	} else {
		tok = tokenizer.NewCharacterTokenizer()
	}

	c, err := cache.Get()
	if err != nil {
		return nil, fmt.Errorf("conductor: failed to get cache: %w", err)
	}

	r := &conductorRouter{
		cache:         c,
		tokenizer:     tok,
		prefixIndexer: prefixcacheindexer.GetSharedPrefixHashTable(),
		prefillTracker: NewPrefillTracker(),
		httpClient: &http.Client{
			Timeout: time.Duration(prefillRequestTimeout) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		prefixUpdateCh: make(chan prefixUpdateJob, 1024),
		defaultConfig:  DefaultConfig(),
	}

	go r.startPrefixUpdater()
	return r, nil
}

// Route implements the KVCache-centric scheduling algorithm from Mooncake (§4.1, Algorithm 1).
//
// For each request it:
//  1. Classifies pods into prefill and decode pools by role-name label.
//  2. Tokenizes the prompt and queries the prefix cache index to find prefix match
//     lengths per prefill pod (Mooncake step 1: "FindBestPrefixMatch").
//  3. Scores each prefill pod by estimated TTFT = T_queue + T_prefill, where T_prefill
//     is reduced by prefix cache hits (Mooncake steps 12-13).
//  4. Scores each decode pod by estimated TBT after adding this request.
//  5. Applies early rejection if predicted TTFT or TBT exceeds SLO thresholds,
//     or if in-flight prefills would overload decode capacity (Mooncake step 18-19).
//  6. Executes prefill on the selected pod and transfers KVCache to the decode pod.
//  7. Routes the decode request to the selected decode pod.
func (r *conductorRouter) Route(ctx *types.RoutingContext, readyPodList types.PodList) (string, error) {
	allPods := readyPodList.All()

	engine, err := validateEngine(allPods)
	if err != nil {
		return "", fmt.Errorf("conductor: engine validation failed: %w", err)
	}

	cfg := r.resolveConfig(ctx)

	// Classify pods into prefill and decode
	prefillPods, decodePods := r.classifyPods(allPods)
	if len(prefillPods) == 0 {
		return "", fmt.Errorf("conductor: no prefill pods ready")
	}
	if len(decodePods) == 0 {
		return "", fmt.Errorf("conductor: no decode pods ready")
	}

	// Tokenize and match prefix cache
	tokens, _ := r.tokenizer.TokenizeInputText(ctx.Message)
	readyPodsMap := make(map[string]struct{}, len(prefillPods))
	for _, pod := range prefillPods {
		readyPodsMap[pod.Name] = struct{}{}
	}
	matchedPods, prefixHashes := r.prefixIndexer.MatchPrefix(tokens, ctx.Model, readyPodsMap)

	promptLen := len(tokens)
	if promptLen == 0 {
		promptLen = len(ctx.Message) // fallback to character count
	}

	// Score prefill pods by estimated TTFT
	var bestPrefillPod *v1.Pod
	bestTTFT := math.MaxFloat64
	prefillCounts := r.prefillTracker.CountsForPods(prefillPods)

	for _, pod := range prefillPods {
		prefixMatchLen := matchedPods[pod.Name]
		matchTokens := int(float64(prefixMatchLen) / 100.0 * float64(promptLen))
		queueDepth := prefillCounts[pod.Name]

		avgPrefillMs := r.getAvgPrefillTimeMs(pod, ctx.Model, cfg)
		ttft := scorePrefillPod(cfg, promptLen, matchTokens, queueDepth, avgPrefillMs)

		klog.V(4).InfoS("conductor_prefill_score",
			"request_id", ctx.RequestID, "pod", pod.Name,
			"ttft_ms", ttft, "prefix_match_pct", prefixMatchLen,
			"queue_depth", queueDepth)

		if ttft < bestTTFT {
			bestTTFT = ttft
			bestPrefillPod = pod
		}
	}

	// Score decode pods by estimated TBT
	var bestDecodePod *v1.Pod
	bestTBT := math.MaxFloat64
	totalDecodeRunning := 0.0

	for _, pod := range decodePods {
		currentTBT := r.getInterTokenLatencyMs(pod, ctx.Model)
		runningReqs := r.getRunningRequests(pod)
		gpuCache := r.getGPUCacheUsage(pod, ctx.Model)
		totalDecodeRunning += float64(runningReqs)

		tbt := scoreDecodePod(cfg, currentTBT, runningReqs, gpuCache)

		klog.V(4).InfoS("conductor_decode_score",
			"request_id", ctx.RequestID, "pod", pod.Name,
			"tbt_ms", tbt, "running_reqs", runningReqs,
			"gpu_cache", gpuCache)

		if tbt < bestTBT {
			bestTBT = tbt
			bestDecodePod = pod
		}
	}

	// Early rejection check
	decodeCapacity := totalDecodeRunning + float64(len(decodePods))
	inFlightPrefills := r.prefillTracker.TotalInFlight()
	if reject, reason := shouldReject(
		cfg, bestTTFT, bestTBT, inFlightPrefills, decodeCapacity,
	); reject {
		klog.InfoS("conductor_rejected", "request_id", ctx.RequestID, "reason", reason)
		return "", fmt.Errorf("conductor: request rejected: %s", reason)
	}

	// Execute prefill
	if ctx.RespHeaders == nil {
		ctx.RespHeaders = make(map[string]string)
	}
	ctx.RespHeaders["prefill-target-pod"] = bestPrefillPod.Name
	ctx.RespHeaders["prefill-target-pod-ip"] = bestPrefillPod.Status.PodIP

	r.prefillTracker.Add(ctx.RequestID, bestPrefillPod.Name)
	ctx.PrefillStartTime = time.Now()

	adapter := &routingContextAdapter{ctx: ctx}

	switch engine {
	case engineSGLang:
		go func() {
			defer r.prefillTracker.Remove(ctx.RequestID)
			if err := executePrefill(ctx.Context, r.httpClient, adapter, bestPrefillPod, engine); err != nil {
				klog.ErrorS(err, "conductor_prefill_failed", "request_id", ctx.RequestID)
			}
			ctx.PrefillEndTime = time.Now()
		}()
	default: // vLLM and others: synchronous
		defer r.prefillTracker.Remove(ctx.RequestID)
		if err := executePrefill(ctx.Context, r.httpClient, adapter, bestPrefillPod, engine); err != nil {
			return "", fmt.Errorf("conductor: prefill failed: %w", err)
		}
		ctx.PrefillEndTime = time.Now()
	}

	// Update prefix cache
	if len(prefixHashes) > 0 {
		r.enqueuePrefixUpdate(prefixHashes, ctx.Model, bestPrefillPod.Name)
	}

	ctx.SetTargetPod(bestDecodePod)

	klog.InfoS("conductor_routed",
		"request_id", ctx.RequestID,
		"prefill_pod", bestPrefillPod.Name,
		"decode_pod", bestDecodePod.Name,
		"ttft_ms", bestTTFT,
		"tbt_ms", bestTBT)

	return ctx.TargetAddress(), nil
}

// SubscribedMetrics returns the metrics this router needs.
func (r *conductorRouter) SubscribedMetrics() []string {
	return []string{
		metrics.InterTokenLatencySeconds,
		metrics.NumRequestsRunning,
		metrics.GPUCacheUsagePerc,
		metrics.RequestPrefillTimeSeconds,
	}
}

// --- Prediction (Mooncake §4.1) ---

// estimatePrefillTimeMs estimates prefill execution time in milliseconds.
// Uses model: T = A * tokens^B + C (superlinear due to attention complexity).
func estimatePrefillTimeMs(cfg *ConductorConfig, tokensToProcess int) float64 {
	if tokensToProcess <= 0 {
		return cfg.PrefillTimeCoeffC
	}
	return cfg.PrefillTimeCoeffA*math.Pow(float64(tokensToProcess), cfg.PrefillTimeCoeffB) + cfg.PrefillTimeCoeffC
}

// estimateQueueTimeMs estimates queue waiting time in milliseconds.
func estimateQueueTimeMs(queuedRequests int, avgPrefillTimeMs float64) float64 {
	if queuedRequests <= 0 {
		return 0
	}
	return float64(queuedRequests) * avgPrefillTimeMs
}

// estimateTBTAfterAddingRequest estimates TBT after adding one request to a decode pod.
// TBT increases sublinearly with batch size.
func estimateTBTAfterAddingRequest(currentTBTMs float64, runningRequests int) float64 {
	if currentTBTMs <= 0 {
		return 0
	}
	return currentTBTMs * (1.0 + 1.0/math.Max(float64(runningRequests), 1.0))
}

// --- Scoring (Mooncake Algorithm 1, lines 12-17) ---

// scorePrefillPod returns estimated TTFT in ms (lower is better).
// TTFT = T_queue + T_prefill, where T_prefill is reduced by prefix cache hits.
func scorePrefillPod(cfg *ConductorConfig, promptLen, prefixMatchLen, queuedRequests int, avgPrefillTimeMs float64) float64 {
	tokensToProcess := max(promptLen-prefixMatchLen, 0)
	tQueue := estimateQueueTimeMs(queuedRequests, avgPrefillTimeMs)
	tPrefill := estimatePrefillTimeMs(cfg, tokensToProcess)
	return tQueue + tPrefill
}

// scoreDecodePod returns estimated TBT in ms after adding a request (lower is better).
func scoreDecodePod(cfg *ConductorConfig, currentTBTMs float64, runningRequests int, gpuCacheUsage float64) float64 {
	_ = cfg // reserved for future config-driven scoring adjustments
	estimatedTBT := estimateTBTAfterAddingRequest(currentTBTMs, runningRequests)
	// GPU cache pressure penalty: 1.5x when >90% utilized
	if gpuCacheUsage > 0.9 {
		estimatedTBT *= 1.5
	}
	return estimatedTBT
}

// --- Rejection ---

// shouldReject checks if a request should be rejected based on predicted TTFT, TBT,
// and decode pool load.
func shouldReject(
	cfg *ConductorConfig,
	predictedTTFTMs, predictedTBTMs float64,
	inFlightPrefills int, decodeCapacity float64,
) (bool, string) {
	if !cfg.EnableEarlyRejection {
		return false, ""
	}

	if predictedTTFTMs > cfg.TTFTSLOMs {
		return true, fmt.Sprintf(
			"predicted TTFT %.0fms exceeds SLO %.0fms",
			predictedTTFTMs, cfg.TTFTSLOMs)
	}

	if predictedTBTMs > cfg.TBTSLOMs {
		return true, fmt.Sprintf(
			"predicted TBT %.0fms exceeds SLO %.0fms",
			predictedTBTMs, cfg.TBTSLOMs)
	}

	// Prediction-based decode overload: estimate future decode load
	// by accounting for in-flight prefills that will become decode requests.
	if decodeCapacity > 0 {
		predictedLoad := float64(inFlightPrefills) / decodeCapacity
		if predictedLoad >= cfg.DecodeLoadRejectThreshold {
			return true, fmt.Sprintf(
				"predicted decode load %.2f exceeds threshold %.2f",
				predictedLoad, cfg.DecodeLoadRejectThreshold)
		}
	}

	return false, ""
}

// --- Config resolution ---

// resolveConfig parses ConductorConfig from the routing context's RoutingConfig,
// falling back to defaults if not present.
func (r *conductorRouter) resolveConfig(ctx *types.RoutingContext) *ConductorConfig {
	if ctx.ConfigProfile == nil || len(ctx.ConfigProfile.RoutingConfig) == 0 {
		return r.defaultConfig
	}
	cfg, err := ParseConfig(ctx.ConfigProfile.RoutingConfig)
	if err != nil {
		klog.ErrorS(err, "conductor: failed to parse algorithm config, using defaults")
		return r.defaultConfig
	}
	return cfg
}

// --- Pod classification ---

// classifyPods separates pods into prefill and decode based on role-name label.
func (r *conductorRouter) classifyPods(pods []*v1.Pod) ([]*v1.Pod, []*v1.Pod) {
	var prefill, decode []*v1.Pod
	for _, pod := range pods {
		if _, ok := pod.Labels[roleSetLabel]; !ok {
			continue
		}
		if !isPodHTTPServer(pod) {
			continue
		}
		switch pod.Labels[roleLabel] {
		case "prefill":
			prefill = append(prefill, pod)
		case "decode":
			decode = append(decode, pod)
		}
	}
	return prefill, decode
}

// --- Metric helpers ---

func (r *conductorRouter) getAvgPrefillTimeMs(pod *v1.Pod, model string, cfg *ConductorConfig) float64 {
	v, err := r.cache.GetMetricValueByPodModel(pod.Name, pod.Namespace, model, metrics.RequestPrefillTimeSeconds)
	if err != nil {
		return cfg.PrefillTimeCoeffC + cfg.PrefillTimeCoeffA*math.Pow(512, cfg.PrefillTimeCoeffB)
	}
	return v.GetSimpleValue() * 1000 // seconds to ms
}

func (r *conductorRouter) getInterTokenLatencyMs(pod *v1.Pod, model string) float64 {
	v, err := r.cache.GetMetricValueByPodModel(pod.Name, pod.Namespace, model, metrics.InterTokenLatencySeconds)
	if err != nil {
		return 20.0 // reasonable default TBT in ms
	}
	return v.GetSimpleValue() * 1000
}

func (r *conductorRouter) getRunningRequests(pod *v1.Pod) int {
	v, err := r.cache.GetMetricValueByPod(pod.Name, pod.Namespace, metrics.RealtimeNumRequestsRunning)
	if err != nil {
		return 0
	}
	return int(v.GetSimpleValue())
}

func (r *conductorRouter) getGPUCacheUsage(pod *v1.Pod, model string) float64 {
	v, err := r.cache.GetMetricValueByPodModel(pod.Name, pod.Namespace, model, metrics.GPUCacheUsagePerc)
	if err != nil {
		return 0
	}
	return v.GetSimpleValue()
}

// --- Prefix cache updater ---

func (r *conductorRouter) startPrefixUpdater() {
	for job := range r.prefixUpdateCh {
		r.prefixIndexer.AddPrefix(job.hashes, job.model, job.pod)
	}
}

func (r *conductorRouter) enqueuePrefixUpdate(hashes []uint64, model, pod string) {
	copyHashes := slices.Clone(hashes)
	select {
	case r.prefixUpdateCh <- prefixUpdateJob{hashes: copyHashes, model: model, pod: pod}:
	default:
		klog.Warningf("conductor: prefix update channel full, dropping update")
	}
}

// routingContextAdapter adapts types.RoutingContext to the prefillContext interface.
type routingContextAdapter struct {
	ctx *types.RoutingContext
}

func (a *routingContextAdapter) RequestID() string         { return a.ctx.RequestID }
func (a *routingContextAdapter) ReqPath() string           { return a.ctx.ReqPath }
func (a *routingContextAdapter) Headers() map[string]string { return a.ctx.ReqHeaders }
func (a *routingContextAdapter) GetReqBody() []byte        { return a.ctx.ReqBody }
func (a *routingContextAdapter) SetReqBody(b []byte)       { a.ctx.ReqBody = b }
