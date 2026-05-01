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

import "sync"

// DefaultMaxWindow caps the number of samples kept per (policy, backend).
// Anything beyond the cap is silently discarded; a caller asking for a
// larger window than the cap receives the mean over the cap.
const DefaultMaxWindow = 60

// Smoother maintains a per-policy/per-backend ring buffer of recent
// samples and returns the arithmetic mean over a caller-chosen window.
// Absorbing transient spikes keeps one bad sample from flipping a
// decision.
//
// State is in-memory; after a controller restart the window rebuilds
// from fresh observations. This is intentional — a stale buffer would
// be worse than an empty one.
type Smoother struct {
	mu        sync.Mutex
	maxWindow int
	data      map[string]map[string][]float64
}

// NewSmoother returns a Smoother that stores at most maxWindow samples
// per (policy, backend). A non-positive maxWindow falls back to
// DefaultMaxWindow.
func NewSmoother(maxWindow int) *Smoother {
	if maxWindow < 1 {
		maxWindow = DefaultMaxWindow
	}
	return &Smoother{
		maxWindow: maxWindow,
		data:      make(map[string]map[string][]float64),
	}
}

// Observe records a sample and returns the mean of the most recent
// window entries. window is clamped to [1, maxWindow]; fewer actual
// samples are averaged when the buffer is still warming up.
func (s *Smoother) Observe(policyKey, backend string, value float64, window int) float64 {
	if window < 1 {
		window = 1
	}
	if window > s.maxWindow {
		window = s.maxWindow
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[policyKey] == nil {
		s.data[policyKey] = make(map[string][]float64)
	}
	buf := append(s.data[policyKey][backend], value)
	if len(buf) > s.maxWindow {
		buf = buf[len(buf)-s.maxWindow:]
	}
	s.data[policyKey][backend] = buf

	start := len(buf) - window
	if start < 0 {
		start = 0
	}
	slice := buf[start:]
	var sum float64
	for _, v := range slice {
		sum += v
	}
	return sum / float64(len(slice))
}

// Forget drops all samples for the given policy. Call this when a
// TrafficPolicy is deleted so the map does not grow unbounded.
func (s *Smoother) Forget(policyKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, policyKey)
}
