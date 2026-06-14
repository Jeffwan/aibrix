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
	"sync"
	"testing"
	"time"

	configPb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyTypePb "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeModelClaimRoutability struct {
	mu          sync.Mutex
	has         bool
	notRoutable bool
}

func (f *fakeModelClaimRoutability) set(has, notRoutable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.has, f.notRoutable = has, notRoutable
}

func (f *fakeModelClaimRoutability) HasModel(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.has
}

func (f *fakeModelClaimRoutability) IsModelClaimNotRoutable(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notRoutable
}

func testHoldGate(budget time.Duration, maxConc int) *holdGate {
	return &holdGate{
		enabled:      true,
		budget:       budget,
		pollInterval: 5 * time.Millisecond,
		sem:          make(chan struct{}, maxConc),
	}
}

func TestHoldGateWaitsUntilModelClaimRoutable(t *testing.T) {
	g := testHoldGate(2*time.Second, 4)
	c := &fakeModelClaimRoutability{has: false, notRoutable: true}
	go func() {
		time.Sleep(30 * time.Millisecond)
		c.set(true, false)
	}()

	start := time.Now()
	ok := g.waitUntilRoutable(context.Background(), c, "m")
	assert.True(t, ok)
	assert.Less(t, time.Since(start), time.Second)
}

func TestHoldGateAlreadyRoutable(t *testing.T) {
	g := testHoldGate(time.Second, 4)
	c := &fakeModelClaimRoutability{has: true, notRoutable: false}
	start := time.Now()
	assert.True(t, g.waitUntilRoutable(context.Background(), c, "m"))
	assert.Less(t, time.Since(start), 20*time.Millisecond)
}

func TestHoldGateBudgetExpiry(t *testing.T) {
	g := testHoldGate(40*time.Millisecond, 4)
	c := &fakeModelClaimRoutability{has: false, notRoutable: true}
	start := time.Now()
	ok := g.waitUntilRoutable(context.Background(), c, "m")
	assert.False(t, ok)
	assert.GreaterOrEqual(t, time.Since(start), 35*time.Millisecond)
}

func TestHoldGateConcurrencyCap(t *testing.T) {
	g := testHoldGate(2*time.Second, 1)
	g.sem <- struct{}{}
	c := &fakeModelClaimRoutability{has: false, notRoutable: true}

	start := time.Now()
	ok := g.waitUntilRoutable(context.Background(), c, "m")
	assert.False(t, ok)
	assert.Less(t, time.Since(start), 50*time.Millisecond)
}

func TestHoldGateContextCancel(t *testing.T) {
	g := testHoldGate(2*time.Second, 4)
	c := &fakeModelClaimRoutability{has: false, notRoutable: true}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	ok := g.waitUntilRoutable(ctx, c, "m")
	assert.False(t, ok)
	assert.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestHoldGateDisabledAndNil(t *testing.T) {
	g := testHoldGate(time.Second, 4)
	g.enabled = false
	c := &fakeModelClaimRoutability{has: false, notRoutable: true}
	assert.False(t, g.waitUntilRoutable(context.Background(), c, "m"))

	var nilGate *holdGate
	assert.False(t, nilGate.waitUntilRoutable(context.Background(), c, "m"))
}

func TestNewHoldGateEnvOverrides(t *testing.T) {
	t.Setenv(envModelClaimHoldEnabled, "false")
	t.Setenv(envModelClaimHoldBudgetSeconds, "5")
	t.Setenv(envModelClaimHoldMaxConcurrent, "7")
	g := newHoldGate()
	assert.False(t, g.enabled)
	assert.Equal(t, 5*time.Second, g.budget)
	assert.Equal(t, 7, cap(g.sem))
}

func TestValidateModelAvailabilityReturnsRetryableForNotRoutableModelClaim(t *testing.T) {
	mockCache := &MockCache{}
	mockCache.On("HasModel", "sleeping-model").Return(false).Once()
	mockCache.On("IsModelClaimNotRoutable", "sleeping-model").Return(true).Once()
	s := &Server{
		cache: mockCache,
		hold:  &holdGate{enabled: false},
	}

	pods, resp := s.validateModelAvailability(context.Background(), "r1", "sleeping-model")
	require.Nil(t, pods)
	require.NotNil(t, resp)
	assert.Equal(t, envoyTypePb.StatusCode_ServiceUnavailable, resp.GetImmediateResponse().GetStatus().GetCode())
	headers := resp.GetImmediateResponse().GetHeaders().GetSetHeaders()
	assert.Contains(t, headers, modelClaimHeader("Retry-After", "30"))
	assert.Contains(t, headers, modelClaimHeader(HeaderErrorNoModelBackends, "sleeping-model"))
	mockCache.AssertExpectations(t)
}

func modelClaimHeader(key, value string) *configPb.HeaderValueOption {
	return &configPb.HeaderValueOption{Header: &configPb.HeaderValue{Key: key, RawValue: []byte(value)}}
}
