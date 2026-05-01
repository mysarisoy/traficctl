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

package metrics

import (
	"context"
	"errors"
	"testing"
)

func TestStaticSource_ReturnsRequestedBackendsOnly(t *testing.T) {
	s := &StaticSource{Values: map[string]float64{
		"v1": 100, "v2": 200, "v3": 300,
	}}
	got, err := s.Sample(context.Background(), "q", []string{"v1", "v2"})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if len(got) != 2 || got["v1"] != 100 || got["v2"] != 200 {
		t.Errorf("unexpected samples: %v", got)
	}
}

func TestStaticSource_OmitsMissingBackends(t *testing.T) {
	s := &StaticSource{Values: map[string]float64{"v1": 100}}
	got, err := s.Sample(context.Background(), "q", []string{"v1", "absent"})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if _, ok := got["absent"]; ok {
		t.Errorf("absent backend should not appear: %v", got)
	}
}

func TestStaticSource_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	s := &StaticSource{Err: want}
	_, err := s.Sample(context.Background(), "q", []string{"v1"})
	if !errors.Is(err, want) {
		t.Errorf("expected %v, got %v", want, err)
	}
}
