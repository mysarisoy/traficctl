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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newPromServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func TestPromSource_ParsesVectorResult(t *testing.T) {
	var gotQuery string
	srv := newPromServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotQuery = r.Form.Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{"resultType":"vector","result":[
				{"metric":{"backend":"v1"},"value":[1713999999,"120.5"]},
				{"metric":{"backend":"v2"},"value":[1713999999,"987.25"]},
				{"metric":{"backend":"legacy"},"value":[1713999999,"42"]}
			]}
		}`))
	})
	defer srv.Close()

	src := NewPromSource(srv.URL, srv.Client())
	got, err := src.Sample(context.Background(), "some_query", []string{"v1", "v2"})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if gotQuery != "some_query" {
		t.Errorf("query not forwarded: got %q", gotQuery)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (filtered), got %v", got)
	}
	if got["v1"] != 120.5 || got["v2"] != 987.25 {
		t.Errorf("unexpected values: %v", got)
	}
	if _, leaked := got["legacy"]; leaked {
		t.Errorf("non-requested backend leaked into result: %v", got)
	}
}

func TestPromSource_HonorsCustomBackendLabel(t *testing.T) {
	srv := newPromServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{"resultType":"vector","result":[
				{"metric":{"svc":"v1"},"value":[1,"1"]},
				{"metric":{"backend":"v1"},"value":[1,"999"]}
			]}
		}`))
	})
	defer srv.Close()

	src := NewPromSource(srv.URL, srv.Client())
	src.BackendLabel = "svc"
	got, err := src.Sample(context.Background(), "q", []string{"v1"})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if got["v1"] != 1 {
		t.Errorf("expected value from svc-labeled sample, got %v", got)
	}
}

func TestPromSource_ReturnsErrorOn5xx(t *testing.T) {
	srv := newPromServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream gone`))
	})
	defer srv.Close()

	src := NewPromSource(srv.URL, srv.Client())
	_, err := src.Sample(context.Background(), "q", []string{"v1"})
	if err == nil {
		t.Fatal("expected error on 5xx")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should include status code, got %v", err)
	}
}

func TestPromSource_ReturnsErrorOnPromStatusError(t *testing.T) {
	srv := newPromServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid query"}`))
	})
	defer srv.Close()

	src := NewPromSource(srv.URL, srv.Client())
	_, err := src.Sample(context.Background(), "q", []string{"v1"})
	if err == nil {
		t.Fatal("expected error when Prometheus returns error status")
	}
	if !strings.Contains(err.Error(), "invalid query") {
		t.Errorf("error should surface upstream message, got %v", err)
	}
}

func TestPromSource_RejectsNonVectorResult(t *testing.T) {
	srv := newPromServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	})
	defer srv.Close()

	src := NewPromSource(srv.URL, srv.Client())
	_, err := src.Sample(context.Background(), "q", []string{"v1"})
	if err == nil || !strings.Contains(err.Error(), "vector") {
		t.Errorf("expected vector-type error, got %v", err)
	}
}

func TestPromSource_RejectsEmptyAddress(t *testing.T) {
	src := NewPromSource("", nil)
	_, err := src.Sample(context.Background(), "q", []string{"v1"})
	if err == nil {
		t.Fatal("expected error with empty address")
	}
}

func TestPromSource_OmitsBackendsWithoutLabel(t *testing.T) {
	srv := newPromServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status":"success",
			"data":{"resultType":"vector","result":[
				{"metric":{"other":"thing"},"value":[1,"5"]}
			]}
		}`))
	})
	defer srv.Close()

	src := NewPromSource(srv.URL, srv.Client())
	got, err := src.Sample(context.Background(), "q", []string{"v1"})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map when no labeled results, got %v", got)
	}
}
