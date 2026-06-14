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

package gateway

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	configPb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	envoyTypePb "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"k8s.io/klog/v2"
)

const (
	envModelClaimHoldEnabled       = "AIBRIX_MODELCLAIM_HOLD_ENABLED"
	envModelClaimHoldBudgetSeconds = "AIBRIX_MODELCLAIM_HOLD_BUDGET_SECONDS"
	envModelClaimHoldMaxConcurrent = "AIBRIX_MODELCLAIM_HOLD_MAX_CONCURRENT"

	defaultModelClaimHoldBudget        = 110 * time.Second
	defaultModelClaimHoldMaxConcurrent = 256
	modelClaimHoldPollInterval         = 500 * time.Millisecond
	modelClaimRetryAfterSeconds        = 30
)

type modelClaimRoutability interface {
	HasModel(model string) bool
	IsModelClaimNotRoutable(model string) bool
}

type holdGate struct {
	enabled      bool
	budget       time.Duration
	pollInterval time.Duration
	sem          chan struct{}
}

func newHoldGate() *holdGate {
	enabled := envBoolDefault(envModelClaimHoldEnabled, true)
	budget := time.Duration(envIntDefault(envModelClaimHoldBudgetSeconds, int(defaultModelClaimHoldBudget/time.Second))) * time.Second
	maxConc := envIntDefault(envModelClaimHoldMaxConcurrent, defaultModelClaimHoldMaxConcurrent)
	if budget <= 0 {
		budget = defaultModelClaimHoldBudget
	}
	if maxConc <= 0 {
		maxConc = defaultModelClaimHoldMaxConcurrent
	}
	klog.InfoS("modelclaim request hold configured", "enabled", enabled, "budgetSeconds", budget/time.Second, "maxConcurrent", maxConc)
	return &holdGate{
		enabled:      enabled,
		budget:       budget,
		pollInterval: modelClaimHoldPollInterval,
		sem:          make(chan struct{}, maxConc),
	}
}

func (h *holdGate) waitUntilRoutable(ctx context.Context, c modelClaimRoutability, model string) bool {
	if h == nil {
		return false
	}
	if !h.enabled {
		recordHoldResult(model, holdResultDisabled, false, 0)
		return false
	}
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		klog.V(4).InfoS("modelclaim hold concurrency cap reached", "model", model)
		recordHoldResult(model, holdResultCapacity, false, 0)
		return false
	}

	holdInFlight.Inc()
	defer holdInFlight.Dec()
	start := time.Now()
	if modelClaimRoutable(c, model) {
		recordHoldResult(model, holdResultForwarded, true, time.Since(start))
		return true
	}

	budget := time.After(h.budget)
	ticker := time.NewTicker(h.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			recordHoldResult(model, holdResultCancelled, true, time.Since(start))
			return false
		case <-budget:
			recordHoldResult(model, holdResultTimeout, true, time.Since(start))
			return false
		case <-ticker.C:
			if modelClaimRoutable(c, model) {
				recordHoldResult(model, holdResultForwarded, true, time.Since(start))
				return true
			}
		}
	}
}

func modelClaimRoutable(c modelClaimRoutability, model string) bool {
	return c.HasModel(model) && !c.IsModelClaimNotRoutable(model)
}

func ModelClaimNotReadyResponse(model string) *extProcPb.ProcessingResponse {
	return generateErrorResponse(envoyTypePb.StatusCode_ServiceUnavailable,
		[]*configPb.HeaderValueOption{
			{Header: &configPb.HeaderValue{
				Key: "Retry-After", RawValue: []byte(strconv.Itoa(modelClaimRetryAfterSeconds))}},
			{Header: &configPb.HeaderValue{
				Key: HeaderErrorNoModelBackends, RawValue: []byte(model)}},
		},
		fmt.Sprintf("model %s is not currently routable, retry shortly", model),
		ErrorCodeServiceUnavailable, "model")
}

func envBoolDefault(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envIntDefault(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
