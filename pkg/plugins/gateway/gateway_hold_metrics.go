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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/vllm-project/aibrix/pkg/constants"
)

const (
	holdResultForwarded = "forwarded"
	holdResultTimeout   = "timeout"
	holdResultCapacity  = "capacity"
	holdResultDisabled  = "disabled"
	holdResultCancelled = "cancelled"
)

var (
	holdSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: constants.AibrixSubsystemName,
		Name:      "modelclaim_hold_seconds",
		Help:      "Time a request was held waiting for a ModelClaim to become routable.",
		Buckets:   []float64{0.5, 1, 2, 5, 10, 20, 40, 60, 90, 120},
	}, []string{"model"})

	holdResultTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Subsystem: constants.AibrixSubsystemName,
		Name:      "modelclaim_hold_result_total",
		Help:      "Hold outcomes by result.",
	}, []string{"model", "result"})

	holdInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Subsystem: constants.AibrixSubsystemName,
		Name:      "modelclaim_hold_in_flight",
		Help:      "Requests currently held waiting for a ModelClaim to become routable.",
	})
)

func recordHoldResult(model, result string, held bool, dur time.Duration) {
	holdResultTotal.WithLabelValues(model, result).Inc()
	if held {
		holdSeconds.WithLabelValues(model).Observe(dur.Seconds())
	}
}
