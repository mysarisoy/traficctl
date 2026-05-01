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

// Package evaluator decides weight adjustments from observed metric values.
// Implementations are pure: the reconciler owns state (cooldown, smoother).
package evaluator

import (
	"fmt"
	"sort"
	"strings"

	trafficv1alpha1 "github.com/yusuf/trafficctl/api/v1alpha1"
)

// Signal is a single health dimension (e.g. latency, error-rate). A backend
// is considered unhealthy on this signal when its sample exceeds Threshold.
// Samples and Threshold are in the same unit — the evaluator only compares
// them — so callers may reuse the type for any per-backend numeric signal.
type Signal struct {
	// Name identifies the signal in decision reasons (e.g. "latency",
	// "errorRate"). It is informational only.
	Name string
	// Samples is the observed value per backend. A backend missing from
	// this map contributes no information from this signal.
	Samples map[string]float64
	// Threshold is the ceiling. Samples strictly greater than Threshold
	// are considered breaches.
	Threshold float64
}

// Input bundles everything an Evaluator needs to produce a decision.
type Input struct {
	Backends       []trafficv1alpha1.Backend
	Current        []trafficv1alpha1.BackendWeight
	Signals        []Signal
	MaxStepPercent int32
}

// Decision is the proposed weight allocation and a short human reason.
// Changed reports whether Weights differs from the input's Current so the
// reconciler can decide whether to patch the route.
type Decision struct {
	Weights []trafficv1alpha1.BackendWeight
	Reason  string
	Changed bool
}

// Evaluator proposes weights given an Input. Implementations must be pure
// functions: the same input always yields the same decision.
type Evaluator interface {
	Evaluate(in Input) Decision
}

// ThresholdEvaluator shifts traffic away from backends whose observed
// values breach any configured signal's threshold. Each decision is bounded
// by MaxStepPercent per backend and respects each backend's declared
// min/max. When multiple signals are configured, a backend is unhealthy if
// any signal breaches; donor ordering reflects the summed excess.
type ThresholdEvaluator struct{}

// NewThresholdEvaluator constructs a ThresholdEvaluator.
func NewThresholdEvaluator() *ThresholdEvaluator { return &ThresholdEvaluator{} }

type backendState struct {
	name       string
	current    int32
	min, max   int32
	score      float64 // Σ max(0, sample/threshold - 1) across signals
	haveSample bool    // at least one signal reported a sample
	breaches   []string
	stepBudget int32
}

// Evaluate implements Evaluator.
func (ThresholdEvaluator) Evaluate(in Input) Decision {
	if in.MaxStepPercent <= 0 {
		return holdDecision(in.Current, "maxStepPercent is zero; holding")
	}
	if len(in.Signals) == 0 {
		return holdDecision(in.Current, "no signals configured; holding")
	}

	states := buildStates(in)
	var unhealthy, healthy []*backendState
	missingSamples := 0
	for _, s := range states {
		if !s.haveSample {
			missingSamples++
			continue
		}
		if len(s.breaches) > 0 {
			unhealthy = append(unhealthy, s)
		} else {
			healthy = append(healthy, s)
		}
	}

	switch {
	case missingSamples == len(states):
		return holdDecision(in.Current, "no samples received; holding")
	case len(unhealthy) == 0:
		return holdDecision(in.Current, "all backends within thresholds")
	case len(healthy) == 0:
		return holdDecision(in.Current, "all backends breach thresholds; nowhere safe to shift")
	}

	// Worst-first for donors (highest score), best-first for recipients
	// (lowest score). Ties broken by name for determinism.
	sort.SliceStable(unhealthy, func(i, j int) bool {
		if unhealthy[i].score != unhealthy[j].score {
			return unhealthy[i].score > unhealthy[j].score
		}
		return unhealthy[i].name < unhealthy[j].name
	})
	sort.SliceStable(healthy, func(i, j int) bool {
		if healthy[i].score != healthy[j].score {
			return healthy[i].score < healthy[j].score
		}
		return healthy[i].name < healthy[j].name
	})

	shifted := int32(0)
	for {
		donor := pickDonor(unhealthy)
		recipient := pickRecipient(healthy)
		if donor == nil || recipient == nil {
			break
		}
		donor.current--
		donor.stepBudget--
		recipient.current++
		recipient.stepBudget--
		shifted++
	}

	if shifted == 0 {
		return holdDecision(in.Current, "no headroom to shift within declared bounds")
	}

	out := make([]trafficv1alpha1.BackendWeight, 0, len(states))
	for _, s := range states {
		out = append(out, trafficv1alpha1.BackendWeight{Name: s.name, Weight: s.current})
	}

	return Decision{
		Weights: out,
		Reason:  buildReason(shifted, unhealthy, healthy),
		Changed: true,
	}
}

func buildStates(in Input) []*backendState {
	current := map[string]int32{}
	for _, w := range in.Current {
		current[w.Name] = w.Weight
	}
	states := make([]*backendState, 0, len(in.Backends))
	for _, b := range in.Backends {
		st := &backendState{
			name:       b.Name,
			current:    current[b.Name],
			min:        b.MinWeight,
			max:        b.MaxWeight,
			stepBudget: in.MaxStepPercent,
		}
		for _, sig := range in.Signals {
			if sig.Threshold <= 0 {
				continue
			}
			v, ok := sig.Samples[b.Name]
			if !ok {
				continue
			}
			st.haveSample = true
			if ratio := v / sig.Threshold; ratio > 1 {
				st.score += ratio - 1
				st.breaches = append(st.breaches, sig.Name)
			}
		}
		states = append(states, st)
	}
	return states
}

func pickDonor(unhealthy []*backendState) *backendState {
	for _, s := range unhealthy {
		if s.stepBudget > 0 && s.current > s.min {
			return s
		}
	}
	return nil
}

func pickRecipient(healthy []*backendState) *backendState {
	for _, s := range healthy {
		if s.stepBudget > 0 && s.current < s.max {
			return s
		}
	}
	return nil
}

func holdDecision(current []trafficv1alpha1.BackendWeight, reason string) Decision {
	clone := make([]trafficv1alpha1.BackendWeight, len(current))
	copy(clone, current)
	return Decision{Weights: clone, Reason: reason, Changed: false}
}

func buildReason(shifted int32, unhealthy, healthy []*backendState) string {
	donors := make([]string, 0, len(unhealthy))
	for _, s := range unhealthy {
		sigs := strings.Join(dedupe(s.breaches), "+")
		donors = append(donors, fmt.Sprintf("%s(%s)", s.name, sigs))
	}
	recipients := namesOf(healthy)
	return fmt.Sprintf("Shifted %d pp: %s -> %s",
		shifted, strings.Join(donors, ","), strings.Join(recipients, ","))
}

func namesOf(states []*backendState) []string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, s.name)
	}
	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
