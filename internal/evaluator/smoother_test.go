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

import "testing"

func TestSmoother_MeansOverWindow(t *testing.T) {
	s := NewSmoother(10)
	if got := s.Observe("p", "v1", 100, 3); got != 100 {
		t.Errorf("first sample: got %v want 100", got)
	}
	if got := s.Observe("p", "v1", 200, 3); got != 150 {
		t.Errorf("two samples: got %v want 150", got)
	}
	if got := s.Observe("p", "v1", 300, 3); got != 200 {
		t.Errorf("three samples: got %v want 200", got)
	}
	if got := s.Observe("p", "v1", 400, 3); got != 300 {
		t.Errorf("rolled window: got %v want 300", got)
	}
}

func TestSmoother_SeparatePolicyKeys(t *testing.T) {
	s := NewSmoother(10)
	s.Observe("a", "v1", 100, 3)
	s.Observe("a", "v1", 200, 3)
	if got := s.Observe("b", "v1", 1000, 3); got != 1000 {
		t.Errorf("policy keys must not cross-contaminate: got %v", got)
	}
}

func TestSmoother_ForgetDropsPolicy(t *testing.T) {
	s := NewSmoother(10)
	s.Observe("a", "v1", 100, 3)
	s.Observe("a", "v1", 200, 3)
	s.Forget("a")
	if got := s.Observe("a", "v1", 500, 3); got != 500 {
		t.Errorf("expected fresh window after Forget, got %v", got)
	}
}

func TestSmoother_WindowClampedToOne(t *testing.T) {
	s := NewSmoother(10)
	s.Observe("a", "v1", 100, 0)
	if got := s.Observe("a", "v1", 500, 0); got != 500 {
		t.Errorf("window<1 should track latest only, got %v", got)
	}
}

func TestSmoother_WindowClampedToMax(t *testing.T) {
	s := NewSmoother(2)
	s.Observe("a", "v1", 100, 5) // stored
	s.Observe("a", "v1", 200, 5) // stored
	// third sample pushes 100 out of the buffer (max=2)
	if got := s.Observe("a", "v1", 300, 5); got != 250 {
		t.Errorf("expected mean of last two (200,300)=250, got %v", got)
	}
}

func TestSmoother_PartialWindow(t *testing.T) {
	s := NewSmoother(10)
	// Only two samples exist; asking for window=5 should mean over the
	// two stored samples, not wait for a full buffer.
	s.Observe("a", "v1", 100, 5)
	if got := s.Observe("a", "v1", 200, 5); got != 150 {
		t.Errorf("partial window: got %v want 150", got)
	}
}
