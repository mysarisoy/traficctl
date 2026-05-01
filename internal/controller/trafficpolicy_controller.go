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

package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	trafficv1alpha1 "github.com/yusuf/trafficctl/api/v1alpha1"
	"github.com/yusuf/trafficctl/internal/evaluator"
	"github.com/yusuf/trafficctl/internal/metrics"
	"github.com/yusuf/trafficctl/internal/router"
)

const (
	conditionReady            = "Ready"
	conditionRouteResolved    = "RouteResolved"
	conditionMetricsAvailable = "MetricsAvailable"

	reasonInvalidSpec    = "InvalidSpec"
	reasonPaused         = "Paused"
	reasonReconciled     = "Reconciled"
	reasonRouteMissing   = "RouteMissing"
	reasonRouteWriteErr  = "RouteUpdateFailed"
	reasonWeightsApplied = "WeightsApplied"
	reasonWeightsShifted = "WeightsShifted"
	reasonMetricsOK      = "MetricsAvailable"
	reasonMetricsError   = "MetricsUnavailable"
	reasonResumed        = "Resumed"

	defaultCooldown            = 30 * time.Second
	defaultMaxStepPercent      = int32(20)
	defaultMovingAverageWindow = 3
)

// TrafficPolicyReconciler reconciles a TrafficPolicy object.
type TrafficPolicyReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Weighter     router.Weighter
	MetricSource metrics.Source
	Evaluator    evaluator.Evaluator
	Smoother     *evaluator.Smoother
	// Recorder emits Kubernetes Events tied to the policy. Optional —
	// when nil, events are silently skipped (keeps unit tests lean).
	Recorder record.EventRecorder
}

func (r *TrafficPolicyReconciler) emit(policy *trafficv1alpha1.TrafficPolicy, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(policy, eventType, reason, message)
}

// +kubebuilder:rbac:groups=traffic.traffic.devops.io,resources=trafficpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=traffic.traffic.devops.io,resources=trafficpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=traffic.traffic.devops.io,resources=trafficpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a TrafficPolicy toward its declared steady state:
// validate the spec, honor Paused, (optionally) evaluate metrics and
// step weights within bounds, and apply them to the target HTTPRoute
// via the Weighter.
func (r *TrafficPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var policy trafficv1alpha1.TrafficPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			if r.Smoother != nil {
				r.Smoother.Forget(req.NamespacedName.String())
			}
			forgetPolicyMetrics(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	original := policy.DeepCopy()
	wasPaused := original.Spec.Paused
	prevPhase := original.Status.Phase

	if err := validateSpec(policy.Spec); err != nil {
		log.Info("Rejecting invalid TrafficPolicy spec", "reason", err.Error())
		if prevPhase != trafficv1alpha1.PhaseDegraded {
			r.emit(&policy, corev1.EventTypeWarning, reasonInvalidSpec, err.Error())
		}
		recordFreeze(policy.Namespace, policy.Name, freezeCauseInvalid)
		r.setReady(&policy, metav1.ConditionFalse, reasonInvalidSpec, err.Error())
		policy.Status.Phase = trafficv1alpha1.PhaseDegraded
		policy.Status.ObservedGeneration = policy.Generation
		recordPhase(policy.Namespace, policy.Name, policy.Status.Phase)
		return ctrl.Result{}, r.patchStatusIfChanged(ctx, &policy, original)
	}

	if policy.Spec.Paused {
		if !wasPaused {
			r.emit(&policy, corev1.EventTypeNormal, reasonPaused, "Controller paused; weights frozen")
		}
		recordFreeze(policy.Namespace, policy.Name, freezeCausePaused)
		r.setReady(&policy, metav1.ConditionTrue, reasonPaused, "Controller is paused; weights are frozen")
		policy.Status.Phase = trafficv1alpha1.PhaseFrozen
		policy.Status.ObservedGeneration = policy.Generation
		recordPhase(policy.Namespace, policy.Name, policy.Status.Phase)
		return ctrl.Result{}, r.patchStatusIfChanged(ctx, &policy, original)
	}
	if wasPaused {
		r.emit(&policy, corev1.EventTypeNormal, reasonResumed, "Controller resumed; weights may move again")
	}

	if len(policy.Status.Weights) == 0 {
		policy.Status.Weights = initialWeights(policy.Spec.Backends)
		policy.Status.LastTransitionReason = "Initialized weights within declared bounds"
	}

	phase := trafficv1alpha1.PhaseStable
	r.evaluateMetrics(ctx, &policy, &phase, log)

	if err := r.Weighter.Apply(ctx, policy.Namespace, policy.Spec.RouteName, policy.Status.Weights); err != nil {
		var nf router.NotFoundError
		if errors.As(err, &nf) {
			log.Info("Target HTTPRoute not found", "route", fmt.Sprintf("%s/%s", policy.Namespace, policy.Spec.RouteName))
			if prevPhase != trafficv1alpha1.PhaseDegraded {
				r.emit(&policy, corev1.EventTypeWarning, reasonRouteMissing, nf.Error())
			}
			recordFreeze(policy.Namespace, policy.Name, freezeCauseRoute)
			meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
				Type:               conditionRouteResolved,
				Status:             metav1.ConditionFalse,
				Reason:             reasonRouteMissing,
				Message:            nf.Error(),
				ObservedGeneration: policy.Generation,
			})
			r.setReady(&policy, metav1.ConditionFalse, reasonRouteMissing, nf.Error())
			policy.Status.Phase = trafficv1alpha1.PhaseDegraded
			policy.Status.ObservedGeneration = policy.Generation
			recordPhase(policy.Namespace, policy.Name, policy.Status.Phase)
			return ctrl.Result{RequeueAfter: strategyCooldown(policy.Spec.Strategy)},
				r.patchStatusIfChanged(ctx, &policy, original)
		}
		log.Error(err, "Failed to apply weights to HTTPRoute")
		r.emit(&policy, corev1.EventTypeWarning, reasonRouteWriteErr, err.Error())
		r.setReady(&policy, metav1.ConditionFalse, reasonRouteWriteErr, err.Error())
		policy.Status.ObservedGeneration = policy.Generation
		_ = r.patchStatusIfChanged(ctx, &policy, original)
		return ctrl.Result{}, err
	}
	recordBackendWeights(policy.Namespace, policy.Name, policy.Status.Weights)

	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               conditionRouteResolved,
		Status:             metav1.ConditionTrue,
		Reason:             reasonWeightsApplied,
		Message:            "Weights applied to HTTPRoute",
		ObservedGeneration: policy.Generation,
	})
	r.setReady(&policy, metav1.ConditionTrue, reasonReconciled, "Weights are within declared bounds")

	if phase == trafficv1alpha1.PhaseProgressing {
		r.emit(&policy, corev1.EventTypeNormal, reasonWeightsShifted,
			fmt.Sprintf("%s (new weights: %s)", policy.Status.LastTransitionReason, formatWeights(policy.Status.Weights)))
		recordWeightShift(policy.Namespace, policy.Name, policy.Status.Weights)
	}
	recordPhase(policy.Namespace, policy.Name, phase)

	now := metav1.Now()
	policy.Status.LastEvaluationTime = &now
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Phase = phase

	if err := r.patchStatusIfChanged(ctx, &policy, original); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: strategyCooldown(policy.Spec.Strategy)}, nil
}

func formatWeights(ws []trafficv1alpha1.BackendWeight) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, fmt.Sprintf("%s=%d", w.Name, w.Weight))
	}
	return strings.Join(parts, ",")
}

// evaluateMetrics optionally samples each configured signal and asks the
// evaluator for a new weight proposal. It mutates policy.Status and the
// phase argument in place. All failure paths are non-fatal: the
// reconciler still applies the current weights downstream.
func (r *TrafficPolicyReconciler) evaluateMetrics(
	ctx context.Context,
	policy *trafficv1alpha1.TrafficPolicy,
	phase *trafficv1alpha1.TrafficPolicyPhase,
	log logrLogger,
) {
	specs := signalSpecs(policy.Spec)
	if len(specs) == 0 || r.MetricSource == nil || r.Evaluator == nil {
		return
	}

	names := backendNames(policy.Spec.Backends)
	signals := make([]evaluator.Signal, 0, len(specs))
	for _, spec := range specs {
		samples, err := r.MetricSource.Sample(ctx, spec.query, names)
		if err != nil {
			log.Info("Metrics source unavailable; freezing weights",
				"signal", spec.name, "error", err.Error())
			recordMetricSourceError(policy.Namespace, policy.Name, spec.name)
			recordFreeze(policy.Namespace, policy.Name, freezeCauseMetrics)
			prev := meta.FindStatusCondition(policy.Status.Conditions, conditionMetricsAvailable)
			if prev == nil || prev.Status != metav1.ConditionFalse {
				r.emit(policy, corev1.EventTypeWarning, reasonMetricsError,
					fmt.Sprintf("signal %q: %s", spec.name, err.Error()))
			}
			meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
				Type:               conditionMetricsAvailable,
				Status:             metav1.ConditionFalse,
				Reason:             reasonMetricsError,
				Message:            fmt.Sprintf("%s: %s", spec.name, err.Error()),
				ObservedGeneration: policy.Generation,
			})
			*phase = trafficv1alpha1.PhaseFrozen
			return
		}
		if r.Smoother != nil {
			samples = r.smooth(policy, spec.name, samples)
		}
		signals = append(signals, evaluator.Signal{
			Name:      spec.name,
			Samples:   samples,
			Threshold: spec.threshold,
		})
	}

	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               conditionMetricsAvailable,
		Status:             metav1.ConditionTrue,
		Reason:             reasonMetricsOK,
		Message:            "Metrics source returned samples",
		ObservedGeneration: policy.Generation,
	})

	if !cooldownElapsed(policy) {
		policy.Status.LastTransitionReason = "Within cooldown; holding weights"
		return
	}

	step := policy.Spec.Strategy.MaxStepPercent
	if step <= 0 {
		step = defaultMaxStepPercent
	}
	decision := r.Evaluator.Evaluate(evaluator.Input{
		Backends:       policy.Spec.Backends,
		Current:        policy.Status.Weights,
		Signals:        signals,
		MaxStepPercent: step,
	})
	policy.Status.LastTransitionReason = decision.Reason
	if decision.Changed {
		policy.Status.Weights = decision.Weights
		now := metav1.Now()
		policy.Status.LastWeightChangeTime = &now
		*phase = trafficv1alpha1.PhaseProgressing
	}
}

// smoothedSignal is the per-signal smoothing key. Keeping signals in
// separate buffers means a latency spike does not pollute the error-rate
// moving average (and vice versa).
func (r *TrafficPolicyReconciler) smooth(
	policy *trafficv1alpha1.TrafficPolicy,
	signalName string,
	samples map[string]float64,
) map[string]float64 {
	window := int(policy.Spec.Strategy.MovingAverageWindow)
	if window <= 0 {
		window = defaultMovingAverageWindow
	}
	key := policy.Namespace + "/" + policy.Name + "#" + signalName
	out := make(map[string]float64, len(samples))
	for name, v := range samples {
		out[name] = r.Smoother.Observe(key, name, v, window)
	}
	return out
}

// logrLogger is a narrow interface to avoid leaking logr into signatures
// outside the top-level Reconcile entry point.
type logrLogger interface {
	Info(msg string, keysAndValues ...any)
}

// signalSpec is the reconciler-local projection of a configured metric:
// the name used for smoothing/reason attribution, the PromQL query, and
// the breach threshold in that signal's native units.
type signalSpec struct {
	name      string
	query     string
	threshold float64
}

func signalSpecs(spec trafficv1alpha1.TrafficPolicySpec) []signalSpec {
	if spec.Metrics == nil {
		return nil
	}
	var out []signalSpec
	if m := spec.Metrics.Latency; m != nil {
		out = append(out, signalSpec{
			name:      "latency",
			query:     m.Query,
			threshold: float64(m.ThresholdMs),
		})
	}
	if m := spec.Metrics.ErrorRate; m != nil {
		out = append(out, signalSpec{
			name:      "errorRate",
			query:     m.Query,
			threshold: float64(m.ThresholdPercent),
		})
	}
	return out
}

func backendNames(backends []trafficv1alpha1.Backend) []string {
	out := make([]string, 0, len(backends))
	for _, b := range backends {
		out = append(out, b.Name)
	}
	return out
}

func cooldownElapsed(policy *trafficv1alpha1.TrafficPolicy) bool {
	last := policy.Status.LastWeightChangeTime
	if last == nil {
		return true
	}
	return time.Since(last.Time) >= strategyCooldown(policy.Spec.Strategy)
}

func (r *TrafficPolicyReconciler) setReady(
	policy *trafficv1alpha1.TrafficPolicy,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
	})
}

func (r *TrafficPolicyReconciler) patchStatusIfChanged(
	ctx context.Context,
	desired, original *trafficv1alpha1.TrafficPolicy,
) error {
	if equality.Semantic.DeepEqual(original.Status, desired.Status) {
		return nil
	}
	return r.Status().Patch(ctx, desired, client.MergeFrom(original))
}

// validateSpec enforces the invariants needed for a valid weight allocation:
// unique backend names, per-backend min <= max, and the summed bounds must
// straddle 100.
func validateSpec(spec trafficv1alpha1.TrafficPolicySpec) error {
	if len(spec.Backends) < 2 {
		return fmt.Errorf("at least two backends are required, got %d", len(spec.Backends))
	}
	seen := make(map[string]struct{}, len(spec.Backends))
	var minSum, maxSum int32
	for _, b := range spec.Backends {
		if b.Name == "" {
			return fmt.Errorf("backend name must not be empty")
		}
		if _, dup := seen[b.Name]; dup {
			return fmt.Errorf("duplicate backend name %q", b.Name)
		}
		seen[b.Name] = struct{}{}
		if b.MinWeight > b.MaxWeight {
			return fmt.Errorf("backend %q: minWeight %d exceeds maxWeight %d", b.Name, b.MinWeight, b.MaxWeight)
		}
		minSum += b.MinWeight
		maxSum += b.MaxWeight
	}
	if minSum > 100 {
		return fmt.Errorf("sum of minWeights (%d) exceeds 100", minSum)
	}
	if maxSum < 100 {
		return fmt.Errorf("sum of maxWeights (%d) is less than 100", maxSum)
	}
	return nil
}

// initialWeights returns a starting allocation summing to 100: each backend
// gets its minWeight, and the remainder is distributed proportionally to
// each backend's headroom (maxWeight - minWeight) using largest-remainder
// rounding.
func initialWeights(backends []trafficv1alpha1.Backend) []trafficv1alpha1.BackendWeight {
	weights := make([]trafficv1alpha1.BackendWeight, len(backends))
	var used, totalHead int32
	for i, b := range backends {
		weights[i] = trafficv1alpha1.BackendWeight{Name: b.Name, Weight: b.MinWeight}
		used += b.MinWeight
		totalHead += b.MaxWeight - b.MinWeight
	}
	remainder := int32(100) - used
	if remainder <= 0 || totalHead <= 0 {
		return weights
	}

	type share struct {
		idx  int
		frac float64
	}
	shares := make([]share, len(backends))
	var assigned int32
	for i, b := range backends {
		head := b.MaxWeight - b.MinWeight
		exact := float64(remainder) * float64(head) / float64(totalHead)
		whole := int32(exact)
		weights[i].Weight += whole
		assigned += whole
		shares[i] = share{i, exact - float64(whole)}
	}
	leftover := remainder - assigned
	if leftover == 0 {
		return weights
	}
	sort.SliceStable(shares, func(i, j int) bool { return shares[i].frac > shares[j].frac })
	for _, s := range shares {
		if leftover == 0 {
			break
		}
		if weights[s.idx].Weight < backends[s.idx].MaxWeight {
			weights[s.idx].Weight++
			leftover--
		}
	}
	return weights
}

func strategyCooldown(s trafficv1alpha1.Strategy) time.Duration {
	if s.CooldownSeconds <= 0 {
		return defaultCooldown
	}
	return time.Duration(s.CooldownSeconds) * time.Second
}

// SetupWithManager sets up the controller with the Manager.
func (r *TrafficPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&trafficv1alpha1.TrafficPolicy{}).
		Named("trafficpolicy").
		Complete(r)
}
