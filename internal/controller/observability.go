/*
Copyright 2026.

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

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	trafficv1alpha1 "github.com/yusuf/trafficctl/api/v1alpha1"
)

// Controller-owned observability metrics. Registered with the
// controller-runtime metrics registry so they're served alongside the
// framework's built-in reconcile counters on --metrics-bind-address.
//
// Controller-runtime already exposes reconcile_total, reconcile_errors,
// and reconcile_time_seconds — don't duplicate those. These cover the
// domain signals: shifts, freezes, metric errors, per-backend weight.
var (
	weightShiftsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trafficctl_weight_shifts_total",
			Help: "Total number of weight changes applied per TrafficPolicy.",
		},
		[]string{"namespace", "name"},
	)

	freezesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trafficctl_freezes_total",
			Help: "Total number of reconciles that held weights, by cause.",
		},
		[]string{"namespace", "name", "cause"},
	)

	metricSourceErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trafficctl_metric_source_errors_total",
			Help: "Total per-signal failures from the configured metrics source.",
		},
		[]string{"namespace", "name", "signal"},
	)

	backendWeight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "trafficctl_backend_weight",
			Help: "Current applied weight (0-100) per backend.",
		},
		[]string{"namespace", "name", "backend"},
	)

	policyPhase = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "trafficctl_policy_phase",
			Help: "Policy phase as a one-hot gauge: 1 for the current phase, 0 otherwise.",
		},
		[]string{"namespace", "name", "phase"},
	)
)

const (
	freezeCausePaused  = "paused"
	freezeCauseMetrics = "metrics_unavailable"
	freezeCauseRoute   = "route_missing"
	freezeCauseInvalid = "invalid_spec"
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		weightShiftsTotal,
		freezesTotal,
		metricSourceErrorsTotal,
		backendWeight,
		policyPhase,
	)
}

// recordWeightShift increments the per-policy shift counter and updates
// per-backend weight gauges to the newly applied allocation.
func recordWeightShift(namespace, name string, weights []trafficv1alpha1.BackendWeight) {
	weightShiftsTotal.WithLabelValues(namespace, name).Inc()
	for _, w := range weights {
		backendWeight.WithLabelValues(namespace, name, w.Name).Set(float64(w.Weight))
	}
}

// recordBackendWeights syncs the backend-weight gauges without bumping
// the shift counter. Used every reconcile so the gauges reflect the
// current state even when nothing changed.
func recordBackendWeights(namespace, name string, weights []trafficv1alpha1.BackendWeight) {
	for _, w := range weights {
		backendWeight.WithLabelValues(namespace, name, w.Name).Set(float64(w.Weight))
	}
}

// recordPhase sets the one-hot phase gauge for the given policy,
// zeroing the other phases so dashboards don't have to de-conflict.
func recordPhase(namespace, name string, phase trafficv1alpha1.TrafficPolicyPhase) {
	all := []trafficv1alpha1.TrafficPolicyPhase{
		trafficv1alpha1.PhasePending,
		trafficv1alpha1.PhaseStable,
		trafficv1alpha1.PhaseProgressing,
		trafficv1alpha1.PhaseFrozen,
		trafficv1alpha1.PhaseDegraded,
	}
	for _, p := range all {
		v := 0.0
		if p == phase {
			v = 1.0
		}
		policyPhase.WithLabelValues(namespace, name, string(p)).Set(v)
	}
}

func recordFreeze(namespace, name, cause string) {
	freezesTotal.WithLabelValues(namespace, name, cause).Inc()
}

func recordMetricSourceError(namespace, name, signal string) {
	metricSourceErrorsTotal.WithLabelValues(namespace, name, signal).Inc()
}

// forgetPolicyMetrics removes all label series for a deleted policy so
// the gauges don't leak cardinality. Counters are intentionally left in
// place — their rate() over the deletion boundary is still meaningful.
func forgetPolicyMetrics(namespace, name string) {
	backendWeight.DeletePartialMatch(prometheus.Labels{"namespace": namespace, "name": name})
	policyPhase.DeletePartialMatch(prometheus.Labels{"namespace": namespace, "name": name})
}
