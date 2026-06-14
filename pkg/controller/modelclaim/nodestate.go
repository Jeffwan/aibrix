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
	"context"
	"encoding/json"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NodeStoreStateAnnotation is the Node annotation the node weight-cache DaemonSet
// (Layer-2 block B2) stamps to advertise which models it has staged, by tier.
// Read by storeLocality to score placement. It is the contract between the
// store (writer) and the controller's placement scorer (reader).
const NodeStoreStateAnnotation = "nodestore.modelclaim.aibrix.ai/state"

// Locality cost tiers (lower = cheaper to bring the model up on this node).
const (
	localityHot    = 0.0   // weights resident in the node store's pinned DRAM
	localityWarm   = 1.0   // weights on the node store's local NVMe
	localityRemote = 100.0 // not on the node; must fetch from a remote/HF source
)

// NodeStoreState is the parsed weight-cache state of one node's store.
type NodeStoreState struct {
	Hot           []string `json:"hot,omitempty"`  // served model names resident in pinned DRAM
	Warm          []string `json:"warm,omitempty"` // served model names staged on local NVMe
	FreeDRAMBytes int64    `json:"freeDramBytes,omitempty"`
}

// parseNodeStoreState reads the store-state annotation off a Node. A missing or
// malformed annotation yields an empty state (everything looks remote), which is
// safe: storeLocality then treats the node as a cold-load candidate.
func parseNodeStoreState(node *corev1.Node) NodeStoreState {
	var s NodeStoreState
	if node == nil {
		return s
	}
	if raw, ok := node.Annotations[NodeStoreStateAnnotation]; ok {
		_ = json.Unmarshal([]byte(raw), &s) // best-effort; empty on error
	}
	return s
}

// storeLocality scores placement by where a model's weights already are, read
// from each node's store-state annotation (populated by the block B2 DaemonSet).
// When no node advertises state (DaemonSet absent), every node is "remote" →
// equal cost → placement falls back to load, identical to uniformLocality. It
// self-activates once the DaemonSet starts reporting.
type storeLocality struct {
	reader client.Reader
}

func (s storeLocality) Cost(model, nodeName string) float64 {
	if nodeName == "" || s.reader == nil {
		return localityRemote
	}
	node := &corev1.Node{}
	if err := s.reader.Get(context.Background(), types.NamespacedName{Name: nodeName}, node); err != nil {
		return localityRemote
	}
	state := parseNodeStoreState(node)
	switch {
	case slices.Contains(state.Hot, model):
		return localityHot
	case slices.Contains(state.Warm, model):
		return localityWarm
	default:
		return localityRemote
	}
}
