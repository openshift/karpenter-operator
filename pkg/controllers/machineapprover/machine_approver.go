package machineapprover

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	certificatesv1client "k8s.io/client-go/kubernetes/typed/certificates/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	controllerName           = "karpenter-machine-approver"
	nodeBootstrapperUsername = "system:serviceaccount:openshift-machine-config-operator:node-bootstrapper"
	nodeGroup                = "system:nodes"
)

type certificateApprovalClient interface {
	UpdateApproval(context.Context, string, *certificatesv1.CertificateSigningRequest, metav1.UpdateOptions) (*certificatesv1.CertificateSigningRequest, error)
}

// MachineApproverController approves CSRs for Karpenter-provisioned nodes.
type MachineApproverController struct {
	client        client.Client
	certClient    certificateApprovalClient
	hostedCluster cluster.Cluster
	verifier      common.NodeIdentityVerifier
}

func NewMachineApproverController(hostedCluster cluster.Cluster, verifier common.NodeIdentityVerifier) *MachineApproverController {
	return &MachineApproverController{
		hostedCluster: hostedCluster,
		verifier:      verifier,
	}
}

func (r *MachineApproverController) Name() string {
	return controllerName
}

func (r *MachineApproverController) SetupWithManager(mgr ctrl.Manager) error {
	if r.hostedCluster == nil {
		return errors.New("hosted cluster is required")
	}
	if r.verifier == nil {
		return errors.New("node identity verifier is required")
	}

	certClient, err := certificatesv1client.NewForConfig(r.hostedCluster.GetConfig())
	if err != nil {
		return err
	}
	r.certClient = certClient.CertificateSigningRequests()
	r.client = r.hostedCluster.GetClient()

	c, err := controller.New(r.Name(), mgr, controller.Options{Reconciler: r})
	if err != nil {
		return fmt.Errorf("failed to construct %s controller: %w", r.Name(), err)
	}

	if err := c.Watch(source.Kind(
		r.hostedCluster.GetCache(),
		&certificatesv1.CertificateSigningRequest{},
		&handler.TypedEnqueueRequestForObject[*certificatesv1.CertificateSigningRequest]{},
		predicate.NewTypedPredicateFuncs(csrFilterFn),
	)); err != nil {
		return fmt.Errorf("failed to watch CertificateSigningRequest: %w", err)
	}

	return nil
}

func csrFilterFn(csr *certificatesv1.CertificateSigningRequest) bool {
	// only reconcile pending CSRs (not approved and not denied).
	if !isCertificateRequestPending(csr) {
		return false
	}

	switch csr.Spec.SignerName {
	case certificatesv1.KubeAPIServerClientKubeletSignerName:
		return csr.Spec.Username == nodeBootstrapperUsername
	case certificatesv1.KubeletServingSignerName:
		return sets.NewString(csr.Spec.Groups...).Has(nodeGroup)
	default:
		return false
	}
}

func (r *MachineApproverController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling CSR", "req", req)

	csr := &certificatesv1.CertificateSigningRequest{}
	if err := r.client.Get(ctx, req.NamespacedName, csr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get csr %s: %w", req.NamespacedName, err)
	}

	// Return early if deleted
	if !csr.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// If a CSR is approved/denied after being added to the queue,
	// but before we reconcile it, trying to approve it will result in an error and cause a loop.
	// Return early if the CSR has been approved/denied externally.
	if !isCertificateRequestPending(csr) {
		log.Info("CSR is already processed", "csr", csr.Name)
		return ctrl.Result{}, nil
	}

	authorized, err := r.authorize(ctx, csr)
	if err != nil {
		return ctrl.Result{}, err
	}

	if authorized {
		log.Info("Attempting to approve CSR", "csr", csr.Name)
		if err := r.approve(ctx, csr); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to approve csr %s: %w", csr.Name, err)
		}
	}

	return ctrl.Result{}, nil
}

// TODO: include a creation time window for the nodeclaim, the instance and csr triplets and also ratelimit and short circuit approval based on the number of pending CSRs
func (r *MachineApproverController) authorize(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) (bool, error) {
	switch csr.Spec.SignerName {
	case certificatesv1.KubeAPIServerClientKubeletSignerName:
		return r.authorizeClientCSR(ctx, csr)
	case certificatesv1.KubeletServingSignerName:
		return r.authorizeServingCSR(ctx, csr)
	}

	return false, fmt.Errorf("unrecognized signerName %s", csr.Spec.SignerName)
}

func (r *MachineApproverController) authorizeClientCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) (bool, error) {
	x509cr, err := parseCSR(csr.Spec.Request)
	if err != nil {
		return false, err
	}

	nodeName := strings.TrimPrefix(x509cr.Subject.CommonName, "system:node:")
	if len(nodeName) == 0 {
		return false, fmt.Errorf("subject common name does not have a valid node name")
	}

	nodeClaims, err := listNodeClaims(ctx, r.client)
	if err != nil {
		return false, err
	}

	filteredNodeClaims := slices.DeleteFunc(nodeClaims, func(claim karpenterv1.NodeClaim) bool {
		// skip if a node is already created for this nodeClaim.
		return claim.Status.NodeName != ""
	})
	if len(filteredNodeClaims) == 0 {
		return false, nil
	}

	return r.verifier.Verify(ctx, nodeName, filteredNodeClaims)
}

func (r *MachineApproverController) authorizeServingCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) (bool, error) {
	nodeName := strings.TrimPrefix(csr.Spec.Username, "system:node:")
	if len(nodeName) == 0 {
		return false, fmt.Errorf("csr username does not have a valid node name")
	}

	nodeClaim, err := findTargetNodeClaim(ctx, r.client, nodeName)
	if err != nil || nodeClaim == nil {
		return false, err
	}

	return r.verifier.Verify(ctx, nodeName, []karpenterv1.NodeClaim{*nodeClaim})
}

func (r *MachineApproverController) approve(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) error {
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:    certificatesv1.CertificateApproved,
		Reason:  "KarpenterCSRApprove",
		Message: "Auto approved by " + controllerName,
		Status:  corev1.ConditionTrue,
	})

	_, err := r.certClient.UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating approval for csr: %w", err)
	}

	return nil
}

func isCertificateRequestPending(csr *certificatesv1.CertificateSigningRequest) bool {
	for _, condition := range csr.Status.Conditions {
		if condition.Type == certificatesv1.CertificateApproved || condition.Type == certificatesv1.CertificateDenied {
			return false
		}
	}
	return true
}

func parseCSR(pemBytes []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("PEM block type must be CERTIFICATE REQUEST")
	}
	return x509.ParseCertificateRequest(block.Bytes)
}

func listNodeClaims(ctx context.Context, client client.Client) ([]karpenterv1.NodeClaim, error) {
	nodeClaimList := &karpenterv1.NodeClaimList{}
	err := client.List(ctx, nodeClaimList)
	if err != nil {
		return nil, fmt.Errorf("failed to list NodeClaims: %w", err)
	}

	return nodeClaimList.Items, nil
}

func findTargetNodeClaim(ctx context.Context, client client.Client, nodeName string) (*karpenterv1.NodeClaim, error) {
	nodeClaimList, err := listNodeClaims(ctx, client)
	if err != nil {
		return nil, err
	}

	for _, nodeClaim := range nodeClaimList {
		if nodeClaim.Status.NodeName == nodeName {
			return &nodeClaim, nil
		}
	}
	// don't return error, this CSR could belong to a Hypershift NodePool node.
	return nil, nil
}
