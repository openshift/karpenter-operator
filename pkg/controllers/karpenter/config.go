package karpenter

import (
	"strconv"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	corev1 "k8s.io/api/core/v1"
)

const (
	karpenterName = "karpenter"
	fieldManager  = "karpenter-operator"
)

const (
	defaultMetricsPort     = 8080
	defaultHealthProbePort = 8081

	metricsPortName     = "metrics"
	healthProbePortName = "http"

	karpenterPodTerminationGracePeriodSeconds = 10
	karpenterPodPriorityClassName             = "system-node-critical"

	appLabelKey = "app"

	targetWorkloadManagementAnnotation = "target.workload.openshift.io/management"
	targetWorkloadSchedulingPriority   = "{\"effect\": \"PreferredDuringScheduling\"}"
)

var defaultHealthProbePortStr = strconv.Itoa(defaultHealthProbePort)

// operandConfig holds all parameters needed to build the karpenter operand resources.
// OCPController and HCPController populate this from their respective input objects.
type operandConfig struct {
	namespace                string
	karpenterImage           string
	clusterName              string
	clusterEndpoint          string
	cloudProvider            common.CloudProvider
	imagePullPolicy          corev1.PullPolicy
	logLevelArg              string
	releaseVersion           string
	additionalLabels         map[string]string
	additionalEnv            []corev1.EnvVar
	additionalVolumeMounts   []corev1.VolumeMount
	additionalVolumes        []corev1.Volume
	additionalInitContainers []corev1.Container
}
