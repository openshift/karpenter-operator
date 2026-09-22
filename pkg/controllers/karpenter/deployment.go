package karpenter

import (
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsac "k8s.io/client-go/applyconfigurations/apps/v1"
	coreac "k8s.io/client-go/applyconfigurations/core/v1"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"

	"github.com/samber/lo"
)

// buildDeployment constructs the karpenter Deployment apply configuration.
func buildDeployment(cfg *operandConfig, ownerRef *metaac.OwnerReferenceApplyConfiguration) (*appsac.DeploymentApplyConfiguration, error) {
	selectorLabels := map[string]string{appLabelKey: karpenterName}
	podLabels := lo.Assign(selectorLabels, cfg.additionalLabels)

	podSpec, err := buildPodSpec(cfg)
	if err != nil {
		return nil, err
	}

	// Deployment labels - add managed-by label for MonitorOperandsRolloutStatus
	deploymentLabels := map[string]string{
		appLabelKey:                          karpenterName,
		"hypershift.openshift.io/managed-by": "karpenter-operator",
	}

	// Deployment annotations - add version annotation for MonitorOperandsRolloutStatus
	deploymentAnnotations := map[string]string{}
	if cfg.releaseVersion != "" {
		deploymentAnnotations["release.openshift.io/version"] = cfg.releaseVersion
	}

	deployment := appsac.Deployment(karpenterName, cfg.namespace).
		WithOwnerReferences(ownerRef).
		WithLabels(deploymentLabels).
		WithSpec(appsac.DeploymentSpec().
			WithReplicas(1).
			WithSelector(metaac.LabelSelector().WithMatchLabels(selectorLabels)).
			WithTemplate(coreac.PodTemplateSpec().
				WithAnnotations(map[string]string{
					targetWorkloadManagementAnnotation: targetWorkloadSchedulingPriority,
					"openshift.io/required-scc":        "restricted-v2", // TODO: import key from openshift/api
				}).
				WithLabels(podLabels).
				WithSpec(podSpec),
			),
		)

	// Add version annotation to deployment if present
	if len(deploymentAnnotations) > 0 {
		deployment = deployment.WithAnnotations(deploymentAnnotations)
	}

	return deployment, nil
}

// buildPodSpec constructs the karpenter operand pod spec.
func buildPodSpec(cfg *operandConfig) (*coreac.PodSpecApplyConfiguration, error) {
	cloudCfg := cfg.cloudProvider.OperandConfig()

	env, err := buildEnv(cfg, cloudCfg)
	if err != nil {
		return nil, err
	}

	cloudMounts, err := volumeMounts(cloudCfg.VolumeMounts)
	if err != nil {
		return nil, err
	}
	additionalMounts, err := volumeMounts(cfg.additionalVolumeMounts)
	if err != nil {
		return nil, err
	}
	mounts := append(cloudMounts, additionalMounts...)

	cloudVols, err := volumes(cloudCfg.Volumes)
	if err != nil {
		return nil, err
	}
	additionalVols, err := volumes(cfg.additionalVolumes)
	if err != nil {
		return nil, err
	}
	vols := append(cloudVols, additionalVols...)

	additionalEnv, err := envVars(cfg.additionalEnv)
	if err != nil {
		return nil, err
	}
	env = append(env, additionalEnv...)

	inits, err := initContainers(cfg.additionalInitContainers)
	if err != nil {
		return nil, err
	}

	return coreac.PodSpec().
		WithPriorityClassName(karpenterPodPriorityClassName).
		WithServiceAccountName(karpenterName).
		WithTerminationGracePeriodSeconds(karpenterPodTerminationGracePeriodSeconds).
		WithSecurityContext(coreac.PodSecurityContext().
			WithRunAsNonRoot(true).
			WithSeccompProfile(coreac.SeccompProfile().
				WithType(corev1.SeccompProfileTypeRuntimeDefault)),
		).
		WithInitContainers(inits...).
		WithContainers(
			coreac.Container().
				WithName(karpenterName).
				WithImage(cfg.karpenterImage).
				WithImagePullPolicy(cfg.imagePullPolicy).
				WithArgs(cfg.logLevelArg).
				WithEnv(env...).
				WithPorts(karpenterPorts()...).
				WithResources(coreac.ResourceRequirements().
					WithRequests(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					}),
				).
				WithSecurityContext(coreac.SecurityContext().
					WithAllowPrivilegeEscalation(false).
					WithCapabilities(coreac.Capabilities().WithDrop(corev1.Capability("ALL"))),
				).
				WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError).
				WithLivenessProbe(karpenterLivenessProbe()).
				WithReadinessProbe(karpenterReadinessProbe()).
				WithVolumeMounts(mounts...),
		).
		WithVolumes(vols...), nil
}

func buildEnv(cfg *operandConfig, cloudCfg common.OperandCloudConfig) ([]*coreac.EnvVarApplyConfiguration, error) {
	env := []*coreac.EnvVarApplyConfiguration{
		coreac.EnvVar().WithName(common.SystemNamespaceEnvName).
			WithValueFrom(coreac.EnvVarSource().
				WithFieldRef(coreac.ObjectFieldSelector().WithFieldPath("metadata.namespace")),
			),
		coreac.EnvVar().WithName(common.ClusterNameEnvName).WithValue(cfg.clusterName),
		coreac.EnvVar().WithName(common.ClusterEndpointEnvName).WithValue(cfg.clusterEndpoint),
		coreac.EnvVar().WithName(common.DisableWebhookEnvName).WithValue("true"),
		coreac.EnvVar().WithName(common.HealthProbePortEnvName).WithValue(defaultHealthProbePortStr),
	}
	cloudEnv, err := envVars(cloudCfg.Env)
	if err != nil {
		return nil, err
	}
	return append(env, cloudEnv...), nil
}

func karpenterPorts() []*coreac.ContainerPortApplyConfiguration {
	return []*coreac.ContainerPortApplyConfiguration{
		coreac.ContainerPort().WithName(metricsPortName).WithContainerPort(defaultMetricsPort),
		coreac.ContainerPort().WithName(healthProbePortName).WithContainerPort(defaultHealthProbePort).WithProtocol(corev1.ProtocolTCP),
	}
}

func karpenterLivenessProbe() *coreac.ProbeApplyConfiguration {
	return coreac.Probe().
		WithHTTPGet(coreac.HTTPGetAction().WithPath("/healthz").WithPort(intstr.FromInt32(defaultHealthProbePort))).
		WithInitialDelaySeconds(30).
		WithTimeoutSeconds(30)
}

func karpenterReadinessProbe() *coreac.ProbeApplyConfiguration {
	return coreac.Probe().
		WithHTTPGet(coreac.HTTPGetAction().WithPath("/readyz").WithPort(intstr.FromInt32(defaultHealthProbePort))).
		WithInitialDelaySeconds(5).
		WithTimeoutSeconds(30)
}
