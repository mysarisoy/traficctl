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
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	trafficv1alpha1 "github.com/mysarisoy/trafficctl/api/v1alpha1"
)

func TestRecordWeightShift_IncrementsCounterAndSetsGauges(t *testing.T) {
	const ns, name = "obsv-ns-a", "obsv-policy-a"
	weights := []trafficv1alpha1.BackendWeight{
		{Name: "v1", Weight: 70},
		{Name: "v2", Weight: 30},
	}

	before := testutil.ToFloat64(weightShiftsTotal.WithLabelValues(ns, name))
	recordWeightShift(ns, name, weights)
	after := testutil.ToFloat64(weightShiftsTotal.WithLabelValues(ns, name))
	if after-before != 1 {
		t.Errorf("expected shift counter to advance by 1, got %v → %v", before, after)
	}

	if got := testutil.ToFloat64(backendWeight.WithLabelValues(ns, name, "v1")); got != 70 {
		t.Errorf("v1 gauge = %v, want 70", got)
	}
	if got := testutil.ToFloat64(backendWeight.WithLabelValues(ns, name, "v2")); got != 30 {
		t.Errorf("v2 gauge = %v, want 30", got)
	}
}

func TestRecordPhase_IsOneHot(t *testing.T) {
	const ns, name = "obsv-ns-b", "obsv-policy-b"
	recordPhase(ns, name, trafficv1alpha1.PhaseProgressing)

	if got := testutil.ToFloat64(policyPhase.WithLabelValues(ns, name, string(trafficv1alpha1.PhaseProgressing))); got != 1 {
		t.Errorf("Progressing gauge = %v, want 1", got)
	}
	for _, p := range []trafficv1alpha1.TrafficPolicyPhase{
		trafficv1alpha1.PhaseStable,
		trafficv1alpha1.PhaseFrozen,
		trafficv1alpha1.PhaseDegraded,
		trafficv1alpha1.PhasePending,
	} {
		if got := testutil.ToFloat64(policyPhase.WithLabelValues(ns, name, string(p))); got != 0 {
			t.Errorf("%s gauge = %v, want 0", p, got)
		}
	}

	// Flipping to a different phase zeroes the old one.
	recordPhase(ns, name, trafficv1alpha1.PhaseStable)
	if got := testutil.ToFloat64(policyPhase.WithLabelValues(ns, name, string(trafficv1alpha1.PhaseProgressing))); got != 0 {
		t.Errorf("Progressing gauge after flip = %v, want 0", got)
	}
	if got := testutil.ToFloat64(policyPhase.WithLabelValues(ns, name, string(trafficv1alpha1.PhaseStable))); got != 1 {
		t.Errorf("Stable gauge after flip = %v, want 1", got)
	}
}

func TestForgetPolicyMetrics_RemovesGaugeSeries(t *testing.T) {
	const ns, name = "obsv-ns-c", "obsv-policy-c"
	recordBackendWeights(ns, name, []trafficv1alpha1.BackendWeight{{Name: "v1", Weight: 55}})
	recordPhase(ns, name, trafficv1alpha1.PhaseStable)

	// Sanity: gauge is populated before forgetting.
	if got := testutil.ToFloat64(backendWeight.WithLabelValues(ns, name, "v1")); got != 55 {
		t.Fatalf("pre-forget gauge = %v, want 55", got)
	}

	forgetPolicyMetrics(ns, name)

	// After delete, the previous label combination is gone; a fresh WithLabelValues
	// instantiates a zero-valued gauge — that's the expected Prometheus behavior.
	if got := testutil.ToFloat64(backendWeight.WithLabelValues(ns, name, "v1")); got != 0 {
		t.Errorf("post-forget gauge = %v, want 0 (fresh series)", got)
	}
}

func TestRecordMetricSourceError_CountsPerSignal(t *testing.T) {
	const ns, name = "obsv-ns-d", "obsv-policy-d"
	before := testutil.ToFloat64(metricSourceErrorsTotal.WithLabelValues(ns, name, "latency"))
	recordMetricSourceError(ns, name, "latency")
	recordMetricSourceError(ns, name, "latency")
	recordMetricSourceError(ns, name, "errorRate")

	if got := testutil.ToFloat64(metricSourceErrorsTotal.WithLabelValues(ns, name, "latency")); got-before != 2 {
		t.Errorf("latency counter delta = %v, want 2", got-before)
	}
	if got := testutil.ToFloat64(metricSourceErrorsTotal.WithLabelValues(ns, name, "errorRate")); got < 1 {
		t.Errorf("errorRate counter = %v, want ≥ 1", got)
	}
}
