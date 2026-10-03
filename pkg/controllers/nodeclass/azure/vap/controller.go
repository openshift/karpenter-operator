package vap

import (
	"context"
	"fmt"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/client-go/util/workqueue"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

const (
	vapName        = "karpenter.aksnodeclass.hypershift.io"
	vapBindingName = "karpenter-binding.aksnodeclass.hypershift.io"
)

// Controller keeps the ValidatingAdmissionPolicy and its binding in the hosted cluster that
// stop users from changing AKSNodeClasses directly. Only the operator, which writes as the
// hosted cluster config user, may change them.
type Controller struct {
	hostedClient client.Client
	hostedCache  cache.Cache
}

func NewController(hostedCluster cluster.Cluster) *Controller {
	return &Controller{
		hostedClient: hostedCluster.GetClient(),
		hostedCache:  hostedCluster.GetCache(),
	}
}

func (c *Controller) Name() string {
	return "azure-nodeclass-vap"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	// All events map to a single work item, because the controller always applies both objects.
	enqueue := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{}}
	})
	isManaged := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == vapName || o.GetName() == vapBindingName
	})

	// Trigger an initial reconcile, because the watches only fire once the objects exist.
	initialSync := source.Func(func(_ context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		q.Add(reconcile.Request{})
		return nil
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		WatchesRawSource(source.Kind[client.Object](c.hostedCache, &admissionv1.ValidatingAdmissionPolicy{}, enqueue, isManaged)).
		WatchesRawSource(source.Kind[client.Object](c.hostedCache, &admissionv1.ValidatingAdmissionPolicyBinding{}, enqueue, isManaged)).
		WatchesRawSource(initialSync).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	policy.Name = vapName
	if _, err := controllerutil.CreateOrUpdate(ctx, c.hostedClient, policy, func() error {
		policy.Spec = vapSpec()
		return nil
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ValidatingAdmissionPolicy %s: %w", vapName, err)
	}

	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	binding.Name = vapBindingName
	if _, err := controllerutil.CreateOrUpdate(ctx, c.hostedClient, binding, func() error {
		binding.Spec = admissionv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        vapName,
			ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
		}
		return nil
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ValidatingAdmissionPolicyBinding %s: %w", vapBindingName, err)
	}
	return ctrl.Result{}, nil
}

// vapSpec denies any change to the spec of an AKSNodeClass, including creation and deletion,
// in every served API version.
func vapSpec() admissionv1.ValidatingAdmissionPolicySpec {
	return admissionv1.ValidatingAdmissionPolicySpec{
		MatchConstraints: &admissionv1.MatchResources{
			ResourceRules: []admissionv1.NamedRuleWithOperations{{
				RuleWithOperations: admissionv1.RuleWithOperations{
					Operations: []admissionv1.OperationType{admissionv1.OperationAll},
					Rule: admissionv1.Rule{
						APIGroups:   []string{azurekarpenterv1beta1.Group},
						APIVersions: []string{"*"},
						Resources:   []string{"aksnodeclasses"},
					},
				},
			}},
		},
		MatchConditions: []admissionv1.MatchCondition{{
			Name:       "exclude-hcco-user",
			Expression: "'system:hosted-cluster-config' != request.userInfo.username",
		}},
		Validations: []admissionv1.Validation{{
			Expression: "has(oldObject.spec) && has(object.spec) && object.spec == oldObject.spec",
			Message:    "AKSNodeClass resource can't be created/updated/deleted directly, please use OpenShiftAzureNodeClass resource instead",
		}},
	}
}
