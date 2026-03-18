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
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/vllm-project/aibrix/pkg/utils"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	engineVLLM   = "vllm"
	engineSGLang = "sglang"

	sglangBootstrapPort           = int64(8998)
	sglangBootstrapPortAnnotation = "model.aibrix.ai/sglang-bootstrap-port"
	engineLabelKey                = "model.aibrix.ai/engine"
	roleSetLabel                  = "roleset-name"
	roleLabel                     = "role-name"
	podGroupIndexLabel            = "stormservice.orchestration.aibrix.ai/pod-group-index"

	kvConnectorTypeSHFS = "shfs"
	kvConnectorTypeNIXL = "nixl"
)

var (
	prefillRequestTimeout = utils.LoadEnvInt("AIBRIX_PREFILL_REQUEST_TIMEOUT", 30)
	kvConnectorType       = utils.LoadEnv("AIBRIX_KV_CONNECTOR_TYPE", kvConnectorTypeSHFS)
)

// PrefillTracker manages in-flight prefill request counts per pod.
type PrefillTracker struct {
	podCounts    sync.Map // map[podName]*atomic.Int32
	requestToPod sync.Map // map[requestID]podName
}

// NewPrefillTracker creates a new PrefillTracker.
func NewPrefillTracker() *PrefillTracker {
	return &PrefillTracker{}
}

// Add registers a prefill request for the given pod.
func (t *PrefillTracker) Add(requestID, podName string) {
	ci, _ := t.podCounts.LoadOrStore(podName, &atomic.Int32{})
	ci.(*atomic.Int32).Add(1)
	t.requestToPod.Store(requestID, podName)
}

// Remove unregisters a prefill request.
func (t *PrefillTracker) Remove(requestID string) {
	podIface, ok := t.requestToPod.LoadAndDelete(requestID)
	if !ok {
		return
	}
	podName := podIface.(string)
	ci, ok := t.podCounts.Load(podName)
	if !ok {
		return
	}
	cnt := ci.(*atomic.Int32)
	if cnt.Add(-1) < 0 {
		cnt.Store(0)
	}
}

// CountForPod returns the in-flight prefill count for a pod.
func (t *PrefillTracker) CountForPod(podName string) int {
	ci, ok := t.podCounts.Load(podName)
	if !ok {
		return 0
	}
	return int(ci.(*atomic.Int32).Load())
}

// CountsForPods returns in-flight prefill counts for the given pods.
func (t *PrefillTracker) CountsForPods(pods []*v1.Pod) map[string]int {
	counts := make(map[string]int, len(pods))
	for _, pod := range pods {
		counts[pod.Name] = t.CountForPod(pod.Name)
	}
	return counts
}

// TotalInFlight returns total in-flight prefill requests across all pods.
func (t *PrefillTracker) TotalInFlight() int {
	total := 0
	t.podCounts.Range(func(_, value any) bool {
		total += int(value.(*atomic.Int32).Load())
		return true
	})
	return total
}

// getEngineType returns the LLM engine type from pod label, defaulting to vllm.
func getEngineType(pod *v1.Pod) string {
	if e, ok := pod.Labels[engineLabelKey]; ok {
		return e
	}
	return engineVLLM
}

// validateEngine checks all pods use the same engine and returns it.
func validateEngine(pods []*v1.Pod) (string, error) {
	if len(pods) == 0 {
		return "", fmt.Errorf("no pods provided")
	}
	engine := getEngineType(pods[0])
	for i := 1; i < len(pods); i++ {
		if e := getEngineType(pods[i]); e != engine {
			return "", fmt.Errorf("inconsistent engines: %s has %s, %s has %s",
				pods[0].Name, engine, pods[i].Name, e)
		}
	}
	return engine, nil
}

// isPodHTTPServer checks if pod runs the HTTP server (node_rank=0 for multi-node TP).
func isPodHTTPServer(pod *v1.Pod) bool {
	idx, ok := pod.Labels[podGroupIndexLabel]
	if !ok {
		return true // No label = single-node or old setup
	}
	return idx == "0"
}

// prefillContext abstracts the routing context fields needed by prefill execution.
type prefillContext interface {
	RequestID() string
	ReqPath() string
	Headers() map[string]string
	GetReqBody() []byte
	SetReqBody([]byte)
}

// executePrefill sends the prefill request to the selected prefill pod.
func executePrefill(
	ctx context.Context, httpClient *http.Client,
	routingCtx prefillContext, prefillPod *v1.Pod, engine string,
) error {
	payload, err := buildPrefillPayload(routingCtx, prefillPod, engine)
	if err != nil {
		return fmt.Errorf("build prefill payload: %w", err)
	}

	apiURL := fmt.Sprintf("http://%s:%d%s",
		prefillPod.Status.PodIP,
		utils.GetModelPortForPod(routingCtx.RequestID(), prefillPod),
		routingCtx.ReqPath())

	klog.InfoS("prefill_request_start",
		"request_id", routingCtx.RequestID(),
		"llm_engine", engine,
		"prefill_pod", prefillPod.Name,
		"prefill_url", apiURL)

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(prefillRequestTimeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", apiURL, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("create prefill request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Request-Id", routingCtx.RequestID())
	for k, v := range routingCtx.Headers() {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute prefill request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read prefill response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("prefill failed status %d: %s", resp.StatusCode, string(body))
	}

	// For vLLM SHFS mode, extract kv_transfer_params from response
	if engine == engineVLLM {
		var responseData map[string]any
		if err := sonic.Unmarshal(body, &responseData); err != nil {
			return fmt.Errorf("unmarshal prefill response: %w", err)
		}
		if err := updateReqBodyWithKVParams(routingCtx, responseData, prefillPod); err != nil {
			return err
		}
	}

	return nil
}

func buildPrefillPayload(ctx prefillContext, pod *v1.Pod, engine string) ([]byte, error) {
	var req map[string]any
	if err := sonic.Unmarshal(ctx.GetReqBody(), &req); err != nil {
		return nil, fmt.Errorf("unmarshal request body: %w", err)
	}

	if engine == engineSGLang {
		req["bootstrap_host"] = pod.Status.PodIP
		port := sglangBootstrapPort
		if ps, ok := pod.Annotations[sglangBootstrapPortAnnotation]; ok {
			if p, err := strconv.ParseInt(ps, 10, 64); err == nil {
				port = p
			}
		}
		req["bootstrap_port"] = port
		req["bootstrap_room"] = rand.Int63n(1<<63 - 1) //nolint:gosec

		body, err := sonic.Marshal(req)
		if err != nil {
			return nil, err
		}
		bodyCopy := make([]byte, len(body))
		copy(bodyCopy, body)
		ctx.SetReqBody(bodyCopy)
	}

	if engine == engineVLLM && kvConnectorType == kvConnectorTypeSHFS {
		req["kv_transfer_params"] = map[string]any{
			"do_remote_decode":  true,
			"do_remote_prefill": false,
			"remote_engine_id":  nil,
			"remote_block_ids":  nil,
			"remote_host":       nil,
			"remote_port":       nil,
		}
	}

	req["max_tokens"] = 1
	req["max_completion_tokens"] = 1
	req["stream"] = false
	delete(req, "stream_options")

	return sonic.Marshal(req)
}

func updateReqBodyWithKVParams(
	ctx prefillContext, responseData map[string]any, prefillPod *v1.Pod,
) error {
	if kvConnectorType == kvConnectorTypeNIXL {
		var original map[string]any
		if err := sonic.Unmarshal(ctx.GetReqBody(), &original); err != nil {
			return fmt.Errorf("unmarshal original body: %w", err)
		}
		original["disagg_prefill_resp"] = responseData
		updated, err := sonic.Marshal(original)
		if err != nil {
			return fmt.Errorf("marshal updated body: %w", err)
		}
		ctx.SetReqBody(updated)
		return nil
	}

	// SHFS mode
	kvParams, ok := responseData["kv_transfer_params"]
	if !ok {
		klog.InfoS("no kv_transfer_params in prefill response", "request_id", ctx.RequestID())
		return nil
	}
	var original map[string]any
	if err := sonic.Unmarshal(ctx.GetReqBody(), &original); err != nil {
		return fmt.Errorf("unmarshal original body: %w", err)
	}
	original["kv_transfer_params"] = kvParams
	if m, ok := kvParams.(map[string]any); ok {
		m["remote_host"] = prefillPod.Status.PodIP
	}
	updated, err := sonic.Marshal(original)
	if err != nil {
		return fmt.Errorf("marshal updated body: %w", err)
	}
	ctx.SetReqBody(updated)
	return nil
}
