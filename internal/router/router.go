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

// Package router translates TrafficPolicy decisions into writes against
// the underlying routing layer (currently Gateway API HTTPRoute).
package router

import (
	"context"
	"fmt"

	trafficv1alpha1 "github.com/yusuf/trafficctl/api/v1alpha1"
)

// Weighter applies backend weights to an external routing resource. The
// reconciler consumes this interface so decision logic stays independent
// of any particular routing implementation.
type Weighter interface {
	// Apply sets the weight for each named backend on the route identified
	// by namespace/name. Implementations must be idempotent and treat
	// backendRefs whose names are not in weights as out of scope (leave
	// their weight unchanged).
	Apply(ctx context.Context, namespace, routeName string, weights []trafficv1alpha1.BackendWeight) error
}

// NotFoundError is returned when the target routing resource cannot be
// resolved. The reconciler uses errors.As to distinguish this from
// transient failures.
type NotFoundError struct {
	Namespace string
	Name      string
}

func (e NotFoundError) Error() string {
	return fmt.Sprintf("route %s/%s not found", e.Namespace, e.Name)
}
