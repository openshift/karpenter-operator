package azurenodeclass

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	"github.com/awslabs/operatorpkg/status"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

func TestSyncConditions(t *testing.T) {
	const generation int64 = 3

	versionResolved := metav1.Condition{
		Type:    openshiftkarpenterv1.ConditionTypeVersionResolved,
		Status:  metav1.ConditionTrue,
		Reason:  openshiftkarpenterv1.ConditionReasonVersionNotSpecified,
		Message: "Using the control plane release image",
	}
	versionNotResolved := metav1.Condition{
		Type:    openshiftkarpenterv1.ConditionTypeVersionResolved,
		Status:  metav1.ConditionFalse,
		Reason:  openshiftkarpenterv1.ConditionReasonResolutionFailed,
		Message: "version 4.30.0 is not available in channel stable-4.21",
	}
	supportedVersionSkew := metav1.Condition{
		Type:    openshiftkarpenterv1.ConditionTypeSupportedVersionSkew,
		Status:  metav1.ConditionTrue,
		Reason:  openshiftkarpenterv1.ConditionReasonAsExpected,
		Message: "Version skew is within the supported policy",
	}
	upstreamSubnetsReady := status.Condition{
		Type:               azurekarpenterv1beta1.ConditionTypeSubnetsReady,
		Status:             metav1.ConditionTrue,
		Reason:             azurekarpenterv1beta1.ConditionTypeSubnetsReady,
		ObservedGeneration: 1,
	}
	upstreamImagesNotReady := status.Condition{
		Type:               azurekarpenterv1beta1.ConditionTypeImagesReady,
		Status:             metav1.ConditionFalse,
		Reason:             "ImagesNotFound",
		Message:            "no images found",
		ObservedGeneration: 1,
	}
	upstreamReady := status.Condition{
		Type:               openshiftkarpenterv1.ConditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             openshiftkarpenterv1.ConditionTypeReady,
		ObservedGeneration: 1,
	}
	upstreamNotReady := status.Condition{
		Type:               openshiftkarpenterv1.ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "UnhealthyDependents",
		Message:            "ImagesReady=False",
		ObservedGeneration: 1,
	}

	tests := map[string]struct {
		conditions         []metav1.Condition
		upstreamConditions []status.Condition
		want               []metav1.Condition
	}{
		"When the version is resolved, it should copy the AKSNodeClass conditions including Ready": {
			conditions:         []metav1.Condition{versionResolved, supportedVersionSkew},
			upstreamConditions: []status.Condition{upstreamSubnetsReady, upstreamReady},
			want: []metav1.Condition{
				versionResolved,
				supportedVersionSkew,
				metav1.Condition(upstreamSubnetsReady),
				metav1.Condition(upstreamReady),
			},
		},
		"When the AKSNodeClass is not ready, it should copy its Ready condition": {
			conditions:         []metav1.Condition{versionResolved},
			upstreamConditions: []status.Condition{upstreamImagesNotReady, upstreamNotReady},
			want: []metav1.Condition{
				versionResolved,
				metav1.Condition(upstreamImagesNotReady),
				metav1.Condition(upstreamNotReady),
			},
		},
		"When the version is not resolved, it should set Ready to False with the resolution failure": {
			conditions:         []metav1.Condition{versionNotResolved},
			upstreamConditions: []status.Condition{upstreamReady},
			want: []metav1.Condition{
				versionNotResolved,
				{
					Type:               openshiftkarpenterv1.ConditionTypeReady,
					Status:             metav1.ConditionFalse,
					Reason:             openshiftkarpenterv1.ConditionReasonResolutionFailed,
					Message:            "Version resolution failed: version 4.30.0 is not available in channel stable-4.21",
					ObservedGeneration: generation,
				},
			},
		},
		"When neither the version nor the AKSNodeClass are ready, it should prioritize version resolution": {
			conditions:         []metav1.Condition{versionNotResolved},
			upstreamConditions: []status.Condition{upstreamImagesNotReady, upstreamNotReady},
			want: []metav1.Condition{
				versionNotResolved,
				metav1.Condition(upstreamImagesNotReady),
				{
					Type:               openshiftkarpenterv1.ConditionTypeReady,
					Status:             metav1.ConditionFalse,
					Reason:             openshiftkarpenterv1.ConditionReasonResolutionFailed,
					Message:            "Version resolution failed: version 4.30.0 is not available in channel stable-4.21",
					ObservedGeneration: generation,
				},
			},
		},
		"When the version resolution is unknown, it should set Ready to False": {
			upstreamConditions: []status.Condition{upstreamReady},
			want: []metav1.Condition{
				{
					Type:               openshiftkarpenterv1.ConditionTypeReady,
					Status:             metav1.ConditionFalse,
					Reason:             openshiftkarpenterv1.ConditionReasonResolutionFailed,
					Message:            "Version resolution status is unknown",
					ObservedGeneration: generation,
				},
			},
		},
		"When the AKSNodeClass has no conditions yet, it should keep the existing conditions": {
			conditions: []metav1.Condition{versionResolved, supportedVersionSkew},
			want:       []metav1.Condition{versionResolved, supportedVersionSkew},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			got := syncConditions(tc.conditions, tc.upstreamConditions, generation)
			g.Expect(withoutTransitionTimes(got)).To(Equal(tc.want))
		})
	}
}

func TestSyncConditionsDoesNotModifyItsInput(t *testing.T) {
	g := NewWithT(t)
	conditions := []metav1.Condition{{
		Type:   openshiftkarpenterv1.ConditionTypeVersionResolved,
		Status: metav1.ConditionFalse,
		Reason: openshiftkarpenterv1.ConditionReasonResolutionFailed,
	}}
	original := append([]metav1.Condition(nil), conditions...)

	syncConditions(conditions, []status.Condition{{Type: azurekarpenterv1beta1.ConditionTypeSubnetsReady, Status: metav1.ConditionTrue, Reason: "SubnetsReady"}}, 1)

	g.Expect(conditions).To(Equal(original))
}

func TestSyncConditionsKeepsTransitionTimes(t *testing.T) {
	unchanged := metav1.NewTime(time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC))
	upstreamTransition := metav1.NewTime(time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC))

	tests := map[string]struct {
		conditions         []metav1.Condition
		upstreamConditions []status.Condition
	}{
		"When the version stays unresolved while the AKSNodeClass is ready, it should not change the Ready condition": {
			conditions: []metav1.Condition{{
				Type:               openshiftkarpenterv1.ConditionTypeReady,
				Status:             metav1.ConditionFalse,
				Reason:             openshiftkarpenterv1.ConditionReasonResolutionFailed,
				Message:            "Version resolution status is unknown",
				ObservedGeneration: 1,
				LastTransitionTime: unchanged,
			}},
			upstreamConditions: []status.Condition{{
				Type:               openshiftkarpenterv1.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             openshiftkarpenterv1.ConditionTypeReady,
				LastTransitionTime: upstreamTransition,
			}},
		},
		"When the version is resolved and the AKSNodeClass stays ready, it should not change the Ready condition": {
			conditions: []metav1.Condition{
				{
					Type:               openshiftkarpenterv1.ConditionTypeVersionResolved,
					Status:             metav1.ConditionTrue,
					Reason:             openshiftkarpenterv1.ConditionReasonVersionNotSpecified,
					LastTransitionTime: unchanged,
				},
				{
					Type:               openshiftkarpenterv1.ConditionTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             openshiftkarpenterv1.ConditionTypeReady,
					LastTransitionTime: unchanged,
				},
			},
			upstreamConditions: []status.Condition{{
				Type:               openshiftkarpenterv1.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             openshiftkarpenterv1.ConditionTypeReady,
				LastTransitionTime: upstreamTransition,
			}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(syncConditions(tc.conditions, tc.upstreamConditions, 1)).To(Equal(tc.conditions))
		})
	}
}

// withoutTransitionTimes clears the transition times, which meta.SetStatusCondition sets to the current time.
func withoutTransitionTimes(conditions []metav1.Condition) []metav1.Condition {
	result := make([]metav1.Condition, 0, len(conditions))
	for _, condition := range conditions {
		condition.LastTransitionTime = metav1.Time{}
		result = append(result, condition)
	}
	return result
}
