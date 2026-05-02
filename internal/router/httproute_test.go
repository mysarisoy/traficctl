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

package router

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	trafficv1alpha1 "github.com/mysarisoy/trafficctl/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := gatewayv1.Install(s); err != nil {
		t.Fatalf("install gateway scheme: %v", err)
	}
	return s
}

func ptrInt32(v int32) *int32 { return &v }

func buildRoute() *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "auth-route", Namespace: "prod"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "auth-v1"},
						Weight:                 ptrInt32(50),
					}},
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "auth-v2"},
						Weight:                 ptrInt32(50),
					}},
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "unrelated"},
						Weight:                 ptrInt32(10),
					}},
				},
			}},
		},
	}
}

func TestHTTPRouteWeighter_ApplyUpdatesMatchingBackends(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(buildRoute()).
		Build()

	w := NewHTTPRouteWeighter(c)
	err := w.Apply(context.Background(), "prod", "auth-route", []trafficv1alpha1.BackendWeight{
		{Name: "auth-v1", Weight: 70},
		{Name: "auth-v2", Weight: 30},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got gatewayv1.HTTPRoute
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "auth-route"}, &got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	refs := got.Spec.Rules[0].BackendRefs
	weights := map[string]int32{}
	for _, r := range refs {
		if r.Weight != nil {
			weights[string(r.Name)] = *r.Weight
		}
	}
	if weights["auth-v1"] != 70 || weights["auth-v2"] != 30 {
		t.Errorf("unexpected managed weights: %v", weights)
	}
	if weights["unrelated"] != 10 {
		t.Errorf("out-of-scope backendRef was modified: got %d, want 10", weights["unrelated"])
	}
}

func TestHTTPRouteWeighter_ApplyReturnsNotFound(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	w := NewHTTPRouteWeighter(c)
	err := w.Apply(context.Background(), "prod", "missing", []trafficv1alpha1.BackendWeight{
		{Name: "auth-v1", Weight: 100},
	})

	var nf NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("want NotFoundError, got %v", err)
	}
	if nf.Namespace != "prod" || nf.Name != "missing" {
		t.Errorf("wrong NotFoundError fields: %+v", nf)
	}
}

func TestHTTPRouteWeighter_ApplyIsIdempotent(t *testing.T) {
	scheme := newTestScheme(t)
	route := buildRoute()
	route.Spec.Rules[0].BackendRefs[0].Weight = ptrInt32(70)
	route.Spec.Rules[0].BackendRefs[1].Weight = ptrInt32(30)

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route).Build()
	w := NewHTTPRouteWeighter(c)

	err := w.Apply(context.Background(), "prod", "auth-route", []trafficv1alpha1.BackendWeight{
		{Name: "auth-v1", Weight: 70},
		{Name: "auth-v2", Weight: 30},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got gatewayv1.HTTPRoute
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "auth-route"}, &got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	// ResourceVersion bump would indicate a wasted write.
	if got.ResourceVersion != route.ResourceVersion {
		t.Errorf("Apply wrote the route despite no change (rv %q -> %q)",
			route.ResourceVersion, got.ResourceVersion)
	}
}
