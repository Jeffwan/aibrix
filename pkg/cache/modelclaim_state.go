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

package cache

import "sync"

// modelClaimState tracks which warm runtime pods advertise each served model
// and at what port. Port 0 is the non-routable marker written while a model is
// activating or sleeping; it keeps the model known to the gateway without
// adding it to the normal routing structures.
type modelClaimState struct {
	mu sync.RWMutex
	// model -> podKey -> advertised port (0 = known but not routable).
	ports map[string]map[string]int
}

func (s *modelClaimState) set(podKey, model string, port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ports == nil {
		s.ports = make(map[string]map[string]int)
	}
	byPod := s.ports[model]
	if byPod == nil {
		byPod = make(map[string]int)
		s.ports[model] = byPod
	}
	byPod[podKey] = port
}

func (s *modelClaimState) clearPod(podKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for model, byPod := range s.ports {
		delete(byPod, podKey)
		if len(byPod) == 0 {
			delete(s.ports, model)
		}
	}
}

func (s *modelClaimState) isModelClaim(model string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.ports[model]) > 0
}

func (s *modelClaimState) isNotRoutable(model string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byPod, ok := s.ports[model]
	if !ok || len(byPod) == 0 {
		return false
	}
	for _, port := range byPod {
		if port > 0 {
			return false
		}
	}
	return true
}

func (c *Store) IsModelClaim(model string) bool {
	return c.modelClaimState.isModelClaim(model)
}

func (c *Store) IsModelClaimNotRoutable(model string) bool {
	return c.modelClaimState.isNotRoutable(model)
}
