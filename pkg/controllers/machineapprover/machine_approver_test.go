package machineapprover

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	certificatesv1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestCSRFilterFn(t *testing.T) {
	tests := map[string]struct {
		csr  *certificatesv1.CertificateSigningRequest
		want bool
	}{
		"When pending client CSR comes from node bootstrapper, it should be selected": {
			csr: &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: certificatesv1.KubeAPIServerClientKubeletSignerName,
				Username:   nodeBootstrapperUsername,
			}},
			want: true,
		},
		"When pending client CSR comes from another user, it should be ignored": {
			csr: &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: certificatesv1.KubeAPIServerClientKubeletSignerName,
				Username:   "system:node:worker-0",
			}},
		},
		"When pending serving CSR has system nodes group, it should be selected": {
			csr: &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: certificatesv1.KubeletServingSignerName,
				Groups:     []string{nodeGroup},
			}},
			want: true,
		},
		"When pending serving CSR lacks system nodes group, it should be ignored": {
			csr: &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: certificatesv1.KubeletServingSignerName,
				Groups:     []string{"system:authenticated"},
			}},
		},
		"When CSR uses unsupported signer, it should be ignored": {
			csr: &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: "example.com/unknown",
			}},
		},
		"When CSR is already approved, it should be ignored": {
			csr: &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: certificatesv1.KubeAPIServerClientKubeletSignerName,
				Username:   nodeBootstrapperUsername,
			}, Status: certificatesv1.CertificateSigningRequestStatus{
				Conditions: []certificatesv1.CertificateSigningRequestCondition{{Type: certificatesv1.CertificateApproved}},
			}},
		},
		"When CSR is already denied, it should be ignored": {
			csr: &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				SignerName: certificatesv1.KubeAPIServerClientKubeletSignerName,
				Username:   nodeBootstrapperUsername,
			}, Status: certificatesv1.CertificateSigningRequestStatus{
				Conditions: []certificatesv1.CertificateSigningRequestCondition{{Type: certificatesv1.CertificateDenied}},
			}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := csrFilterFn(tc.csr); got != tc.want {
				t.Errorf("csrFilterFn() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAuthorizeClientCSR(t *testing.T) {
	tests := map[string]struct {
		x509csr                 []byte
		objects                 []client.Object
		authorize               bool
		wantAuthorize           bool
		wantErr                 string
		wantVerifiedProviderIDs []string
	}{
		"When CSR request is invalid, it should return an error": {
			x509csr: []byte("-----BEGIN??\n"),
			wantErr: "PEM block type must be CERTIFICATE REQUEST",
		},
		"When CSR common name has no node name, it should return an error": {
			x509csr: createCSR("system:node:"),
			wantErr: "subject common name does not have a valid node name",
		},
		"When no NodeClaims exist, it should not call the verifier": {
			x509csr:       createCSR("system:node:test1"),
			authorize:     true,
			wantAuthorize: false,
		},
		"When an unbound NodeClaim exists, it should delegate node authorization": {
			x509csr:                 createCSR("system:node:test1"),
			objects:                 []client.Object{nodeClaim("aws:///instance-1", "")},
			authorize:               true,
			wantAuthorize:           true,
			wantVerifiedProviderIDs: []string{"aws:///instance-1"},
		},
		"When bound NodeClaims exist, it should exclude them from bootstrap authorization": {
			x509csr: createCSR("system:node:test1"),
			objects: []client.Object{
				nodeClaim("aws:///instance-1", ""),
				nodeClaim("aws:///instance-2", "worker-2"),
			},
			authorize:               true,
			wantAuthorize:           true,
			wantVerifiedProviderIDs: []string{"aws:///instance-1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			verifier := &fakeNodeIdentityVerifier{authorized: tc.authorize}
			r := &MachineApproverController{
				client:   fake.NewClientBuilder().WithScheme(scheme()).WithObjects(tc.objects...).Build(),
				verifier: verifier,
			}
			csr := &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				Request:    tc.x509csr,
				SignerName: certificatesv1.KubeAPIServerClientKubeletSignerName,
			}}

			authorized, err := r.authorizeClientCSR(t.Context(), csr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("authorizeClientCSR() error = %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorizeClientCSR() error = %v", err)
			}
			if authorized != tc.wantAuthorize {
				t.Errorf("authorizeClientCSR() = %t, want %t", authorized, tc.wantAuthorize)
			}
			if len(tc.objects) == 0 && verifier.calls != 0 {
				t.Errorf("verifier called %d times, want 0", verifier.calls)
			}
			if tc.wantVerifiedProviderIDs != nil && !slices.Equal(verifiedProviderIDs(verifier.nodeClaims), tc.wantVerifiedProviderIDs) {
				t.Errorf("verifier called with NodeClaims %v, want ProviderIDs %v", verifiedProviderIDs(verifier.nodeClaims), tc.wantVerifiedProviderIDs)
			}
		})
	}
}

func TestAuthorizeServingCSR(t *testing.T) {
	tests := map[string]struct {
		username      string
		objects       []client.Object
		authorize     bool
		wantAuthorize bool
		wantErr       string
	}{
		"When CSR username has no node name, it should return an error": {
			username: "system:node:",
			wantErr:  "csr username does not have a valid node name",
		},
		"When no NodeClaim matches the node name, it should not call the verifier": {
			username: "system:node:test1",
			objects:  []client.Object{nodeClaim("aws:///instance-1", "test2")},
		},
		"When a matching NodeClaim exists, it should delegate node authorization": {
			username:      "system:node:test1",
			objects:       []client.Object{nodeClaim("aws:///instance-1", "test1")},
			authorize:     true,
			wantAuthorize: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			verifier := &fakeNodeIdentityVerifier{authorized: tc.authorize}
			r := &MachineApproverController{
				client:   fake.NewClientBuilder().WithScheme(scheme()).WithObjects(tc.objects...).Build(),
				verifier: verifier,
			}
			csr := &certificatesv1.CertificateSigningRequest{Spec: certificatesv1.CertificateSigningRequestSpec{
				Username:   tc.username,
				SignerName: certificatesv1.KubeletServingSignerName,
			}}

			authorized, err := r.authorizeServingCSR(t.Context(), csr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("authorizeServingCSR() error = %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorizeServingCSR() error = %v", err)
			}
			if authorized != tc.wantAuthorize {
				t.Errorf("authorizeServingCSR() = %t, want %t", authorized, tc.wantAuthorize)
			}
			if len(tc.objects) == 0 || tc.objects[0].(*karpenterv1.NodeClaim).Status.NodeName != "test1" {
				if verifier.calls != 0 {
					t.Errorf("verifier called %d times, want 0", verifier.calls)
				}
			}
		})
	}
}

func TestReconcileApprovesAuthorizedCSR(t *testing.T) {
	csr := newTestCSR("csr")
	nodeClaim := nodeClaim("aws:///instance-1", "")
	controllerClient := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(csr, nodeClaim).Build()
	certificateClient := &fakeCertificateApprovalClient{}

	r := &MachineApproverController{
		client:     controllerClient,
		certClient: certificateClient,
		verifier:   &fakeNodeIdentityVerifier{authorized: true},
	}

	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(csr)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if certificateClient.calls != 1 || certificateClient.name != csr.Name {
		t.Fatalf("UpdateApproval called %d times for %q, want once for %q", certificateClient.calls, certificateClient.name, csr.Name)
	}
	if certificateClient.approved == nil {
		t.Fatal("CSR approval was not requested")
	}
	if len(certificateClient.approved.Status.Conditions) != 1 {
		t.Fatalf("CSR conditions = %v, want one approved condition", certificateClient.approved.Status.Conditions)
	}
	condition := certificateClient.approved.Status.Conditions[0]
	if condition.Type != certificatesv1.CertificateApproved || condition.Reason != "KarpenterCSRApprove" {
		t.Fatalf("CSR condition = %v, want approved condition with KarpenterCSRApprove reason", condition)
	}
}

func TestReconcileReturnsVerifierError(t *testing.T) {
	csr := newTestCSR("csr")
	nodeClaim := nodeClaim("aws:///instance-1", "")
	r := &MachineApproverController{
		client: fake.NewClientBuilder().WithScheme(scheme()).WithObjects(csr, nodeClaim).Build(),
		verifier: &fakeNodeIdentityVerifier{
			err: errors.New("authorization failed"),
		},
	}

	if _, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(csr)}); err == nil || !strings.Contains(err.Error(), "authorization failed") {
		t.Fatalf("Reconcile() error = %v, want authorization failed", err)
	}
}

func verifiedProviderIDs(nodeClaims []karpenterv1.NodeClaim) []string {
	ids := make([]string, len(nodeClaims))
	for i, claim := range nodeClaims {
		ids[i] = claim.Status.ProviderID
	}
	return ids
}

func nodeClaim(providerID, nodeName string) *karpenterv1.NodeClaim {
	return &karpenterv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "nodeclaim-" + strings.ReplaceAll(providerID, "/", "-")},
		Status: karpenterv1.NodeClaimStatus{
			ProviderID: providerID,
			NodeName:   nodeName,
		},
	}
}

type fakeNodeIdentityVerifier struct {
	authorized bool
	err        error
	calls      int
	nodeName   string
	nodeClaims []karpenterv1.NodeClaim
}

func (f *fakeNodeIdentityVerifier) Verify(_ context.Context, nodeName string, nodeClaims []karpenterv1.NodeClaim) (bool, error) {
	f.calls++
	f.nodeName = nodeName
	f.nodeClaims = nodeClaims
	return f.authorized, f.err
}

type fakeCertificateApprovalClient struct {
	approved *certificatesv1.CertificateSigningRequest
	name     string
	calls    int
}

func (f *fakeCertificateApprovalClient) UpdateApproval(_ context.Context, name string, csr *certificatesv1.CertificateSigningRequest, _ metav1.UpdateOptions) (*certificatesv1.CertificateSigningRequest, error) {
	f.calls++
	f.name = name
	f.approved = csr.DeepCopy()
	return f.approved, nil
}

func newTestCSR(name string) *certificatesv1.CertificateSigningRequest {
	return &certificatesv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: certificatesv1.CertificateSigningRequestSpec{
			Request:    createCSR("system:node:test1"),
			SignerName: certificatesv1.KubeAPIServerClientKubeletSignerName,
			Username:   nodeBootstrapperUsername,
		},
	}
}

func scheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "karpenter.sh",
		Version: "v1",
		Kind:    "NodeClaim",
	}, &karpenterv1.NodeClaim{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "karpenter.sh",
		Version: "v1",
		Kind:    "NodeClaimList",
	}, &karpenterv1.NodeClaimList{})
	return scheme
}

func createCSR(commonName string) []byte {
	keyBytes, _ := rsa.GenerateKey(rand.Reader, 2048)
	template := x509.CertificateRequest{
		Subject:            pkix.Name{Organization: []string{"system:nodes"}, CommonName: commonName},
		SignatureAlgorithm: x509.SHA256WithRSA,
		IPAddresses:        []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:           []string{"node1", "node1.local"},
	}
	csrBytes, _ := x509.CreateCertificateRequest(rand.Reader, &template, keyBytes)
	var csrOut bytes.Buffer
	_ = pem.Encode(&csrOut, &pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes})
	return csrOut.Bytes()
}
