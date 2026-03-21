/*
Copyright 2024 The Aibrix Team.

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

package ratelimiter

import (
	"context"
)

// noopRateLimiter is a no-op rate limiter that always allows requests.
// Used in standalone mode when Redis is not available.
type noopRateLimiter struct{}

// NewNoopRateLimiter creates a rate limiter that never limits.
func NewNoopRateLimiter() RateLimiter {
	return &noopRateLimiter{}
}

func (n *noopRateLimiter) Get(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (n *noopRateLimiter) GetLimit(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (n *noopRateLimiter) Incr(_ context.Context, _ string, _ int64) (int64, error) {
	return 0, nil
}
