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
	"encoding/json"
	"fmt"

	"github.com/vllm-project/aibrix/pkg/types"
)

// RouterConductor is the routing algorithm name for conductor-based PD disaggregation.
const RouterConductor types.RoutingAlgorithm = "conductor"

// ConductorConfig holds tunable parameters for the conductor algorithm.
// Configured per-model via model.aibrix.ai/config annotation's algorithmConfig field.
//
// The SLO thresholds and prefill time coefficients correspond to the scheduling
// parameters described in Mooncake §4.1 (Algorithm 1, lines 13-18) and §5.1.
type ConductorConfig struct {
	// SLO thresholds
	TTFTSLOMs float64 `json:"ttftSloMs"` // TTFT SLO in milliseconds
	TBTSLOMs  float64 `json:"tbtSloMs"`  // TBT SLO in milliseconds

	// Prefill time prediction model: T = A * tokens^B + C (ms)
	PrefillTimeCoeffA float64 `json:"prefillTimeCoeffA"`
	PrefillTimeCoeffB float64 `json:"prefillTimeCoeffB"` // superlinear attention
	PrefillTimeCoeffC float64 `json:"prefillTimeCoeffC"` // base overhead ms

	// Early rejection
	EnableEarlyRejection      bool    `json:"enableEarlyRejection"`
	DecodeLoadRejectThreshold float64 `json:"decodeLoadRejectThreshold"`
	MaxPrefillQueueDepth      int     `json:"maxPrefillQueueDepth"`
}

// DefaultConfig returns ConductorConfig with sensible defaults.
func DefaultConfig() *ConductorConfig {
	return &ConductorConfig{
		TTFTSLOMs:                 30000,
		TBTSLOMs:                  200,
		PrefillTimeCoeffA:         0.001,
		PrefillTimeCoeffB:         1.5,
		PrefillTimeCoeffC:         5.0,
		EnableEarlyRejection:      true,
		DecodeLoadRejectThreshold: 0.9,
		MaxPrefillQueueDepth:      64,
	}
}

// ParseConfig extracts ConductorConfig from the generic RoutingConfig field.
// Returns defaults if raw is nil or empty. Unset fields retain default values.
func ParseConfig(raw json.RawMessage) (*ConductorConfig, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse conductor config: %w", err)
	}
	return cfg, nil
}
