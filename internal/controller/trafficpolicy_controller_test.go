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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	trafficv1alpha1 "github.com/mysarisoy/trafficctl/api/v1alpha1"
	"github.com/mysarisoy/trafficctl/internal/evaluator"
	"github.com/mysarisoy/trafficctl/internal/metrics"
	"github.com/mysarisoy/trafficctl/internal/router"
)

type fakeWeighterCall struct {
	Namespace string
	RouteName string
	Weights   []trafficv1alpha1.BackendWeight
}

type fakeWeighter struct {
	err   error
	calls []fakeWeighterCall
}

func (f *fakeWeighter) Apply(
	_ context.Context,
	namespace, routeName string,
	weights []trafficv1alpha1.BackendWeight,
) error {
	f.calls = append(f.calls, fakeWeighterCall{
		Namespace: namespace,
		RouteName: routeName,
		Weights:   append([]trafficv1alpha1.BackendWeight(nil), weights...),
	})
	return f.err
}

func weightsByName(ws []trafficv1alpha1.BackendWeight) map[string]int32 {
	m := map[string]int32{}
	for _, w := range ws {
		m[w.Name] = w.Weight
	}
	return m
}

var _ = Describe("TrafficPolicy Controller", func() {
	Context("When reconciling a resource without metrics", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		trafficpolicy := &trafficv1alpha1.TrafficPolicy{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind TrafficPolicy")
			err := k8sClient.Get(ctx, typeNamespacedName, trafficpolicy)
			if err != nil && errors.IsNotFound(err) {
				resource := &trafficv1alpha1.TrafficPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: trafficv1alpha1.TrafficPolicySpec{
						RouteName: "auth-route",
						Backends: []trafficv1alpha1.Backend{
							{Name: "auth-v1", MinWeight: 20, MaxWeight: 100},
							{Name: "auth-v2", MinWeight: 10, MaxWeight: 80},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &trafficv1alpha1.TrafficPolicy{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance TrafficPolicy")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("applies initial weights via the Weighter and marks Ready", func() {
			w := &fakeWeighter{}
			r := &TrafficPolicyReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Weighter: w,
			}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(w.calls).To(HaveLen(1))
			call := w.calls[0]
			Expect(call.Namespace).To(Equal("default"))
			Expect(call.RouteName).To(Equal("auth-route"))
			Expect(call.Weights).To(HaveLen(2))

			updated := &trafficv1alpha1.TrafficPolicy{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())

			Expect(updated.Status.Phase).To(Equal(trafficv1alpha1.PhaseStable))
			var total int32
			for _, wt := range updated.Status.Weights {
				total += wt.Weight
			}
			Expect(total).To(Equal(int32(100)))

			ready := meta.FindStatusCondition(updated.Status.Conditions, conditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))

			resolved := meta.FindStatusCondition(updated.Status.Conditions, conditionRouteResolved)
			Expect(resolved).NotTo(BeNil())
			Expect(resolved.Status).To(Equal(metav1.ConditionTrue))
		})

		It("reports Degraded and emits a Warning event when the target HTTPRoute is missing", func() {
			w := &fakeWeighter{err: router.NotFoundError{Namespace: "default", Name: "auth-route"}}
			rec := record.NewFakeRecorder(4)
			r := &TrafficPolicyReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Weighter: w,
				Recorder: NewLegacyEventRecorder(rec),
			}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &trafficv1alpha1.TrafficPolicy{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())

			Expect(updated.Status.Phase).To(Equal(trafficv1alpha1.PhaseDegraded))

			resolved := meta.FindStatusCondition(updated.Status.Conditions, conditionRouteResolved)
			Expect(resolved).NotTo(BeNil())
			Expect(resolved.Status).To(Equal(metav1.ConditionFalse))
			Expect(resolved.Reason).To(Equal(reasonRouteMissing))

			Eventually(rec.Events).Should(Receive(ContainSubstring(reasonRouteMissing)))
		})
	})

	Context("When reconciling a resource with metrics", func() {
		const resourceName = "metrics-resource"

		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			policy := &trafficv1alpha1.TrafficPolicy{}
			err := k8sClient.Get(ctx, typeNamespacedName, policy)
			if err != nil && errors.IsNotFound(err) {
				resource := &trafficv1alpha1.TrafficPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: trafficv1alpha1.TrafficPolicySpec{
						RouteName: "auth-route",
						Backends: []trafficv1alpha1.Backend{
							{Name: "auth-v1", MinWeight: 20, MaxWeight: 100},
							{Name: "auth-v2", MinWeight: 10, MaxWeight: 80},
						},
						Metrics: &trafficv1alpha1.MetricSpec{
							Latency: &trafficv1alpha1.LatencyMetric{
								Query:       "p95_latency_ms",
								ThresholdMs: 500,
							},
						},
						Strategy: trafficv1alpha1.Strategy{
							CooldownSeconds:     30,
							MaxStepPercent:      20,
							MovingAverageWindow: 1,
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &trafficv1alpha1.TrafficPolicy{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("shifts weight away from an unhealthy backend and emits a Normal event", func() {
			w := &fakeWeighter{}
			src := &metrics.StaticSource{Values: map[string]float64{
				"auth-v1": 120,
				"auth-v2": 800,
			}}
			rec := record.NewFakeRecorder(4)
			r := &TrafficPolicyReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				Weighter:     w,
				MetricSource: src,
				Evaluator:    evaluator.NewThresholdEvaluator(),
				Smoother:     evaluator.NewSmoother(evaluator.DefaultMaxWindow),
				Recorder:     NewLegacyEventRecorder(rec),
			}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(w.calls).To(HaveLen(1))
			applied := weightsByName(w.calls[0].Weights)
			// Initial allocation is v1=57, v2=43 (headroom-proportional).
			// A 20pp per-backend shift gives v1=77, v2=23.
			Expect(applied["auth-v1"]).To(Equal(int32(77)))
			Expect(applied["auth-v2"]).To(Equal(int32(23)))

			updated := &trafficv1alpha1.TrafficPolicy{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())

			Expect(updated.Status.Phase).To(Equal(trafficv1alpha1.PhaseProgressing))
			Expect(updated.Status.LastWeightChangeTime).NotTo(BeNil())

			ma := meta.FindStatusCondition(updated.Status.Conditions, conditionMetricsAvailable)
			Expect(ma).NotTo(BeNil())
			Expect(ma.Status).To(Equal(metav1.ConditionTrue))

			Eventually(rec.Events).Should(Receive(ContainSubstring(reasonWeightsShifted)))
		})

		It("shifts weight when only error-rate breaches its threshold", func() {
			const errRateName = "metrics-errorrate"
			errRateKey := types.NamespacedName{Name: errRateName, Namespace: "default"}
			By("creating a policy driven by an error-rate metric")
			errRate := &trafficv1alpha1.TrafficPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: errRateName, Namespace: "default"},
				Spec: trafficv1alpha1.TrafficPolicySpec{
					RouteName: "auth-route",
					Backends: []trafficv1alpha1.Backend{
						{Name: "auth-v1", MinWeight: 0, MaxWeight: 100},
						{Name: "auth-v2", MinWeight: 0, MaxWeight: 100},
					},
					Metrics: &trafficv1alpha1.MetricSpec{
						ErrorRate: &trafficv1alpha1.ErrorRateMetric{
							Query:            "error_rate_percent",
							ThresholdPercent: 1,
						},
					},
					Strategy: trafficv1alpha1.Strategy{
						CooldownSeconds:     30,
						MaxStepPercent:      10,
						MovingAverageWindow: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, errRate)).To(Succeed())
			defer func() {
				cleanup := &trafficv1alpha1.TrafficPolicy{}
				Expect(k8sClient.Get(ctx, errRateKey, cleanup)).To(Succeed())
				Expect(k8sClient.Delete(ctx, cleanup)).To(Succeed())
			}()

			w := &fakeWeighter{}
			src := &metrics.StaticSource{Values: map[string]float64{
				"auth-v1": 0.2,
				"auth-v2": 6,
			}}
			r := &TrafficPolicyReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				Weighter:     w,
				MetricSource: src,
				Evaluator:    evaluator.NewThresholdEvaluator(),
				Smoother:     evaluator.NewSmoother(evaluator.DefaultMaxWindow),
			}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: errRateKey})
			Expect(err).NotTo(HaveOccurred())

			Expect(w.calls).To(HaveLen(1))
			applied := weightsByName(w.calls[0].Weights)
			// Initial allocation: v1=50, v2=50. A 10pp step shifts away
			// from the error-heavy v2 toward the healthy v1.
			Expect(applied["auth-v1"]).To(Equal(int32(60)))
			Expect(applied["auth-v2"]).To(Equal(int32(40)))

			updated := &trafficv1alpha1.TrafficPolicy{}
			Expect(k8sClient.Get(ctx, errRateKey, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(trafficv1alpha1.PhaseProgressing))
		})

		It("freezes weights and emits a Warning event when the metrics source is unavailable", func() {
			w := &fakeWeighter{}
			src := &metrics.StaticSource{Err: errors.NewServiceUnavailable("prometheus down")}
			rec := record.NewFakeRecorder(4)
			r := &TrafficPolicyReconciler{
				Client:       k8sClient,
				Scheme:       k8sClient.Scheme(),
				Weighter:     w,
				MetricSource: src,
				Evaluator:    evaluator.NewThresholdEvaluator(),
				Smoother:     evaluator.NewSmoother(evaluator.DefaultMaxWindow),
				Recorder:     NewLegacyEventRecorder(rec),
			}

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &trafficv1alpha1.TrafficPolicy{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())

			Expect(updated.Status.Phase).To(Equal(trafficv1alpha1.PhaseFrozen))
			Expect(updated.Status.LastWeightChangeTime).To(BeNil())

			ma := meta.FindStatusCondition(updated.Status.Conditions, conditionMetricsAvailable)
			Expect(ma).NotTo(BeNil())
			Expect(ma.Status).To(Equal(metav1.ConditionFalse))
			Expect(ma.Reason).To(Equal(reasonMetricsError))

			Eventually(rec.Events).Should(Receive(ContainSubstring(reasonMetricsError)))
		})
	})
})
