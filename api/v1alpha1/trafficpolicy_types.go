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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TrafficPolicySpec defines the desired state of a TrafficPolicy.
type TrafficPolicySpec struct {
	// RouteName is the name of the Gateway API HTTPRoute in the same namespace
	// whose backend weights this policy manages.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RouteName string `json:"routeName"`

	// Backends declares the versions whose traffic weights are controlled.
	// At least two backends must be declared; weights across backends always
	// sum to 100.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=2
	Backends []Backend `json:"backends"`

	// Metrics declares the signals driving weight decisions. When omitted,
	// the controller holds weights at their current value (last known good).
	// +optional
	Metrics *MetricSpec `json:"metrics,omitempty"`

	// Strategy bounds how aggressively the controller may change weights.
	// +optional
	Strategy Strategy `json:"strategy,omitempty"`

	// Paused freezes the controller: observed metrics are still recorded but
	// no weight changes are applied. Useful for incident response.
	// +optional
	Paused bool `json:"paused,omitempty"`
}

// Backend identifies a version participating in the split and the bounds
// within which its weight may move.
type Backend struct {
	// Name matches a backendRef name on the target HTTPRoute.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// MinWeight is the lowest weight (0-100) this backend may receive.
	// Prevents starvation; useful for keeping a canary under observation.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MinWeight int32 `json:"minWeight"`

	// MaxWeight is the highest weight (0-100) this backend may receive.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MaxWeight int32 `json:"maxWeight"`
}

// MetricSpec bundles the signals the controller reads from Prometheus.
// Backends are shifted away whenever ANY configured signal is breached;
// configure only the signals you actually want to act on.
type MetricSpec struct {
	// Latency applies a P95-style threshold check against a PromQL query.
	// +optional
	Latency *LatencyMetric `json:"latency,omitempty"`

	// ErrorRate flags backends whose HTTP error rate (e.g. 5xx fraction)
	// crosses a threshold.
	// +optional
	ErrorRate *ErrorRateMetric `json:"errorRate,omitempty"`
}

// LatencyMetric is a PromQL query whose result is compared against a
// millisecond threshold. Backends whose observed latency exceeds the
// threshold lose weight toward healthier peers.
type LatencyMetric struct {
	// Query is the PromQL expression returning a vector keyed by backend.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Query string `json:"query"`

	// ThresholdMs is the latency ceiling in milliseconds.
	// +kubebuilder:validation:Minimum=1
	ThresholdMs int32 `json:"thresholdMs"`
}

// ErrorRateMetric is a PromQL query whose result is compared against a
// percent threshold. The query is expected to return the error rate as a
// percentage (0-100) keyed by backend — e.g.
//
//	100 * sum by (backend) (rate(http_requests_total{status=~"5.."}[1m]))
//	    / sum by (backend) (rate(http_requests_total[1m]))
//
// Backends whose observed rate exceeds the threshold lose weight.
type ErrorRateMetric struct {
	// Query is the PromQL expression returning a per-backend error-rate
	// percentage (0-100).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Query string `json:"query"`

	// ThresholdPercent is the error-rate ceiling, expressed as percent
	// (1-100). Sub-percent thresholds can be achieved in PromQL by
	// scaling the query (e.g. report in per-mille and set threshold
	// accordingly).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	ThresholdPercent int32 `json:"thresholdPercent"`
}

// Strategy bounds the rate and smoothness of weight changes.
type Strategy struct {
	// CooldownSeconds is the minimum wait between two weight changes.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=30
	// +optional
	CooldownSeconds int32 `json:"cooldownSeconds,omitempty"`

	// MaxStepPercent caps how many percentage points a single decision may
	// shift per backend.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=20
	// +optional
	MaxStepPercent int32 `json:"maxStepPercent,omitempty"`

	// MovingAverageWindow is the number of recent metric samples blended
	// before a decision, smoothing transient spikes.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	// +optional
	MovingAverageWindow int32 `json:"movingAverageWindow,omitempty"`
}

// TrafficPolicyPhase is a coarse summary of controller behavior, aimed at
// human operators and dashboards.
// +kubebuilder:validation:Enum=Pending;Stable;Progressing;Frozen;Degraded
type TrafficPolicyPhase string

const (
	// PhasePending means the controller has not yet evaluated the policy.
	PhasePending TrafficPolicyPhase = "Pending"
	// PhaseStable means weights are within bounds and no change is needed.
	PhaseStable TrafficPolicyPhase = "Stable"
	// PhaseProgressing means weights are actively shifting.
	PhaseProgressing TrafficPolicyPhase = "Progressing"
	// PhaseFrozen means weights are held: either .spec.paused or a
	// degraded metrics source.
	PhaseFrozen TrafficPolicyPhase = "Frozen"
	// PhaseDegraded means the policy cannot progress (e.g. missing route,
	// invalid spec) and operator attention is required.
	PhaseDegraded TrafficPolicyPhase = "Degraded"
)

// BackendWeight is the current weight applied to a backend.
type BackendWeight struct {
	Name   string `json:"name"`
	Weight int32  `json:"weight"`
}

// TrafficPolicyStatus defines the observed state of a TrafficPolicy.
type TrafficPolicyStatus struct {
	// Phase is a coarse summary of controller behavior.
	// +optional
	Phase TrafficPolicyPhase `json:"phase,omitempty"`

	// Weights are the weights the controller most recently applied.
	// +listType=map
	// +listMapKey=name
	// +optional
	Weights []BackendWeight `json:"weights,omitempty"`

	// LastEvaluationTime is when the controller last ran a decision cycle.
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`

	// LastWeightChangeTime is when the controller last changed the applied
	// weights. Used to enforce Strategy.CooldownSeconds.
	// +optional
	LastWeightChangeTime *metav1.Time `json:"lastWeightChangeTime,omitempty"`

	// LastTransitionReason is a short human-readable explanation of the
	// most recent weight change, e.g. "auth-v2 latency over threshold".
	// +optional
	LastTransitionReason string `json:"lastTransitionReason,omitempty"`

	// ObservedGeneration is the .metadata.generation the controller last
	// processed. Lets clients detect stale status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions follow the standard Kubernetes pattern. Expected types:
	// "Ready", "MetricsAvailable", "RouteResolved".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tp
// +kubebuilder:printcolumn:name="Route",type=string,JSONPath=".spec.routeName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Paused",type=boolean,JSONPath=".spec.paused"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// TrafficPolicy is the Schema for the trafficpolicies API.
type TrafficPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of TrafficPolicy
	// +required
	Spec TrafficPolicySpec `json:"spec"`

	// status defines the observed state of TrafficPolicy
	// +optional
	Status TrafficPolicyStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TrafficPolicyList contains a list of TrafficPolicy
type TrafficPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TrafficPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TrafficPolicy{}, &TrafficPolicyList{})
}
