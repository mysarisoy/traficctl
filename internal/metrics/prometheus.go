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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultBackendLabel is the Prometheus label the source reads to
// discover which backend a sample belongs to. Queries must project a
// label of this name (e.g. `sum by (backend) (...)`).
const DefaultBackendLabel = "backend"

// PromSource is a Source backed by a Prometheus-compatible HTTP API
// (Prometheus, Thanos query, Mimir). It executes instant queries against
// /api/v1/query and extracts one scalar per backend using BackendLabel.
type PromSource struct {
	// Address is the base URL of the Prometheus API (no trailing path),
	// e.g. "http://prometheus.monitoring:9090".
	Address string
	// HTTPClient is optional; http.DefaultClient is used when nil.
	HTTPClient *http.Client
	// BackendLabel is the label whose value identifies the backend. An
	// empty value falls back to DefaultBackendLabel.
	BackendLabel string
}

// NewPromSource constructs a PromSource with sane defaults.
func NewPromSource(address string, httpClient *http.Client) *PromSource {
	return &PromSource{
		Address:      address,
		HTTPClient:   httpClient,
		BackendLabel: DefaultBackendLabel,
	}
}

// Sample implements Source. It filters the vector result by backends —
// unknown labels are ignored so that over-broad queries don't leak data
// into the decision.
func (p *PromSource) Sample(ctx context.Context, query string, backends []string) (map[string]float64, error) {
	if strings.TrimSpace(p.Address) == "" {
		return nil, fmt.Errorf("prometheus: Address is not configured")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("prometheus: query is empty")
	}

	endpoint, err := url.Parse(strings.TrimRight(p.Address, "/") + "/api/v1/query")
	if err != nil {
		return nil, fmt.Errorf("prometheus: invalid address %q: %w", p.Address, err)
	}

	form := url.Values{}
	form.Set("query", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("prometheus: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := p.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus: query request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("prometheus: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Prometheus returns machine-readable JSON for 4xx as well; try
		// to surface the error field before falling back to raw body.
		if msg := extractErrorMessage(body); msg != "" {
			return nil, fmt.Errorf("prometheus: http %d: %s", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("prometheus: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed queryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("prometheus: decode response: %w", err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("prometheus: query failed (%s): %s", parsed.ErrorType, parsed.Error)
	}
	if parsed.Data.ResultType != "vector" {
		return nil, fmt.Errorf("prometheus: expected vector result, got %q", parsed.Data.ResultType)
	}

	wanted := make(map[string]struct{}, len(backends))
	for _, b := range backends {
		wanted[b] = struct{}{}
	}
	label := p.BackendLabel
	if label == "" {
		label = DefaultBackendLabel
	}

	out := make(map[string]float64, len(parsed.Data.Result))
	for _, s := range parsed.Data.Result {
		name, ok := s.Metric[label]
		if !ok {
			continue
		}
		if _, requested := wanted[name]; !requested {
			continue
		}
		if len(s.Value) != 2 {
			return nil, fmt.Errorf("prometheus: unexpected value shape for %s=%q: %v", label, name, s.Value)
		}
		raw, ok := s.Value[1].(string)
		if !ok {
			return nil, fmt.Errorf("prometheus: non-string value for %s=%q", label, name)
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("prometheus: parse value for %s=%q: %w", label, name, err)
		}
		out[name] = v
	}
	return out, nil
}

type queryResponse struct {
	Status    string    `json:"status"`
	ErrorType string    `json:"errorType,omitempty"`
	Error     string    `json:"error,omitempty"`
	Data      queryData `json:"data"`
}

type queryData struct {
	ResultType string         `json:"resultType"`
	Result     []vectorSample `json:"result"`
}

type vectorSample struct {
	Metric map[string]string `json:"metric"`
	// Value is [unix_seconds, "value-string"] per the Prometheus HTTP API.
	Value []any `json:"value"`
}

func extractErrorMessage(body []byte) string {
	var parsed queryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.Error != "" {
		return parsed.Error
	}
	return ""
}
