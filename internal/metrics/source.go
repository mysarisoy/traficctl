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

// Package metrics abstracts the signal sources (Prometheus, etc.) the
// traffic policy controller consults when deciding on weight changes.
package metrics

import "context"

// Source returns a scalar observation per backend. The controller is
// responsible for translating query results into per-backend values; a
// Source implementation owns that mapping.
type Source interface {
	// Sample executes the query and returns one value per backend name.
	// Implementations must omit backends the query did not produce a
	// value for (rather than inventing zeros). Returning a non-nil error
	// is reserved for upstream failures (connection, auth, 5xx); a query
	// that simply returns an empty vector must yield an empty map and
	// nil error.
	Sample(ctx context.Context, query string, backends []string) (map[string]float64, error)
}

// StaticSource is an in-memory Source used by tests and for wiring the
// controller when no real metrics backend is configured.
type StaticSource struct {
	Values map[string]float64
	Err    error
}

// Sample returns the configured values, filtered to the requested backends.
func (s *StaticSource) Sample(_ context.Context, _ string, backends []string) (map[string]float64, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	out := make(map[string]float64, len(backends))
	for _, b := range backends {
		if v, ok := s.Values[b]; ok {
			out[b] = v
		}
	}
	return out, nil
}
