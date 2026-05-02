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

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	trafficv1alpha1 "github.com/mysarisoy/trafficctl/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(trafficv1alpha1.AddToScheme(s))
	return s
}

func samplePolicy(name, ns string, paused bool) *trafficv1alpha1.TrafficPolicy {
	return &trafficv1alpha1.TrafficPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: trafficv1alpha1.TrafficPolicySpec{
			RouteName: "auth-route",
			Paused:    paused,
			Backends: []trafficv1alpha1.Backend{
				{Name: "v1", MinWeight: 20, MaxWeight: 100},
				{Name: "v2", MinWeight: 10, MaxWeight: 80},
			},
		},
		Status: trafficv1alpha1.TrafficPolicyStatus{
			Phase: trafficv1alpha1.PhaseStable,
			Weights: []trafficv1alpha1.BackendWeight{
				{Name: "v1", Weight: 70},
				{Name: "v2", Weight: 30},
			},
		},
	}
}

func newFakeClient(t *testing.T, objs ...ctrlclient.Object) ctrlclient.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
}

func TestRunList_PrintsTable(t *testing.T) {
	c := newFakeClient(t, samplePolicy("auth-api", "default", false))
	var out bytes.Buffer
	if err := runList(context.Background(), c, "default", false, &out); err != nil {
		t.Fatalf("runList: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "auth-api") || !strings.Contains(body, "auth-route") {
		t.Errorf("expected listing to include name and route, got:\n%s", body)
	}
	if !strings.Contains(body, "v1=70") {
		t.Errorf("expected weights in listing, got:\n%s", body)
	}
}

func TestRunStatus_PrintsDetails(t *testing.T) {
	c := newFakeClient(t, samplePolicy("auth-api", "default", false))
	var out bytes.Buffer
	if err := runStatus(context.Background(), c, "default", "auth-api", &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	body := out.String()
	for _, needle := range []string{"auth-api", "auth-route", "Backends:", "v1", "v2", "Stable"} {
		if !strings.Contains(body, needle) {
			t.Errorf("expected output to contain %q, got:\n%s", needle, body)
		}
	}
}

func TestTogglePaused_FlipsSpec(t *testing.T) {
	c := newFakeClient(t, samplePolicy("auth-api", "default", false))
	var out bytes.Buffer
	if err := togglePaused(context.Background(), c, "default", "auth-api", true, &out); err != nil {
		t.Fatalf("togglePaused: %v", err)
	}
	if !strings.Contains(out.String(), "frozen") {
		t.Errorf("expected confirmation message, got %q", out.String())
	}

	got := &trafficv1alpha1.TrafficPolicy{}
	if err := c.Get(context.Background(), ctrlclient.ObjectKey{Namespace: "default", Name: "auth-api"}, got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Spec.Paused {
		t.Errorf("expected Paused=true after freeze")
	}
}

func TestTogglePaused_NoopWhenAlreadySet(t *testing.T) {
	c := newFakeClient(t, samplePolicy("auth-api", "default", true))
	var out bytes.Buffer
	if err := togglePaused(context.Background(), c, "default", "auth-api", true, &out); err != nil {
		t.Fatalf("togglePaused: %v", err)
	}
	if !strings.Contains(out.String(), "already paused=true") {
		t.Errorf("expected noop message, got %q", out.String())
	}
}

func TestRunList_AllNamespaces(t *testing.T) {
	c := newFakeClient(t,
		samplePolicy("a", "ns1", false),
		samplePolicy("b", "ns2", false),
	)
	var out bytes.Buffer
	if err := runList(context.Background(), c, "", true, &out); err != nil {
		t.Fatalf("runList: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "NAMESPACE") {
		t.Errorf("expected NAMESPACE column header, got:\n%s", body)
	}
	if !strings.Contains(body, "ns1") || !strings.Contains(body, "ns2") {
		t.Errorf("expected entries from both namespaces, got:\n%s", body)
	}
}
