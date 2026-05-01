package controller

import (
	trafficv1alpha1 "github.com/yusuf/trafficctl/api/v1alpha1"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/record"
)

type runtimeEventRecorder struct {
	recorder events.EventRecorder
}

func NewRuntimeEventRecorder(recorder events.EventRecorder) EventRecorder {
	if recorder == nil {
		return nil
	}
	return runtimeEventRecorder{recorder: recorder}
}

func (r runtimeEventRecorder) Event(policy *trafficv1alpha1.TrafficPolicy, eventType, reason, message string) {
	r.recorder.Eventf(policy, nil, eventType, reason, reason, message)
}

type legacyEventRecorder struct {
	recorder record.EventRecorder
}

func NewLegacyEventRecorder(recorder record.EventRecorder) EventRecorder {
	if recorder == nil {
		return nil
	}
	return legacyEventRecorder{recorder: recorder}
}

func (r legacyEventRecorder) Event(policy *trafficv1alpha1.TrafficPolicy, eventType, reason, message string) {
	r.recorder.Event(policy, eventType, reason, message)
}
