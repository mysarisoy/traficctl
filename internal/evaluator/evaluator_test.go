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

package evaluator

import (
	"testing"

	trafficv1alpha1 "github.com/yusuf/trafficctl/api/v1alpha1"
)

func backend(name string, min, max int32) trafficv1alpha1.Backend {
	return trafficv1alpha1.Backend{Name: name, MinWeight: min, MaxWeight: max}
}

func weight(name string, w int32) trafficv1alpha1.BackendWeight {
	return trafficv1alpha1.BackendWeight{Name: name, Weight: w}
}

func weightsMap(ws []trafficv1alpha1.BackendWeight) map[string]int32 {
	m := map[string]int32{}
	for _, w := range ws {
		m[w.Name] = w.Weight
	}
	return m
}

func sumWeights(ws []trafficv1alpha1.BackendWeight) int32 {
	var s int32
	for _, w := range ws {
		s += w.Weight
	}
	return s
}

func latency(samples map[string]float64, threshold float64) Signal {
	return Signal{Name: "latency", Samples: samples, Threshold: threshold}
}

func errorRate(samples map[string]float64, threshold float64) Signal {
	return Signal{Name: "errorRate", Samples: samples, Threshold: threshold}
}

func TestThresholdEvaluator_ShiftsAwayFromUnhealthy(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends: []trafficv1alpha1.Backend{
			backend("v1", 20, 100),
			backend("v2", 10, 80),
		},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals:        []Signal{latency(map[string]float64{"v1": 200, "v2": 700}, 500)},
		MaxStepPercent: 20,
	})
	if !d.Changed {
		t.Fatalf("expected a change, got %+v", d)
	}
	got := weightsMap(d.Weights)
	if got["v1"] != 70 || got["v2"] != 30 {
		t.Errorf("expected v1=70 v2=30, got %v", got)
	}
	if sumWeights(d.Weights) != 100 {
		t.Errorf("weights do not sum to 100: %v", d.Weights)
	}
}

func TestThresholdEvaluator_HoldsWhenAllHealthy(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends:       []trafficv1alpha1.Backend{backend("v1", 20, 100), backend("v2", 10, 80)},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals:        []Signal{latency(map[string]float64{"v1": 100, "v2": 200}, 500)},
		MaxStepPercent: 20,
	})
	if d.Changed {
		t.Errorf("expected hold, got change: %+v", d)
	}
}

func TestThresholdEvaluator_HoldsWhenAllUnhealthy(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends:       []trafficv1alpha1.Backend{backend("v1", 20, 100), backend("v2", 10, 80)},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals:        []Signal{latency(map[string]float64{"v1": 800, "v2": 900}, 500)},
		MaxStepPercent: 20,
	})
	if d.Changed {
		t.Errorf("expected hold when nowhere safe to shift, got: %+v", d)
	}
}

func TestThresholdEvaluator_HoldsWhenNoSamples(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends:       []trafficv1alpha1.Backend{backend("v1", 20, 100), backend("v2", 10, 80)},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals:        []Signal{latency(map[string]float64{}, 500)},
		MaxStepPercent: 20,
	})
	if d.Changed {
		t.Errorf("expected hold on empty samples, got: %+v", d)
	}
}

func TestThresholdEvaluator_HoldsWithoutSignals(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends:       []trafficv1alpha1.Backend{backend("v1", 20, 100), backend("v2", 10, 80)},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		MaxStepPercent: 20,
	})
	if d.Changed {
		t.Errorf("expected hold when no signals configured, got: %+v", d)
	}
}

func TestThresholdEvaluator_RespectsMinBound(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends: []trafficv1alpha1.Backend{
			backend("v1", 20, 100),
			backend("v2", 40, 80),
		},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 40), weight("v2", 60)},
		Signals:        []Signal{latency(map[string]float64{"v1": 100, "v2": 900}, 500)},
		MaxStepPercent: 50,
	})
	if !d.Changed {
		t.Fatalf("expected change, got %+v", d)
	}
	got := weightsMap(d.Weights)
	if got["v2"] != 40 {
		t.Errorf("expected v2 driven exactly to minWeight=40, got %d", got["v2"])
	}
}

func TestThresholdEvaluator_RespectsMaxBound(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends: []trafficv1alpha1.Backend{
			backend("v1", 0, 55),
			backend("v2", 0, 100),
		},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals:        []Signal{latency(map[string]float64{"v1": 100, "v2": 900}, 500)},
		MaxStepPercent: 50,
	})
	if !d.Changed {
		t.Fatalf("expected change, got %+v", d)
	}
	got := weightsMap(d.Weights)
	if got["v1"] > 55 {
		t.Errorf("v1 violated maxWeight: got %d", got["v1"])
	}
}

func TestThresholdEvaluator_CappedByMaxStepPercent(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends:       []trafficv1alpha1.Backend{backend("v1", 0, 100), backend("v2", 0, 100)},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals:        []Signal{latency(map[string]float64{"v1": 100, "v2": 900}, 500)},
		MaxStepPercent: 5,
	})
	got := weightsMap(d.Weights)
	if got["v1"] != 55 || got["v2"] != 45 {
		t.Errorf("expected exactly 5pp shift per backend, got %v", got)
	}
}

func TestThresholdEvaluator_PreservesOrder(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends:       []trafficv1alpha1.Backend{backend("v1", 20, 100), backend("v2", 10, 80)},
		Current:        []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals:        []Signal{latency(map[string]float64{"v1": 100, "v2": 700}, 500)},
		MaxStepPercent: 10,
	})
	if d.Weights[0].Name != "v1" || d.Weights[1].Name != "v2" {
		t.Errorf("output order must match input backends order, got %v", d.Weights)
	}
}

// A latency-healthy backend should still lose weight if its error rate
// breaches the threshold: any signal breach marks the backend unhealthy.
func TestThresholdEvaluator_ShiftsOnErrorRateAlone(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends: []trafficv1alpha1.Backend{backend("v1", 0, 100), backend("v2", 0, 100)},
		Current:  []trafficv1alpha1.BackendWeight{weight("v1", 50), weight("v2", 50)},
		Signals: []Signal{
			latency(map[string]float64{"v1": 100, "v2": 150}, 500),
			errorRate(map[string]float64{"v1": 0.2, "v2": 6}, 1),
		},
		MaxStepPercent: 10,
	})
	if !d.Changed {
		t.Fatalf("expected shift driven by error-rate signal, got %+v", d)
	}
	got := weightsMap(d.Weights)
	if got["v1"] != 60 || got["v2"] != 40 {
		t.Errorf("expected 10pp shift v1<-v2, got %v", got)
	}
}

// When both signals breach, their excesses accumulate — the worst-scoring
// backend is the donor.
func TestThresholdEvaluator_CombinesSignalsIntoScore(t *testing.T) {
	ev := NewThresholdEvaluator()
	d := ev.Evaluate(Input{
		Backends: []trafficv1alpha1.Backend{
			backend("v1", 0, 100),
			backend("v2", 0, 100),
			backend("v3", 0, 100),
		},
		Current: []trafficv1alpha1.BackendWeight{
			weight("v1", 30), weight("v2", 30), weight("v3", 40),
		},
		Signals: []Signal{
			// v2 breaches latency a little; v3 breaches latency a lot AND
			// error rate. v3 should be the donor.
			latency(map[string]float64{"v1": 100, "v2": 600, "v3": 900}, 500),
			errorRate(map[string]float64{"v1": 0.1, "v2": 0.2, "v3": 5}, 1),
		},
		MaxStepPercent: 5,
	})
	if !d.Changed {
		t.Fatalf("expected change, got %+v", d)
	}
	got := weightsMap(d.Weights)
	// v3 is the worst and bleeds 5pp; recipient is v1 (only healthy).
	if got["v3"] != 35 {
		t.Errorf("expected v3 to give up 5pp, got v3=%d (all=%v)", got["v3"], got)
	}
	if got["v1"] != 35 {
		t.Errorf("expected v1 to receive 5pp, got v1=%d (all=%v)", got["v1"], got)
	}
	if got["v2"] != 30 {
		t.Errorf("expected v2 untouched (unhealthy but not worst), got v2=%d", got["v2"])
	}
}
