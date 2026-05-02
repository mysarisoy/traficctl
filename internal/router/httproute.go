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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	trafficv1alpha1 "github.com/mysarisoy/trafficctl/api/v1alpha1"
)

// HTTPRouteWeighter writes backend weights onto a Gateway API HTTPRoute.
// Matching is by backendRef Name within the same namespace as the route.
type HTTPRouteWeighter struct {
	Client client.Client
}

// NewHTTPRouteWeighter returns a Weighter that targets Gateway API
// HTTPRoute resources via the provided controller-runtime client.
func NewHTTPRouteWeighter(c client.Client) *HTTPRouteWeighter {
	return &HTTPRouteWeighter{Client: c}
}

// Apply resolves the target HTTPRoute and patches every backendRef whose
// name appears in weights. BackendRefs with unknown names are left
// untouched. A missing HTTPRoute is reported as NotFoundError.
func (w *HTTPRouteWeighter) Apply(
	ctx context.Context,
	namespace, routeName string,
	weights []trafficv1alpha1.BackendWeight,
) error {
	var route gatewayv1.HTTPRoute
	key := types.NamespacedName{Namespace: namespace, Name: routeName}
	if err := w.Client.Get(ctx, key, &route); err != nil {
		if apierrors.IsNotFound(err) {
			return NotFoundError{Namespace: namespace, Name: routeName}
		}
		return err
	}

	desired := make(map[string]int32, len(weights))
	for _, bw := range weights {
		desired[bw.Name] = bw.Weight
	}

	original := route.DeepCopy()
	changed := false
	for ri := range route.Spec.Rules {
		rule := &route.Spec.Rules[ri]
		for bi := range rule.BackendRefs {
			ref := &rule.BackendRefs[bi]
			weight, ok := desired[string(ref.Name)]
			if !ok {
				continue
			}
			if ref.Weight == nil || *ref.Weight != weight {
				w := weight
				ref.Weight = &w
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return w.Client.Patch(ctx, &route, client.MergeFrom(original))
}
