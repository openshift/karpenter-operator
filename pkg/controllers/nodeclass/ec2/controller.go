package ec2nodeclass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterapis "github.com/aws/karpenter-provider-aws/pkg/apis"
	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	finalizer = "hypershift.openshift.io/ec2-nodeclass-finalizer"

	// defaultRootVolumeSize is 120Gi because HCP NodePools provisioned with HCP CLI are set with 120Gi root volume by default.
	// https://github.com/openshift/hypershift/blob/8be1d9c6f8f79106444e48f2b7d0069b942ba0d7/cmd/nodepool/aws/create.go#L30
	defaultRootVolumeSize = "120Gi"
)

// karpenterSubnetsConfigMapName is the ConfigMap in the HCP namespace that lists the subnet IDs
// resolved by all OpenshiftEC2NodeClasses. The HyperShift operator adds them to the VPC endpoint subnets.
const karpenterSubnetsConfigMapName = "karpenter-subnets"

// EC2NodeClassReconciler reconciles OpenshiftEC2NodeClasses in the hosted cluster into EC2NodeClasses.
// HostedControlPlanes and user data Secrets are read from the management cluster.
type EC2NodeClassReconciler struct {
	namespace     string
	hostedCluster cluster.Cluster

	managementClient client.Client
	hostedClient     client.Client
}

func NewEC2NodeClassReconciler(hostedCluster cluster.Cluster, namespace string) *EC2NodeClassReconciler {
	return &EC2NodeClassReconciler{
		namespace:     namespace,
		hostedCluster: hostedCluster,
	}
}

func (r *EC2NodeClassReconciler) Name() string {
	return "ec2-nodeclass"
}

func (r *EC2NodeClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.hostedCluster == nil {
		return errors.New("hosted cluster is required")
	}
	r.managementClient = mgr.GetClient()
	r.hostedClient = r.hostedCluster.GetClient()
	hostedCache := r.hostedCluster.GetCache()

	return ctrl.NewControllerManagedBy(mgr).
		Named(r.Name()).
		WithOptions(controller.Options{
			RateLimiter:             workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1*time.Second, 10*time.Second),
			MaxConcurrentReconciles: 10,
		}).
		WatchesRawSource(source.Kind[client.Object](hostedCache, &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			&handler.EnqueueRequestForObject{})).
		WatchesRawSource(source.Kind[client.Object](hostedCache, &awskarpenterv1.EC2NodeClass{},
			&handler.EnqueueRequestForObject{})).
		WatchesRawSource(source.Kind[client.Object](hostedCache, &admissionv1.ValidatingAdmissionPolicy{},
			handler.EnqueueRequestsFromMapFunc(r.mapVAPToOpenShiftEC2NodeClasses))).
		WatchesRawSource(source.Kind[client.Object](hostedCache, &admissionv1.ValidatingAdmissionPolicyBinding{},
			handler.EnqueueRequestsFromMapFunc(r.mapVAPBindingToOpenShiftEC2NodeClasses))).
		// Watch secrets in the management cluster and reconcile all ec2nodeclasses
		Watches(&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.mapToOpenShiftEC2NodeClasses),
			builder.WithPredicates(r.karpenterSecretPredicate())).
		// Watch HostedControlPlane for instance profile and resource tag changes
		Watches(&hyperv1.HostedControlPlane{},
			handler.EnqueueRequestsFromMapFunc(r.mapToOpenShiftEC2NodeClasses),
			builder.WithPredicates(r.hcpPredicate())).
		Complete(r)
}

func (r *EC2NodeClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { //nolint:gocyclo
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling", "req", req)

	hcp, err := hypershift.GetHostedControlPlane(ctx, r.managementClient, r.namespace)
	if err != nil {
		if errors.Is(err, hypershift.ErrHostedControlPlaneNotFound) {
			log.Info("HostedControlPlane not found, requeueing")
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
		return ctrl.Result{}, err
	}

	if hcp.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	openshiftEC2NodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	if err := r.hostedClient.Get(ctx, req.NamespacedName, openshiftEC2NodeClass); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("openshiftEC2NodeClass not found, aborting reconcile", "name", req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get openshiftEC2NodeClass %q: %w", req.NamespacedName, err)
	}

	ec2NodeClass := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:      openshiftEC2NodeClass.Name,
			Namespace: openshiftEC2NodeClass.Namespace,
		},
	}

	if !openshiftEC2NodeClass.DeletionTimestamp.IsZero() {
		exists, err := deleteIfNeeded(ctx, r.hostedClient, ec2NodeClass)
		if err != nil {
			return ctrl.Result{}, err
		}
		if exists {
			// wait until EC2NodeClass is deleted
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}

		// Update ConfigMap to remove this OpenshiftEC2NodeClass's subnets.
		// This handles the case where other OpenshiftEC2NodeClass resources still exist.
		if err := r.reconcileKarpenterSubnetsConfigMap(ctx, hcp); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to reconcile karpenter subnets configmap during deletion: %w", err)
		}

		if controllerutil.ContainsFinalizer(openshiftEC2NodeClass, finalizer) {
			original := openshiftEC2NodeClass.DeepCopy()
			controllerutil.RemoveFinalizer(openshiftEC2NodeClass, finalizer)
			if err := r.hostedClient.Patch(ctx, openshiftEC2NodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer from openshiftEC2NodeClass: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(openshiftEC2NodeClass, finalizer) {
		original := openshiftEC2NodeClass.DeepCopy()
		controllerutil.AddFinalizer(openshiftEC2NodeClass, finalizer)
		if err := r.hostedClient.Patch(ctx, openshiftEC2NodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer to openshiftEC2NodeClass: %w", err)
		}
	}

	userDataSecret, err := r.getUserDataSecret(ctx, openshiftEC2NodeClass)
	if err != nil {
		if errors.Is(err, errKarpenterUserDataSecretNotFound) {
			// Don't treat this as an error
			// ec2nodeclass controller might have been faster than karpenterignition controller to generate the user data secret
			log.Info(err.Error())
			return ctrl.Result{RequeueAfter: time.Second * 1}, nil
		}
		return ctrl.Result{}, err
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, r.hostedClient, ec2NodeClass, func() error {
		if err := controllerutil.SetControllerReference(openshiftEC2NodeClass, ec2NodeClass, r.hostedClient.Scheme()); err != nil {
			return err
		}
		return reconcileEC2NodeClass(ctx, ec2NodeClass, openshiftEC2NodeClass, hcp, userDataSecret)
	}); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileStatus(ctx, ec2NodeClass, openshiftEC2NodeClass, hcp); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileKarpenterSubnetsConfigMap(ctx, hcp); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile karpenter subnets configmap: %w", err)
	}

	if err := r.reconcileVAP(ctx); err != nil {
		return ctrl.Result{}, err
	}

	// We requeue an un-pinned NodeClass if the control plane is in the middle of an upgrade to eventually ensure the NodeClass is upgraded to the new release image.
	if openshiftEC2NodeClass.Spec.Version == "" && isControlPlaneUpgrading(hcp) {
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	}
	return ctrl.Result{}, nil
}

// deleteIfNeeded deletes the object if it exists and returns whether it still exists.
func deleteIfNeeded(ctx context.Context, c client.Client, o client.Object) (exists bool, err error) {
	if err := c.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, fmt.Errorf("error getting %T: %w", o, err)
	}
	if o.GetDeletionTimestamp() != nil {
		return true, nil
	}
	if err := c.Delete(ctx, o); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("error deleting %T: %w", o, err)
	}
	return true, nil
}

// isControlPlaneUpgrading returns true when the desired release image differs
// from the most recent Completed version in history.
// Returns false during initial install (no Completed entry or desired not yet populated).
// The loop returns on the first CompletedUpdate entry found. This is safe because the
// ControlPlaneVersion.History list is ordered newest-first per the API contract.
func isControlPlaneUpgrading(hcp *hyperv1.HostedControlPlane) bool {
	desiredImage := hcp.Status.ControlPlaneVersion.Desired.Image
	if desiredImage == "" {
		return false
	}

	for _, entry := range hcp.Status.ControlPlaneVersion.History {
		if entry.State == configv1.CompletedUpdate {
			return entry.Image != desiredImage
		}
	}

	return false
}

func reconcileEC2NodeClass(ctx context.Context, ec2NodeClass *awskarpenterv1.EC2NodeClass, openshiftEC2NodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1.HostedControlPlane, userDataSecret *corev1.Secret) error { //nolint:gocyclo
	pauseUpgrade := isControlPlaneUpgrading(hcp)

	var amiSelectorTerms []awskarpenterv1.AMISelectorTerm

	userData := new(string(userDataSecret.Data["value"]))

	// When upgrade is in progress, and the nodeclass is tied to the control plane version, and the EC2NodeClass already has AMI/UserData set
	// (i.e. not the first creation), preserve the existing drift-triggering fields that cause a node rollout upgrade.
	if pauseUpgrade && openshiftEC2NodeClass.Spec.Version == "" && len(ec2NodeClass.Spec.AMISelectorTerms) > 0 && ec2NodeClass.Spec.UserData != nil {
		ctrl.LoggerFrom(ctx).Info("Control plane upgrade in progress, preserving existing userData and amis")
		userData = ec2NodeClass.Spec.UserData
		amiSelectorTerms = ec2NodeClass.Spec.AMISelectorTerms
	} else {
		var err error
		amiSelectorTerms, err = amiSelectorTermsFromUserDataSecret(userDataSecret)
		if err != nil {
			return fmt.Errorf("failed to get AMISelectorTerms: %w", err)
		}
	}

	ec2NodeClass.Spec = awskarpenterv1.EC2NodeClassSpec{
		UserData:                         userData,
		AMIFamily:                        new("Custom"),
		AMISelectorTerms:                 amiSelectorTerms,
		AssociatePublicIPAddress:         karpenterAssociatePublicIPAddressFromNodeClassSpec(openshiftEC2NodeClass.Spec),
		Tags:                             mergeEC2NodeClassTags(ctx, openshiftEC2NodeClass, hcp),
		DetailedMonitoring:               karpenterDetailedMonitoringFromNodeClassSpec(openshiftEC2NodeClass.Spec),
		BlockDeviceMappings:              karpenterBlockDeviceMappingFromNodeClassSpec(openshiftEC2NodeClass.Spec),
		InstanceStorePolicy:              karpenterInstanceStorePolicyFromNodeClassSpec(openshiftEC2NodeClass.Spec),
		MetadataOptions:                  karpenterMetadataOptionsFromNodeClassSpec(openshiftEC2NodeClass.Spec),
		CapacityReservationSelectorTerms: karpenterCapacityReservationSelectorTermsFromNodeClassSpec(openshiftEC2NodeClass.Spec),
		Kubelet:                          karpenterKubeletConfigurationFromNodeClassSpec(openshiftEC2NodeClass.Spec),
	}

	// Set instance profile from HostedCluster annotation (platform-controlled)
	if instanceProfile, ok := hcp.Annotations[hyperv1.AWSKarpenterDefaultInstanceProfile]; ok && instanceProfile != "" {
		ec2NodeClass.Spec.InstanceProfile = new(instanceProfile)
	}

	// Set default BlockDeviceMappings if not specified in OpenshiftEC2NodeClass.
	if ec2NodeClass.Spec.BlockDeviceMappings == nil {
		ec2NodeClass.Spec.BlockDeviceMappings = []*awskarpenterv1.BlockDeviceMapping{
			{
				DeviceName: new("/dev/xvda"),
				EBS: &awskarpenterv1.BlockDevice{
					VolumeSize: new(resource.MustParse(defaultRootVolumeSize)),
					VolumeType: new("gp3"),
					Encrypted:  new(true),
				},
			},
		}
	}

	var subnetSelectorTerms []awskarpenterv1.SubnetSelectorTerm
	if openshiftEC2NodeClass.Spec.SubnetSelectorTerms != nil {
		for _, term := range openshiftEC2NodeClass.Spec.SubnetSelectorTerms {
			subnetSelectorTerms = append(subnetSelectorTerms, awskarpenterv1.SubnetSelectorTerm{
				Tags: term.Tags,
				ID:   term.ID,
			})
		}
	} else {
		subnetSelectorTerms = []awskarpenterv1.SubnetSelectorTerm{
			{
				Tags: DefaultSubnetSelectorTags(hcp.Spec.InfraID),
			},
		}
	}
	ec2NodeClass.Spec.SubnetSelectorTerms = subnetSelectorTerms

	var securityGroupSelectorTerms []awskarpenterv1.SecurityGroupSelectorTerm
	if openshiftEC2NodeClass.Spec.SecurityGroupSelectorTerms != nil {
		for _, term := range openshiftEC2NodeClass.Spec.SecurityGroupSelectorTerms {
			securityGroupSelectorTerms = append(securityGroupSelectorTerms, awskarpenterv1.SecurityGroupSelectorTerm{
				Tags: term.Tags,
				ID:   term.ID,
				Name: term.Name,
			})
		}
	} else {
		securityGroupSelectorTerms = []awskarpenterv1.SecurityGroupSelectorTerm{
			{
				Tags: DefaultSecurityGroupSelectorTags(hcp.Spec.InfraID),
			},
		}
	}
	ec2NodeClass.Spec.SecurityGroupSelectorTerms = securityGroupSelectorTerms

	return nil
}

// DefaultSubnetSelectorTags returns the tags selecting the private subnets of the hosted cluster.
func DefaultSubnetSelectorTags(infraID string) map[string]string {
	return map[string]string{
		"kubernetes.io/role/internal-elb":                "1",
		fmt.Sprintf("kubernetes.io/cluster/%s", infraID): "*",
	}
}

// DefaultSecurityGroupSelectorTags returns the tags selecting the default security group of the hosted cluster.
func DefaultSecurityGroupSelectorTags(infraID string) map[string]string {
	return map[string]string{
		"karpenter.sh/discovery": infraID,
	}
}

func (r *EC2NodeClassReconciler) reconcileStatus(ctx context.Context, ec2NodeClass *awskarpenterv1.EC2NodeClass, openshiftNodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1.HostedControlPlane) error {
	log := ctrl.LoggerFrom(ctx)

	originalObj := openshiftNodeClass.DeepCopy()

	// Update only the fields this controller manages, leaving fields owned by the
	// ignition controller (ReleaseImage, Version, VersionResolved and SupportedVersionSkew conditions) untouched.
	securityGroups := make([]openshiftkarpenterv1.SecurityGroup, 0, len(ec2NodeClass.Status.SecurityGroups))
	for _, sg := range ec2NodeClass.Status.SecurityGroups {
		securityGroups = append(securityGroups, openshiftkarpenterv1.SecurityGroup{
			ID:   sg.ID,
			Name: sg.Name,
		})
	}
	openshiftNodeClass.Status.SecurityGroups = securityGroups

	subnets := make([]openshiftkarpenterv1.Subnet, 0, len(ec2NodeClass.Status.Subnets))
	for _, s := range ec2NodeClass.Status.Subnets {
		subnets = append(subnets, openshiftkarpenterv1.Subnet{
			ID:     s.ID,
			Zone:   s.Zone,
			ZoneID: s.ZoneID,
		})
	}
	openshiftNodeClass.Status.Subnets = subnets

	// Sync CapacityReservations from upstream EC2NodeClass.
	// Upstream karpenter uses lowercase enum values (open, targeted, default, capacity-block,
	// active, expiring) while our API uses PascalCase (Open, Targeted, Default, CapacityBlock,
	// Active, Expiring), so we convert here.
	openshiftNodeClass.Status.CapacityReservations = nil
	for _, cr := range ec2NodeClass.Status.CapacityReservations {
		resolved := openshiftkarpenterv1.CapacityReservation{
			AvailabilityZone:      cr.AvailabilityZone,
			ID:                    cr.ID,
			InstanceMatchCriteria: upstreamInstanceMatchCriteria(cr.InstanceMatchCriteria),
			InstanceType:          cr.InstanceType,
			OwnerID:               cr.OwnerID,
			ReservationType:       upstreamReservationType(cr.ReservationType),
			State:                 upstreamReservationState(cr.State),
		}
		if cr.EndTime != nil {
			resolved.EndTime = *cr.EndTime
		}
		openshiftNodeClass.Status.CapacityReservations = append(openshiftNodeClass.Status.CapacityReservations, resolved)
	}

	// Sync conditions from the upstream EC2NodeClass. Use SetStatusCondition so that
	// conditions managed by the ignition controller are preserved.
	for _, condition := range ec2NodeClass.Status.Conditions {
		meta.SetStatusCondition(&openshiftNodeClass.Status.Conditions, metav1.Condition(condition))
	}

	// Compute the top-level Ready condition atomically by combining EC2 readiness
	// with version resolution status. This ensures a single controller owns the
	// Ready semantic rather than having multiple controllers race to set it.
	r.computeReadyCondition(openshiftNodeClass)

	setAWSResourceTagConflictCondition(openshiftNodeClass, hcp)

	if !reflect.DeepEqual(originalObj.Status, openshiftNodeClass.Status) {
		if err := r.hostedClient.Status().Patch(ctx, openshiftNodeClass, client.MergeFromWithOptions(originalObj, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("failed to update status: %w", err)
		}
	}

	log.Info("Reconciled OpenshiftEC2NodeClass status")
	return nil
}

// computeReadyCondition computes the top-level Ready condition by combining
// the upstream EC2NodeClass Ready status with the VersionResolved condition.
// If VersionResolved is False, Ready is set to False regardless of EC2 readiness.
func (r *EC2NodeClassReconciler) computeReadyCondition(openshiftNodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass) {
	versionResolved := meta.FindStatusCondition(openshiftNodeClass.Status.Conditions, openshiftkarpenterv1.ConditionTypeVersionResolved)
	if versionResolved == nil || versionResolved.Status != metav1.ConditionTrue {
		reason := openshiftkarpenterv1.ConditionReasonResolutionFailed
		message := "Version resolution status is unknown"
		if versionResolved != nil {
			reason = versionResolved.Reason
			message = fmt.Sprintf("Version resolution failed: %s", versionResolved.Message)
		}
		meta.SetStatusCondition(&openshiftNodeClass.Status.Conditions, metav1.Condition{
			Type:               openshiftkarpenterv1.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: openshiftNodeClass.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             reason,
			Message:            message,
		})
	}
}

func (r *EC2NodeClassReconciler) reconcileKarpenterSubnetsConfigMap(ctx context.Context, hcp *hyperv1.HostedControlPlane) error { //nolint:gocyclo
	log := ctrl.LoggerFrom(ctx)

	// List all OpenshiftEC2NodeClass resources in guest cluster
	openshiftEC2NodeClassList := &openshiftkarpenterv1.OpenshiftEC2NodeClassList{}
	if err := r.hostedClient.List(ctx, openshiftEC2NodeClassList); err != nil {
		return fmt.Errorf("failed to list OpenshiftEC2NodeClass: %w", err)
	}

	subnetIDSet := sets.NewString()
	for _, nodeClass := range openshiftEC2NodeClassList.Items {
		// Skip NodeClasses that are being deleted — their subnets should no
		// longer be propagated to VPC endpoints.
		if !nodeClass.DeletionTimestamp.IsZero() {
			continue
		}
		for _, subnet := range nodeClass.Status.Subnets {
			if subnet.ID != "" {
				subnetIDSet.Insert(subnet.ID)
			}
		}
	}

	subnetIDs := subnetIDSet.List() // Sorted list

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      karpenterSubnetsConfigMapName,
			Namespace: r.namespace,
		},
	}

	// If there are no OpenshiftEC2NodeClass resources with resolved subnets,
	// delete the ConfigMap (no NodeClasses exist, or none have subnets in status yet).
	// The ConfigMap is also cleaned up automatically via owner reference when HCP is deleted.
	if subnetIDSet.Len() == 0 {
		if _, err := deleteIfNeeded(ctx, r.managementClient, configMap); err != nil {
			return fmt.Errorf("failed to delete karpenter subnets configmap: %w", err)
		}
		log.Info("Deleted karpenter subnets configmap (no OpenshiftEC2NodeClass resources with resolved subnets)")
		return nil
	}

	// Create or update ConfigMap in management cluster

	_, err := controllerutil.CreateOrUpdate(ctx, r.managementClient, configMap, func() error {
		// Set owner reference to HostedControlPlane for automatic cleanup
		if err := controllerutil.SetControllerReference(hcp, configMap, r.managementClient.Scheme()); err != nil {
			return err
		}

		if configMap.Labels == nil {
			configMap.Labels = make(map[string]string)
		}
		configMap.Labels["hypershift.openshift.io/managed-by"] = "karpenter"
		configMap.Labels["hypershift.openshift.io/infra-id"] = hcp.Spec.InfraID

		if configMap.Data == nil {
			configMap.Data = make(map[string]string)
		}

		// Store as JSON array
		subnetIDsJSON, err := json.Marshal(subnetIDs)
		if err != nil {
			return fmt.Errorf("failed to marshal subnet IDs: %w", err)
		}
		configMap.Data["subnetIDs"] = string(subnetIDsJSON)

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile karpenter subnets configmap: %w", err)
	}

	log.Info("Reconciled karpenter subnets configmap", "subnetCount", len(subnetIDs))
	return nil
}

func (r *EC2NodeClassReconciler) reconcileVAP(ctx context.Context) error {
	vap := &admissionv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "karpenter.ec2nodeclass.hypershift.io",
		},
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, r.hostedClient, vap, func() error {
		vap.Spec.MatchConstraints = &admissionv1.MatchResources{
			ResourceRules: []admissionv1.NamedRuleWithOperations{
				{
					RuleWithOperations: admissionv1.RuleWithOperations{
						Operations: []admissionv1.OperationType{
							admissionv1.OperationAll,
						},
						Rule: admissionv1.Rule{
							APIGroups:   []string{awskarpenterapis.Group},
							APIVersions: []string{"v1"},
							Resources:   []string{"ec2nodeclasses"},
						},
					},
				},
			},
		}
		vap.Spec.MatchConditions = []admissionv1.MatchCondition{
			{
				Name:       "exclude-hcco-user",
				Expression: "'system:hosted-cluster-config' != request.userInfo.username",
			},
		}

		vap.Spec.Validations = []admissionv1.Validation{
			{
				Expression: "has(oldObject.spec) && has(object.spec) && object.spec == oldObject.spec",
				Message:    "EC2NodeClass resource can't be created/updated/deleted directly, please use OpenshiftEC2NodeClass resource instead",
			},
		}

		return nil
	}); err != nil {
		return err
	}

	vapBinding := &admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "karpenter-binding.ec2nodeclass.hypershift.io",
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.hostedClient, vapBinding, func() error {
		vapBinding.Spec.PolicyName = vap.Name
		vapBinding.Spec.ValidationActions = []admissionv1.ValidationAction{admissionv1.Deny}
		return nil
	})

	return err
}

// karpenterSecretPredicate only returns true on creates/updates of Secrets matching karpenterSecretSelector.
func (r *EC2NodeClassReconciler) karpenterSecretPredicate() predicate.Predicate {
	isKarpenterSecret := func(obj client.Object) bool {
		return karpenterSecretSelector.Matches(labels.Set(obj.GetLabels()))
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isKarpenterSecret(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isKarpenterSecret(e.ObjectNew) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
}

// hcpPredicate filters HostedControlPlane events to changes that affect EC2NodeClasses.
func (r *EC2NodeClassReconciler) hcpPredicate() predicate.Predicate {
	filterHCP := func(obj client.Object) bool {
		if hcp, ok := obj.(*hyperv1.HostedControlPlane); ok {
			// Trigger if the annotation exists
			if _, exists := hcp.Annotations[hyperv1.AWSKarpenterDefaultInstanceProfile]; exists {
				return true
			}
		}
		return false
	}

	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return filterHCP(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldHCP, oldOK := e.ObjectOld.(*hyperv1.HostedControlPlane)
			newHCP, newOK := e.ObjectNew.(*hyperv1.HostedControlPlane)
			if oldOK && newOK {
				oldVal := oldHCP.Annotations[hyperv1.AWSKarpenterDefaultInstanceProfile]
				newVal := newHCP.Annotations[hyperv1.AWSKarpenterDefaultInstanceProfile]
				return oldVal != newVal || !equality.Semantic.DeepEqual(awsResourceTags(oldHCP), awsResourceTags(newHCP))
			}
			return false
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
}

func awsResourceTags(hcp *hyperv1.HostedControlPlane) []hyperv1.AWSClusterResourceTag {
	if hcp.Spec.Platform.AWS == nil {
		return nil
	}
	return hcp.Spec.Platform.AWS.ResourceTags
}

func (r *EC2NodeClassReconciler) mapVAPToOpenShiftEC2NodeClasses(ctx context.Context, o client.Object) []ctrl.Request {
	if o.GetName() != "karpenter.ec2nodeclass.hypershift.io" {
		return nil
	}
	return r.mapToOpenShiftEC2NodeClasses(ctx, o)
}

func (r *EC2NodeClassReconciler) mapVAPBindingToOpenShiftEC2NodeClasses(ctx context.Context, o client.Object) []ctrl.Request {
	if o.GetName() != "karpenter-binding.ec2nodeclass.hypershift.io" {
		return nil
	}
	return r.mapToOpenShiftEC2NodeClasses(ctx, o)
}

// mapToOpenShiftEC2NodeClasses maps a request to all OpenshiftEC2NodeClass resources
func (r *EC2NodeClassReconciler) mapToOpenShiftEC2NodeClasses(ctx context.Context, obj client.Object) []reconcile.Request {
	openshiftEC2NodeClassList := &openshiftkarpenterv1.OpenshiftEC2NodeClassList{}
	if err := r.hostedClient.List(ctx, openshiftEC2NodeClassList); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "failed to list OpenShiftEC2NodeClass")
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, nodeClass := range openshiftEC2NodeClassList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKey{
				Name:      nodeClass.Name,
				Namespace: nodeClass.Namespace,
			},
		})
	}

	return requests
}

// mergeEC2NodeClassTags merges platform tags from HostedControlPlane with OpenshiftEC2NodeClass tags.
// By default, platform tags take precedence over nodeclass tags.
// Platform tags with overridePolicy "Allow" permit nodeclass tags to override them.
// Tags matching Karpenter's restricted patterns are filtered out to prevent validation errors.
// Karpenter restricts the patterns because it manages those tags itself, so the result is not "the karpenter-managed tags won't be present",
// the result is "the tags will still be present and managed by Karpenter"
func mergeEC2NodeClassTags(ctx context.Context, openshiftEC2NodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1.HostedControlPlane) map[string]string {
	log := ctrl.LoggerFrom(ctx)
	tags := make(map[string]string)

	allowOverride := make(map[string]bool)
	// First add platform tags and track which allow override
	if hcp.Spec.Platform.AWS != nil {
		for _, tag := range hcp.Spec.Platform.AWS.ResourceTags {
			tags[tag.Key] = tag.Value
			if tag.OverridePolicy == hyperv1.AWSResourceTagOverridePolicyAllow {
				allowOverride[tag.Key] = true
			}
		}
	}

	// Then add nodeclass tags, only overriding platform tags that explicitly allow it
	for k, v := range openshiftEC2NodeClass.Spec.Tags {
		if _, isHCTag := tags[k]; isHCTag && !allowOverride[k] {
			continue
		}
		tags[k] = v
	}

	// Filter out restricted tags that Karpenter manages automatically
	filteredTags, removedTags := filterRestrictedTags(tags)

	if len(removedTags) > 0 {
		log.V(4).Info("Filtered restricted Karpenter tags", "removedCount", len(removedTags))
	}

	// If we were nil coming in, we should be nil going out, test case comparisons care, {} is
	// not the same as nil
	if openshiftEC2NodeClass.Spec.Tags == nil && len(filteredTags) == 0 {
		return nil
	}

	return filteredTags
}

func setAWSResourceTagConflictCondition(openshiftNodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1.HostedControlPlane) { //nolint:gocyclo
	if hcp.Spec.Platform.AWS == nil || len(hcp.Spec.Platform.AWS.ResourceTags) == 0 || len(openshiftNodeClass.Spec.Tags) == 0 {
		meta.RemoveStatusCondition(&openshiftNodeClass.Status.Conditions, hyperv1.NodePoolAWSResourceTagConflictConditionType)
		return
	}

	var blocked, overridden int
	for k, ncVal := range openshiftNodeClass.Spec.Tags {
		var found bool
		var hcTag hyperv1.AWSClusterResourceTag
		for _, t := range hcp.Spec.Platform.AWS.ResourceTags {
			if t.Key == k {
				found = true
				hcTag = t
				break
			}
		}
		if !found || hcTag.Value == ncVal {
			continue
		}
		if hcTag.OverridePolicy == hyperv1.AWSResourceTagOverridePolicyAllow {
			overridden++
		} else {
			blocked++
		}
	}

	if blocked == 0 {
		msg := "No AWS resource tag conflicts detected"
		if overridden > 0 {
			msg = fmt.Sprintf("%d AWS resource tag override(s) applied; nodeclass values used (allowed by HostedCluster)", overridden)
		}
		meta.SetStatusCondition(&openshiftNodeClass.Status.Conditions, metav1.Condition{
			Type:               hyperv1.NodePoolAWSResourceTagConflictConditionType,
			Status:             metav1.ConditionFalse,
			Reason:             hyperv1.AWSResourceTagNoConflictReason,
			Message:            msg,
			ObservedGeneration: openshiftNodeClass.Generation,
		})
		return
	}

	msg := fmt.Sprintf("%d AWS resource tag conflict(s) detected; HostedCluster values preserved (override not allowed)", blocked)
	if overridden > 0 {
		msg += fmt.Sprintf("; %d override(s) applied (allowed by HostedCluster)", overridden)
	}
	meta.SetStatusCondition(&openshiftNodeClass.Status.Conditions, metav1.Condition{
		Type:               hyperv1.NodePoolAWSResourceTagConflictConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             hyperv1.AWSResourceTagConflictDetectedReason,
		Message:            msg,
		ObservedGeneration: openshiftNodeClass.Generation,
	})
}

// filterRestrictedTags removes tags that match Karpenter's restricted tag patterns.
// Karpenter manages certain tags automatically and prohibits users from setting them.
// Returns a new map with restricted tags filtered out and a slice of removed tag keys.
func filterRestrictedTags(tags map[string]string) (map[string]string, []string) {
	if len(tags) == 0 {
		return tags, nil
	}

	filteredTags := make(map[string]string)
	removedTags := []string{}

	for key, value := range tags {
		isRestricted := false
		for _, pattern := range awskarpenterv1.RestrictedTagPatterns {
			if pattern.MatchString(key) {
				isRestricted = true
				removedTags = append(removedTags, key)
				break
			}
		}
		if !isRestricted {
			filteredTags[key] = value
		}
	}

	return filteredTags, removedTags
}

// upstreamInstanceMatchCriteria converts upstream karpenter's lowercase instanceMatchCriteria
// values (open, targeted) to our PascalCase API values (Open, Targeted).
func upstreamInstanceMatchCriteria(value string) openshiftkarpenterv1.InstanceMatchCriteria {
	return openshiftkarpenterv1.InstanceMatchCriteria(strings.ToUpper(value[:1]) + value[1:])
}

// upstreamReservationType converts upstream karpenter's CapacityReservationType
// values (default, capacity-block) to our PascalCase API values (Default, CapacityBlock).
func upstreamReservationType(value awskarpenterv1.CapacityReservationType) openshiftkarpenterv1.CapacityReservationType {
	switch value {
	case awskarpenterv1.CapacityReservationTypeCapacityBlock:
		return openshiftkarpenterv1.CapacityReservationTypeCapacityBlock
	default:
		return openshiftkarpenterv1.CapacityReservationTypeDefault
	}
}

// upstreamReservationState converts upstream karpenter's CapacityReservationState
// values (active, expiring) to our PascalCase API values (Active, Expiring).
func upstreamReservationState(value awskarpenterv1.CapacityReservationState) openshiftkarpenterv1.CapacityReservationState {
	switch value {
	case awskarpenterv1.CapacityReservationStateExpiring:
		return openshiftkarpenterv1.CapacityReservationStateExpiring
	default:
		return openshiftkarpenterv1.CapacityReservationStateActive
	}
}
