package ec2nodeclass

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	nodeclassstatus "github.com/openshift/karpenter-operator/pkg/controllers/nodeclass/status"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

const finalizer = "hypershift.openshift.io/ec2-nodeclass-finalizer"

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
		// Watch user data secrets in the management cluster and reconcile the ec2nodeclass they belong to
		Watches(&corev1.Secret{},
			hypershift.EnqueueNodeClassForUserDataSecret(),
			builder.WithPredicates(hypershift.KarpenterSecretPredicate())).
		// Watch HostedControlPlane for instance profile, resource tag and control plane version changes
		Watches(&hyperv1beta1.HostedControlPlane{},
			handler.EnqueueRequestsFromMapFunc(r.mapToOpenShiftEC2NodeClasses),
			builder.WithPredicates(r.hcpPredicate())).
		Complete(r)
}

func (r *EC2NodeClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { //nolint:gocyclo
	log := ctrl.LoggerFrom(ctx)

	hcp, err := hypershift.GetHostedControlPlane(ctx, r.managementClient, r.namespace)
	if err != nil {
		if errors.Is(err, hypershift.ErrHostedControlPlaneNotFound) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if hcp.Annotations[openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	openshiftEC2NodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	if err := r.hostedClient.Get(ctx, req.NamespacedName, openshiftEC2NodeClass); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("OpenshiftEC2NodeClass not found, aborting reconcile")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting openshift ec2 nodeclass: %w", err)
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

		if controllerutil.ContainsFinalizer(openshiftEC2NodeClass, finalizer) {
			original := openshiftEC2NodeClass.DeepCopy()
			controllerutil.RemoveFinalizer(openshiftEC2NodeClass, finalizer)
			if err := r.hostedClient.Patch(ctx, openshiftEC2NodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer from openshift ec2 nodeclass: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(openshiftEC2NodeClass, finalizer) {
		original := openshiftEC2NodeClass.DeepCopy()
		controllerutil.AddFinalizer(openshiftEC2NodeClass, finalizer)
		if err := r.hostedClient.Patch(ctx, openshiftEC2NodeClass, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer to openshift ec2 nodeclass: %w", err)
		}
	}

	userDataSecret, err := hypershift.GetUserDataSecret(ctx, r.managementClient, r.namespace, openshiftEC2NodeClass.Name)
	if err != nil {
		if errors.Is(err, hypershift.ErrUserDataSecretNotFound) {
			// Don't treat this as an error
			// ec2nodeclass controller might have been faster than karpenterignition controller to generate the user data secret
			log.V(1).Info("User data secret not found, requeueing", "err", err)
			return ctrl.Result{RequeueAfter: time.Second * 1}, nil
		}
		return ctrl.Result{}, err
	}

	ec2NodeClass, err = r.reconcileEC2NodeClass(ctx, openshiftEC2NodeClass, hcp, userDataSecret)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileStatus(ctx, ec2NodeClass, openshiftEC2NodeClass, hcp); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// deleteIfNeeded deletes the object if it exists and returns whether it still exists.
func deleteIfNeeded(ctx context.Context, c client.Client, o client.Object) (exists bool, err error) {
	if err := c.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting %T: %w", o, err)
	}
	if o.GetDeletionTimestamp() != nil {
		return true, nil
	}
	if err := c.Delete(ctx, o); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("deleting %T: %w", o, err)
	}
	return true, nil
}

// reconcileEC2NodeClass creates or updates the EC2NodeClass of the OpenshiftEC2NodeClass with the user data and AMIs
// produced by the ignition pipeline.
func (r *EC2NodeClassReconciler) reconcileEC2NodeClass(ctx context.Context, nodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1beta1.HostedControlPlane, userDataSecret *corev1.Secret) (*awskarpenterv1.EC2NodeClass, error) {
	ec2NodeClass := &awskarpenterv1.EC2NodeClass{}
	ec2NodeClass.Name = nodeClass.Name
	op, err := controllerutil.CreateOrUpdate(ctx, r.hostedClient, ec2NodeClass, func() error {
		current := ec2NodeClass.Spec
		ec2NodeClass.Spec = ec2NodeClassSpec(ctx, nodeClass, hcp)
		if err := reconcileEC2NodeClassRelease(ctx, &ec2NodeClass.Spec, current, nodeClass, hcp, userDataSecret); err != nil {
			return err
		}
		return controllerutil.SetControllerReference(nodeClass, ec2NodeClass, r.hostedClient.Scheme())
	})
	if err != nil {
		return nil, fmt.Errorf("reconciling ec2 nodeclass: %w", err)
	}
	// Updates are not logged: the API server defaults fields the desired spec leaves unset,
	// so CreateOrUpdate reports an update on every reconcile.
	if op == controllerutil.OperationResultCreated {
		ctrl.LoggerFrom(ctx).Info("Created EC2NodeClass", "ec2nodeclass", ec2NodeClass.Name)
	}
	return ec2NodeClass, nil
}

func (r *EC2NodeClassReconciler) reconcileStatus(ctx context.Context, ec2NodeClass *awskarpenterv1.EC2NodeClass, openshiftNodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1beta1.HostedControlPlane) error {
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

	openshiftNodeClass.Status.Conditions = nodeclassstatus.SyncConditions(openshiftNodeClass.Status.Conditions, ec2NodeClass.Status.Conditions, openshiftNodeClass.Generation)

	setAWSResourceTagConflictCondition(openshiftNodeClass, hcp)

	if !reflect.DeepEqual(originalObj.Status, openshiftNodeClass.Status) {
		if err := r.hostedClient.Status().Patch(ctx, openshiftNodeClass, client.MergeFromWithOptions(originalObj, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("updating status: %w", err)
		}
		log.V(1).Info("Updated OpenshiftEC2NodeClass status")
	}

	return nil
}

// hcpPredicate filters HostedControlPlane events to changes that affect EC2NodeClasses.
func (r *EC2NodeClassReconciler) hcpPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldHCP, oldOK := e.ObjectOld.(*hyperv1beta1.HostedControlPlane)
			newHCP, newOK := e.ObjectNew.(*hyperv1beta1.HostedControlPlane)
			if oldOK && newOK {
				oldVal := oldHCP.Annotations[hyperv1beta1.AWSKarpenterDefaultInstanceProfile]
				newVal := newHCP.Annotations[hyperv1beta1.AWSKarpenterDefaultInstanceProfile]
				return oldVal != newVal ||
					!equality.Semantic.DeepEqual(awsResourceTags(oldHCP), awsResourceTags(newHCP)) ||
					hypershift.ControlPlaneVersionChanged(oldHCP, newHCP)
			}
			return false
		},
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
	}
}

func awsResourceTags(hcp *hyperv1beta1.HostedControlPlane) []hyperv1beta1.AWSClusterResourceTag {
	if hcp.Spec.Platform.AWS == nil {
		return nil
	}
	return hcp.Spec.Platform.AWS.ResourceTags
}

// mapToOpenShiftEC2NodeClasses maps a request to all OpenshiftEC2NodeClass resources
func (r *EC2NodeClassReconciler) mapToOpenShiftEC2NodeClasses(ctx context.Context, obj client.Object) []reconcile.Request {
	openshiftEC2NodeClassList := &openshiftkarpenterv1.OpenshiftEC2NodeClassList{}
	if err := r.hostedClient.List(ctx, openshiftEC2NodeClassList); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Could not list OpenShiftEC2NodeClass")
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
func mergeEC2NodeClassTags(ctx context.Context, openshiftEC2NodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1beta1.HostedControlPlane) map[string]string {
	log := ctrl.LoggerFrom(ctx)
	tags := make(map[string]string)

	allowOverride := make(map[string]bool)
	// First add platform tags and track which allow override
	if hcp.Spec.Platform.AWS != nil {
		for _, tag := range hcp.Spec.Platform.AWS.ResourceTags {
			tags[tag.Key] = tag.Value
			if tag.OverridePolicy == hyperv1beta1.AWSResourceTagOverridePolicyAllow {
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
		log.V(1).Info("Filtered restricted Karpenter tags", "removed count", len(removedTags))
	}

	// If we were nil coming in, we should be nil going out, test case comparisons care, {} is
	// not the same as nil
	if openshiftEC2NodeClass.Spec.Tags == nil && len(filteredTags) == 0 {
		return nil
	}

	return filteredTags
}

func setAWSResourceTagConflictCondition(openshiftNodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass, hcp *hyperv1beta1.HostedControlPlane) { //nolint:gocyclo
	if hcp.Spec.Platform.AWS == nil || len(hcp.Spec.Platform.AWS.ResourceTags) == 0 || len(openshiftNodeClass.Spec.Tags) == 0 {
		meta.RemoveStatusCondition(&openshiftNodeClass.Status.Conditions, hyperv1beta1.NodePoolAWSResourceTagConflictConditionType)
		return
	}

	var blocked, overridden int
	for k, ncVal := range openshiftNodeClass.Spec.Tags {
		var found bool
		var hcTag hyperv1beta1.AWSClusterResourceTag
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
		if hcTag.OverridePolicy == hyperv1beta1.AWSResourceTagOverridePolicyAllow {
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
			Type:               hyperv1beta1.NodePoolAWSResourceTagConflictConditionType,
			Status:             metav1.ConditionFalse,
			Reason:             hyperv1beta1.AWSResourceTagNoConflictReason,
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
		Type:               hyperv1beta1.NodePoolAWSResourceTagConflictConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             hyperv1beta1.AWSResourceTagConflictDetectedReason,
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
