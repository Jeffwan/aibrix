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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParseNodeStoreState(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        "n1",
		Annotations: map[string]string{NodeStoreStateAnnotation: `{"hot":["a"],"warm":["b"],"freeDramBytes":42}`},
	}}
	s := parseNodeStoreState(node)
	assert.Equal(t, []string{"a"}, s.Hot)
	assert.Equal(t, []string{"b"}, s.Warm)
	assert.Equal(t, int64(42), s.FreeDRAMBytes)
}

func TestParseNodeStoreState_MissingOrBad(t *testing.T) {
	assert.Empty(t, parseNodeStoreState(nil).Hot)
	noAnno := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
	assert.Empty(t, parseNodeStoreState(noAnno).Hot)
	bad := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "n", Annotations: map[string]string{NodeStoreStateAnnotation: "not-json"}}}
	assert.Empty(t, parseNodeStoreState(bad).Hot, "malformed annotation yields empty state")
}

func TestStoreLocality_Cost(t *testing.T) {
	hot := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "n-hot", Annotations: map[string]string{NodeStoreStateAnnotation: `{"hot":["m"]}`}}}
	warm := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "n-warm", Annotations: map[string]string{NodeStoreStateAnnotation: `{"warm":["m"]}`}}}
	bare := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n-bare"}}
	c := fake.NewClientBuilder().WithObjects(hot, warm, bare).Build()
	loc := storeLocality{reader: c}

	assert.Equal(t, localityHot, loc.Cost("m", "n-hot"))
	assert.Equal(t, localityWarm, loc.Cost("m", "n-warm"))
	assert.Equal(t, localityRemote, loc.Cost("m", "n-bare"), "no annotation = remote")
	assert.Equal(t, localityRemote, loc.Cost("m", "n-missing"), "node not found = remote")
	assert.Equal(t, localityRemote, loc.Cost("m", ""), "empty node = remote")
	assert.Equal(t, localityRemote, storeLocality{reader: nil}.Cost("m", "n-hot"), "nil reader = remote")
}
