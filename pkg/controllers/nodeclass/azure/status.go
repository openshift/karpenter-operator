package azurenodeclass

import (
	"fmt"
	"slices"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	"github.com/awslabs/operatorpkg/status"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// syncConditions returns the OpenShiftAzureNodeClass conditions with the AKSNodeClass conditions copied in.
// Ready is forced to False while the version is not resolved. The conditions owned by the ignition controller,
// VersionResolved and SupportedVersionSkew, are kept as they are.
// Ready is set only once, so that an unchanged effective readiness keeps its transition time.
func syncConditions(conditions []metav1.Condition, upstreamConditions []status.Condition, generation int64) []metav1.Condition {
	result := slices.Clone(conditions)
	var upstreamReady *metav1.Condition
	for _, upstreamCondition := range upstreamConditions {
		condition := metav1.Condition(upstreamCondition)
		if condition.Type == openshiftkarpenterv1.ConditionTypeReady {
			upstreamReady = &condition
			continue
		}
		meta.SetStatusCondition(&result, condition)
	}

	versionResolved := meta.FindStatusCondition(result, openshiftkarpenterv1.ConditionTypeVersionResolved)
	if versionResolved != nil && versionResolved.Status == metav1.ConditionTrue {
		if upstreamReady != nil {
			meta.SetStatusCondition(&result, *upstreamReady)
		}
		return result
	}

	notReady := metav1.Condition{
		Type:               openshiftkarpenterv1.ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: generation,
		Reason:             openshiftkarpenterv1.ConditionReasonResolutionFailed,
		Message:            "Version resolution status is unknown",
	}
	if versionResolved != nil {
		notReady.Reason = versionResolved.Reason
		notReady.Message = fmt.Sprintf("Version resolution failed: %s", versionResolved.Message)
	}
	meta.SetStatusCondition(&result, notReady)
	return result
}
