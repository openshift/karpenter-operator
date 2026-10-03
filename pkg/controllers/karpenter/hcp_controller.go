package karpenter

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
)

const (
	targetKubeconfigVolumeName = "target-kubeconfig"
	targetKubeconfigMountPath  = "/mnt/kubeconfig"
	targetKubeconfigFilePath   = "target-kubeconfig"
	targetKubeconfigSecretKey  = "value"

	cloudTokenVolumeName          = "cloud-token"
	cloudTokenMountPath           = "/var/run/secrets/openshift/serviceaccount" // nolint:gosec
	cloudTokenFilePath            = cloudTokenMountPath + "/token"
	targetServiceAccountNamespace = "kube-system"
	targetServiceAccountName      = "karpenter"

	karpenterDeploymentName = "karpenter"
)

type HCPControllerConfig struct {
	Namespace        string
	KarpenterImage   string
	ClusterName      string
	ClusterEndpoint  string
	CloudProvider    common.CloudProvider
	TokenMinterImage string
}

// HCPController watches HostedControlPlane objects in management cluster mode and
// deploys the karpenter operand in the same namespace as the HCP.
type HCPController struct {
	client client.Client
	config *HCPControllerConfig
}

func NewHCPController(client client.Client, cfg *HCPControllerConfig) *HCPController {
	return &HCPController{
		client: client,
		config: cfg,
	}
}

func (c *HCPController) Name() string {
	return "karpenter"
}

func (c *HCPController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling karpenter deployment on management cluster")

	hcp := &hyperv1.HostedControlPlane{}
	if err := c.client.Get(ctx, req.NamespacedName, hcp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO(maxcao13): for now we always scale up karpenter if an HCP is provisioned (meaning always)
	// In the future, we need to allow scale to zero based on the HCP AutoNode spec.
	// https://redhat.atlassian.net/browse/AUTOSCALE-520
	if hcp.Spec.AutoNode.Provisioner.Name != hyperv1.ProvisionerKarpenter {
		log.FromContext(ctx).Info("HCP does not use Karpenter provisioner, skipping")
		return ctrl.Result{}, nil
	}

	// Wait for HCP Status.ControlPlaneVersion to be set by HyperShift before proceeding.
	// This ensures the karpenter deployment will have the correct version annotation
	// for MonitorOperandsRolloutStatus tracking.
	if hcp.Status.ControlPlaneVersion.Desired.Version == "" {
		log.FromContext(ctx).Info("waiting for HCP control plane version to be set, requeuing in 5s")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	ref := hcpOwnerRef(hcp)

	if err := applyServiceAccount(ctx, c.client, c.config.Namespace, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ServiceAccount: %w", err)
	}

	cfg := &operandConfig{
		namespace:       c.config.Namespace,
		karpenterImage:  c.config.KarpenterImage,
		clusterName:     c.config.ClusterName,
		clusterEndpoint: c.config.ClusterEndpoint,
		cloudProvider:   c.config.CloudProvider,
		imagePullPolicy: corev1.PullIfNotPresent,
		logLevelArg:     "--log-level=debug", // TODO(maxcao13): make this configurable
		releaseVersion:  hcp.Status.ControlPlaneVersion.Desired.Version,
		additionalEnv: []corev1.EnvVar{
			{Name: common.KubeconfigEnvName, Value: targetKubeconfigMountPath + "/" + targetKubeconfigFilePath},
			{Name: common.DisableLeaderElectionEnvName, Value: "true"},
		},
		additionalVolumeMounts: []corev1.VolumeMount{
			{Name: targetKubeconfigVolumeName, MountPath: targetKubeconfigMountPath, ReadOnly: true},
			{Name: cloudTokenVolumeName, MountPath: cloudTokenMountPath},
		},
		additionalVolumes: []corev1.Volume{
			{
				Name: targetKubeconfigVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  hcp.Spec.InfraID + "-kubeconfig",
						DefaultMode: ptr.To(int32(0640)),
						Items: []corev1.KeyToPath{
							{Key: targetKubeconfigSecretKey, Path: targetKubeconfigFilePath},
						},
					},
				},
			},
			{
				Name:         cloudTokenVolumeName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
		},
		additionalInitContainers: []corev1.Container{tokenMinterContainer(c.config.TokenMinterImage)},
	}
	if err := applyDeployment(ctx, c.client, cfg, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Deployment: %w", err)
	}
	if err := applyPodMonitor(ctx, c.client, cfg, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile PodMonitor: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *HCPController) SetupWithManager(mgr ctrl.Manager) error {
	karpenterFilterPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == karpenterDeploymentName
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		For(&hyperv1.HostedControlPlane{}, builder.WithPredicates(hcpOperandReconcilePredicate())).
		Owns(&appsv1.Deployment{}, builder.WithPredicates(karpenterFilterPredicate)).
		Owns(&monitoringv1.PodMonitor{}, builder.WithPredicates(karpenterFilterPredicate)).
		Owns(&corev1.ServiceAccount{}, builder.WithPredicates(karpenterFilterPredicate)).
		Complete(c)
}

func hcpOperandReconcilePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(event.DeleteEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldHCP, okOld := e.ObjectOld.(*hyperv1.HostedControlPlane)
			newHCP, okNew := e.ObjectNew.(*hyperv1.HostedControlPlane)
			if !okOld || !okNew {
				return true
			}
			return hcpOperandChanged(oldHCP, newHCP)
		},
	}
}

func hcpOperandChanged(oldHCP, newHCP *hyperv1.HostedControlPlane) bool {
	if oldHCP == nil || newHCP == nil {
		return true
	}

	// Check spec changes
	specChanged := oldHCP.Spec.InfraID != newHCP.Spec.InfraID ||
		!equality.Semantic.DeepEqual(oldHCP.Spec.AutoNode, newHCP.Spec.AutoNode)

	// Check control plane version status changes (specific status field we care about)
	oldVersion := oldHCP.Status.ControlPlaneVersion.Desired.Version
	newVersion := newHCP.Status.ControlPlaneVersion.Desired.Version
	versionChanged := oldVersion != newVersion

	// Reconcile if EITHER spec OR version changed
	// (but NOT for other status changes like Ready condition, kubeconfig, etc.)
	return specChanged || versionChanged
}

func hcpOwnerRef(hcp *hyperv1.HostedControlPlane) *metaac.OwnerReferenceApplyConfiguration {
	return metaac.OwnerReference().
		WithAPIVersion(hyperv1.GroupVersion.String()).
		WithKind("HostedControlPlane").
		WithName(hcp.Name).
		WithUID(hcp.UID).
		WithBlockOwnerDeletion(true).
		WithController(true)
}

// tokenMinterContainer defines the tokenMinterContainer which runs as a sidecar in the karpenter operand.
//
// token-minter mints ServiceAccount tokens for a ServiceAccount in the hosted (tenant) cluster.
//
// token-minter will create a hostedcluster-side service account (in the namespace and name we configure),
// and periodically requests signed JWTs from the hostedcluster kube-api with audience "openshift", and write them to disk to cloudTokenFilePath.
// The Karpenter container will then read this token from disk (since they share the same pod), and exchange it with
// the cloud provider's workload identity provider for a temporary access token to authorize against the cloud provider API for
// regular Karpenter operations.
//
// most values are adapted from the controlplane-component definition in openshift/hypershift
// https://github.com/openshift/hypershift/blob/754facc01438f2707b8cb5f8726a70e1fd6d9c92/support/controlplane-component/token-minter-container.go#L134-L152
func tokenMinterContainer(image string) corev1.Container {
	return corev1.Container{
		Name:    "token-minter",
		Image:   image,
		Command: []string{"/usr/bin/control-plane-operator", "token-minter"},
		Args: []string{
			"--service-account-namespace=" + targetServiceAccountNamespace,
			"--service-account-name=" + targetServiceAccountName,
			"--token-file=" + cloudTokenFilePath,
			"--kubeconfig=" + targetKubeconfigMountPath + "/" + targetKubeconfigFilePath,
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("30Mi"),
			},
		},
		RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"test", "-f", cloudTokenFilePath},
				},
			},
			PeriodSeconds:    1,
			FailureThreshold: 30,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: targetKubeconfigVolumeName, MountPath: targetKubeconfigMountPath},
			{Name: cloudTokenVolumeName, MountPath: cloudTokenMountPath},
		},
	}
}
