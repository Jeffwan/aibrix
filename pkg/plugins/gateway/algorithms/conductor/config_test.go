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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, float64(30000), cfg.TTFTSLOMs)
	assert.Equal(t, float64(200), cfg.TBTSLOMs)
	assert.Equal(t, true, cfg.EnableEarlyRejection)
	assert.Equal(t, 1.5, cfg.PrefillTimeCoeffB)
	assert.Equal(t, 64, cfg.MaxPrefillQueueDepth)
}

func TestParseConfigFromRawJSON(t *testing.T) {
	raw := json.RawMessage(`{
		"ttftSloMs": 10000,
		"tbtSloMs": 100,
		"prefillTimeCoeffA": 0.002,
		"enableEarlyRejection": false
	}`)
	cfg, err := ParseConfig(raw)
	require.NoError(t, err)
	assert.Equal(t, float64(10000), cfg.TTFTSLOMs)
	assert.Equal(t, float64(100), cfg.TBTSLOMs)
	assert.Equal(t, 0.002, cfg.PrefillTimeCoeffA)
	assert.Equal(t, false, cfg.EnableEarlyRejection)
	// Fields not in JSON keep defaults
	assert.Equal(t, 1.5, cfg.PrefillTimeCoeffB)
	assert.Equal(t, 64, cfg.MaxPrefillQueueDepth)
}

func TestParseConfigNilRawJSON(t *testing.T) {
	cfg, err := ParseConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, float64(30000), cfg.TTFTSLOMs)
}

func TestParseConfigInvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid}`)
	_, err := ParseConfig(raw)
	assert.Error(t, err)
}
