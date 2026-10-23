package ec2nodeclass

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"
	"github.com/awslabs/operatorpkg/status"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const testInfraID = "test-infra"

func TestReconcile(t *testing.T) {
	const (
		hcpNamespace  = "clusters-example"
		nodeClassName = "default"
		amd64AMI      = "ami-0f3c7d07486cad139"
		arm64AMI      = "ami-0a9b8c7d6e5f40312"
		userData      = `{"ignition":{"version":"3.2.0"}}`
	)

	newHCP := func(annotations map[string]string) *hyperv1beta1.HostedControlPlane {
		return &hyperv1beta1.HostedControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "example",
				Namespace:   hcpNamespace,
				UID:         "4c8f0a4e-4f8e-4a57-9c1f-3b8ad2f2c8a1",
				Annotations: annotations,
			},
			Spec: hyperv1beta1.HostedControlPlaneSpec{
				InfraID: "example-4x7kq",
				Platform: hyperv1beta1.PlatformSpec{
					Type: hyperv1beta1.AWSPlatform,
				},
			},
		}
	}
	userDataSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "user-data-default-karpenter-a1b2c3d4",
			Namespace: hcpNamespace,
			Labels: map[string]string{
				openshiftkarpenterv1.ManagedByKarpenterLabel:      "true",
				archToAMILabelKey(hyperv1beta1.ArchitectureAMD64): amd64AMI,
				archToAMILabelKey(hyperv1beta1.ArchitectureARM64): arm64AMI,
			},
			Annotations: map[string]string{
				openshiftkarpenterv1.TokenSecretNodePoolAnnotation: hcpNamespace + "/default-karpenter",
			},
		},
		Data: map[string][]byte{
			"value": []byte(userData),
		},
	}
	newOpenshiftEC2NodeClass := func(deleting bool) *openshiftkarpenterv1.OpenshiftEC2NodeClass {
		nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeClassName,
				UID:  "9d1f5b3a-6c2e-4d7f-8a1b-2c3d4e5f6a7b",
			},
		}
		if deleting {
			nodeClass.Finalizers = []string{finalizer}
			nodeClass.DeletionTimestamp = new(metav1.Now())
		}
		return nodeClass
	}
	existingEC2NodeClass := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClassName,
		},
	}

	tests := map[string]struct {
		managementObjects []client.Object
		hostedObjects     []client.Object
		expectedResult    ctrl.Result
		expectedHosted    func(g Gomega, hostedClient client.Client)
	}{
		"When no HostedControlPlane exists, it should not create the EC2NodeClass": {
			hostedObjects:  []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult: ctrl.Result{},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
		"When the HostedControlPlane has the Karpenter core e2e override annotation, it should skip reconciliation": {
			managementObjects: []client.Object{
				newHCP(map[string]string{openshiftkarpenterv1.KarpenterCoreE2EOverrideAnnotation: "true"}),
				userDataSecret,
			},
			hostedObjects:  []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult: ctrl.Result{},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(BeEmpty())
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
		"When the user data Secret does not exist yet, it should add the finalizer and requeue": {
			managementObjects: []client.Object{newHCP(nil)},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult:    ctrl.Result{RequeueAfter: time.Second},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(ConsistOf(finalizer))
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
		"When the user data Secret exists, it should create the EC2NodeClass with user data and AMIs": {
			managementObjects: []client.Object{newHCP(nil), userDataSecret},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(false)},
			expectedResult:    ctrl.Result{},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(ConsistOf(finalizer))

				ec2NodeClass := &awskarpenterv1.EC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, ec2NodeClass)).To(Succeed())
				g.Expect(ec2NodeClass.Spec.UserData).To(Equal(ptr.To(userData)))
				g.Expect(ec2NodeClass.Spec.AMISelectorTerms).To(Equal([]awskarpenterv1.AMISelectorTerm{{ID: amd64AMI}, {ID: arm64AMI}}))
				g.Expect(ec2NodeClass.OwnerReferences).To(ConsistOf(metav1.OwnerReference{
					APIVersion:         openshiftkarpenterv1.SchemeGroupVersion.String(),
					Kind:               "OpenshiftEC2NodeClass",
					Name:               nodeClassName,
					UID:                nodeClass.UID,
					Controller:         new(true),
					BlockOwnerDeletion: new(true),
				}))
			},
		},
		"When the OpenshiftEC2NodeClass is being deleted and the EC2NodeClass exists, it should delete the EC2NodeClass and requeue": {
			managementObjects: []client.Object{newHCP(nil), userDataSecret},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(true), existingEC2NodeClass},
			expectedResult:    ctrl.Result{RequeueAfter: 5 * time.Second},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &awskarpenterv1.EC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
				nodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
				g.Expect(hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, nodeClass)).To(Succeed())
				g.Expect(nodeClass.Finalizers).To(ConsistOf(finalizer))
			},
		},
		"When the OpenshiftEC2NodeClass is being deleted and the EC2NodeClass is gone, it should remove the finalizer": {
			managementObjects: []client.Object{newHCP(nil), userDataSecret},
			hostedObjects:     []client.Object{newOpenshiftEC2NodeClass(true)},
			expectedResult:    ctrl.Result{},
			expectedHosted: func(g Gomega, hostedClient client.Client) {
				err := hostedClient.Get(t.Context(), client.ObjectKey{Name: nodeClassName}, &openshiftkarpenterv1.OpenshiftEC2NodeClass{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			managementClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.managementObjects...).
				Build()
			hostedClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.hostedObjects...).
				WithStatusSubresource(&openshiftkarpenterv1.OpenshiftEC2NodeClass{}).
				Build()

			r := &EC2NodeClassReconciler{
				namespace:        hcpNamespace,
				managementClient: managementClient,
				hostedClient:     hostedClient,
			}

			result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Name: nodeClassName}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(tc.expectedResult))
			tc.expectedHosted(g, hostedClient)
		})
	}
}

func TestReconcileEC2NodeClassUpgradePause(t *testing.T) {
	oldUserData := new("old-userdata-hash-abc")
	oldAMI := []awskarpenterv1.AMISelectorTerm{{ID: "ami-old"}}
	newUserData := "new-userdata-hash-xyz"
	newAMI := "ami-new"

	userDataSecret := &corev1.Secret{
		Data: map[string][]byte{
			"value": []byte(newUserData),
		},
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				openshiftkarpenterv1.UserDataAMILabel: newAMI,
			},
		},
	}

	upgradingHCP := &hyperv1beta1.HostedControlPlane{
		Spec: hyperv1beta1.HostedControlPlaneSpec{
			InfraID: testInfraID,
			Platform: hyperv1beta1.PlatformSpec{
				Type: hyperv1beta1.AWSPlatform,
			},
		},
		Status: hyperv1beta1.HostedControlPlaneStatus{
			ControlPlaneVersion: hyperv1beta1.ControlPlaneVersionStatus{
				Desired: configv1.Release{Image: "quay.io/release:4.18.0"},
				History: []hyperv1beta1.ControlPlaneUpdateHistory{
					{State: configv1.PartialUpdate, Image: "quay.io/release:4.18.0"},
					{State: configv1.CompletedUpdate, Image: "quay.io/release:4.17.0"},
				},
			},
		},
	}

	stableHCP := &hyperv1beta1.HostedControlPlane{
		Spec: hyperv1beta1.HostedControlPlaneSpec{
			InfraID: testInfraID,
			Platform: hyperv1beta1.PlatformSpec{
				Type: hyperv1beta1.AWSPlatform,
			},
		},
		Status: hyperv1beta1.HostedControlPlaneStatus{
			ControlPlaneVersion: hyperv1beta1.ControlPlaneVersionStatus{
				Desired: configv1.Release{Image: "quay.io/release:4.17.0"},
				History: []hyperv1beta1.ControlPlaneUpdateHistory{
					{State: configv1.CompletedUpdate, Image: "quay.io/release:4.17.0"},
				},
			},
		},
	}

	tests := map[string]struct {
		hcp                   *hyperv1beta1.HostedControlPlane
		ec2NodeClass          *awskarpenterv1.EC2NodeClass
		openshiftEC2NodeClass *openshiftkarpenterv1.OpenshiftEC2NodeClass
		userDataSecret        *corev1.Secret
		expectedUserData      *string
		expectedAMIs          []awskarpenterv1.AMISelectorTerm
	}{
		"When CP is upgrading and NodeClass is unpinned with existing values, it should preserve old userData and AMIs": {
			hcp: upgradingHCP,
			ec2NodeClass: &awskarpenterv1.EC2NodeClass{
				Spec: awskarpenterv1.EC2NodeClassSpec{
					UserData:         oldUserData,
					AMISelectorTerms: oldAMI,
				},
			},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			expectedUserData:      oldUserData,
			expectedAMIs:          oldAMI,
		},
		"When the control plane is upgrading and the new Secret has no AMIs, it should preserve the current release without an error": {
			hcp: upgradingHCP,
			ec2NodeClass: &awskarpenterv1.EC2NodeClass{
				Spec: awskarpenterv1.EC2NodeClassSpec{
					UserData:         oldUserData,
					AMISelectorTerms: oldAMI,
				},
			},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			userDataSecret: &corev1.Secret{
				Data: map[string][]byte{"value": []byte(newUserData)},
			},
			expectedUserData: oldUserData,
			expectedAMIs:     oldAMI,
		},
		"When CP is upgrading but NodeClass is pinned, it should apply new values": {
			hcp: upgradingHCP,
			ec2NodeClass: &awskarpenterv1.EC2NodeClass{
				Spec: awskarpenterv1.EC2NodeClassSpec{
					UserData:         oldUserData,
					AMISelectorTerms: oldAMI,
				},
			},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				Spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
					Version: "4.18.0",
				},
			},
			expectedUserData: new(newUserData),
			expectedAMIs:     []awskarpenterv1.AMISelectorTerm{{ID: newAMI}},
		},
		"When CP is upgrading and NodeClass is unpinned but has no existing values (first creation), it should still apply new values": {
			hcp:                   upgradingHCP,
			ec2NodeClass:          &awskarpenterv1.EC2NodeClass{},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			expectedUserData:      new(newUserData),
			expectedAMIs:          []awskarpenterv1.AMISelectorTerm{{ID: newAMI}},
		},
		"When CP is stable, it should apply new values regardless": {
			hcp: stableHCP,
			ec2NodeClass: &awskarpenterv1.EC2NodeClass{
				Spec: awskarpenterv1.EC2NodeClassSpec{
					UserData:         oldUserData,
					AMISelectorTerms: oldAMI,
				},
			},
			openshiftEC2NodeClass: &openshiftkarpenterv1.OpenshiftEC2NodeClass{},
			expectedUserData:      new(newUserData),
			expectedAMIs:          []awskarpenterv1.AMISelectorTerm{{ID: newAMI}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			if tc.userDataSecret == nil {
				tc.userDataSecret = userDataSecret
			}
			desired := ec2NodeClassSpec(context.Background(), tc.openshiftEC2NodeClass, tc.hcp)
			err := reconcileEC2NodeClassRelease(context.Background(), &desired, tc.ec2NodeClass.Spec, tc.openshiftEC2NodeClass, tc.hcp, tc.userDataSecret)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(desired.UserData).To(Equal(tc.expectedUserData))
			g.Expect(desired.AMISelectorTerms).To(Equal(tc.expectedAMIs))
		})
	}
}

func TestReconcileStatus(t *testing.T) {
	tests := map[string]struct {
		ec2NodeClassStatus           awskarpenterv1.EC2NodeClassStatus
		expectedCapacityReservations []openshiftkarpenterv1.CapacityReservation
		expectedSubnets              []openshiftkarpenterv1.Subnet
		expectedSecurityGroups       []openshiftkarpenterv1.SecurityGroup
	}{
		"When EC2NodeClass has capacity reservations, it should mirror them to OpenshiftEC2NodeClass status": {
			ec2NodeClassStatus: awskarpenterv1.EC2NodeClassStatus{
				CapacityReservations: []awskarpenterv1.CapacityReservation{
					{
						AvailabilityZone:      "us-east-1a",
						ID:                    "cr-1234567890abcdef0",
						InstanceMatchCriteria: "targeted",
						InstanceType:          "m5.large",
						OwnerID:               "123456789012",
						ReservationType:       "default",
						State:                 "active",
					},
				},
			},
			expectedCapacityReservations: []openshiftkarpenterv1.CapacityReservation{
				{
					AvailabilityZone:      "us-east-1a",
					ID:                    "cr-1234567890abcdef0",
					InstanceMatchCriteria: openshiftkarpenterv1.InstanceMatchCriteriaTargeted,
					InstanceType:          "m5.large",
					OwnerID:               "123456789012",
					ReservationType:       openshiftkarpenterv1.CapacityReservationTypeDefault,
					State:                 openshiftkarpenterv1.CapacityReservationStateActive,
				},
			},
		},
		"When EC2NodeClass has subnets and security groups, it should mirror them to OpenshiftEC2NodeClass status": {
			ec2NodeClassStatus: awskarpenterv1.EC2NodeClassStatus{
				Subnets: []awskarpenterv1.Subnet{
					{ID: "subnet-abc123", Zone: "us-east-1a", ZoneID: "use1-az1"},
				},
				SecurityGroups: []awskarpenterv1.SecurityGroup{
					{ID: "sg-abc123", Name: "test-sg"},
				},
			},
			expectedSubnets: []openshiftkarpenterv1.Subnet{
				{ID: "subnet-abc123", Zone: "us-east-1a", ZoneID: "use1-az1"},
			},
			expectedSecurityGroups: []openshiftkarpenterv1.SecurityGroup{
				{ID: "sg-abc123", Name: "test-sg"},
			},
		},
		"When EC2NodeClass has no capacity reservations, it should leave status capacity reservations empty": {
			ec2NodeClassStatus:           awskarpenterv1.EC2NodeClassStatus{},
			expectedCapacityReservations: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			scheme := runtime.NewScheme()
			g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())

			openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(openshiftNodeClass).
				WithStatusSubresource(openshiftNodeClass).
				Build()

			r := &EC2NodeClassReconciler{hostedClient: fakeClient}

			ec2NodeClass := &awskarpenterv1.EC2NodeClass{
				Status: tc.ec2NodeClassStatus,
			}

			hcp := &hyperv1beta1.HostedControlPlane{Spec: hyperv1beta1.HostedControlPlaneSpec{InfraID: "test-infra"}}
			err := r.reconcileStatus(context.Background(), ec2NodeClass, openshiftNodeClass, hcp)
			g.Expect(err).ToNot(HaveOccurred())

			// Re-fetch to verify what was persisted via status patch
			updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
			g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())

			g.Expect(updated.Status.CapacityReservations).To(Equal(tc.expectedCapacityReservations))
			g.Expect(updated.Status.Subnets).To(Equal(tc.expectedSubnets))
			g.Expect(updated.Status.SecurityGroups).To(Equal(tc.expectedSecurityGroups))
		})
	}
}

func TestReconcileStatusIdempotency(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())

	openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(openshiftNodeClass).
		WithStatusSubresource(openshiftNodeClass).
		Build()

	r := &EC2NodeClassReconciler{hostedClient: fakeClient}

	ec2NodeClass := &awskarpenterv1.EC2NodeClass{
		Status: awskarpenterv1.EC2NodeClassStatus{
			CapacityReservations: []awskarpenterv1.CapacityReservation{
				{
					AvailabilityZone:      "us-east-1a",
					ID:                    "cr-1234567890abcdef0",
					InstanceMatchCriteria: "targeted",
					InstanceType:          "m5.large",
					OwnerID:               "123456789012",
					ReservationType:       "default",
					State:                 "active",
				},
			},
			Subnets: []awskarpenterv1.Subnet{
				{ID: "subnet-abc123", Zone: "us-east-1a", ZoneID: "use1-az1"},
			},
			SecurityGroups: []awskarpenterv1.SecurityGroup{
				{ID: "sg-abc123", Name: "test-sg"},
			},
		},
	}

	hcp := &hyperv1beta1.HostedControlPlane{Spec: hyperv1beta1.HostedControlPlaneSpec{InfraID: "test-infra"}}

	// When reconcileStatus is called twice with the same upstream status it should not accumulate entries
	g.Expect(r.reconcileStatus(context.Background(), ec2NodeClass, openshiftNodeClass, hcp)).To(Succeed())

	updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())

	g.Expect(r.reconcileStatus(context.Background(), ec2NodeClass, updated, hcp)).To(Succeed())

	final := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), final)).To(Succeed())

	// It should have exactly one entry for each, not two
	g.Expect(final.Status.CapacityReservations).To(HaveLen(1))
	g.Expect(final.Status.Subnets).To(HaveLen(1))
	g.Expect(final.Status.SecurityGroups).To(HaveLen(1))
}

func TestReconcileStatusSyncsConditions(t *testing.T) {
	g := NewWithT(t)

	openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Generation: 1},
		Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
			Conditions: []metav1.Condition{{
				Type:               openshiftkarpenterv1.ConditionTypeVersionResolved,
				Status:             metav1.ConditionFalse,
				Reason:             openshiftkarpenterv1.ConditionReasonResolutionFailed,
				Message:            "version 4.30.0 is not available in channel stable-4.21",
				ObservedGeneration: 1,
				LastTransitionTime: metav1.Now(),
			}},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(openshiftNodeClass).
		WithStatusSubresource(openshiftNodeClass).
		Build()
	r := &EC2NodeClassReconciler{hostedClient: fakeClient}

	ec2NodeClass := &awskarpenterv1.EC2NodeClass{
		Status: awskarpenterv1.EC2NodeClassStatus{
			Conditions: []status.Condition{
				{Type: awskarpenterv1.ConditionTypeSubnetsReady, Status: metav1.ConditionTrue, Reason: awskarpenterv1.ConditionTypeSubnetsReady},
				{Type: openshiftkarpenterv1.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: openshiftkarpenterv1.ConditionTypeReady},
			},
		},
	}

	// When the version is not resolved, it should copy the EC2NodeClass conditions but report not ready.
	g.Expect(r.reconcileStatus(t.Context(), ec2NodeClass, openshiftNodeClass, &hyperv1beta1.HostedControlPlane{})).To(Succeed())

	updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	g.Expect(fakeClient.Get(t.Context(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())
	g.Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, awskarpenterv1.ConditionTypeSubnetsReady)).To(BeTrue())
	g.Expect(meta.IsStatusConditionFalse(updated.Status.Conditions, openshiftkarpenterv1.ConditionTypeReady)).To(BeTrue())
}

func TestReconcileStatusPreservesIgnitionOwnedFields(t *testing.T) {
	g := NewWithT(t)

	openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Generation: 2},
		Status: openshiftkarpenterv1.OpenshiftEC2NodeClassStatus{
			ReleaseImage: "quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64",
			Version:      "4.21.10",
			Conditions: []metav1.Condition{
				{
					Type:               openshiftkarpenterv1.ConditionTypeVersionResolved,
					Status:             metav1.ConditionTrue,
					Reason:             openshiftkarpenterv1.ConditionReasonVersionResolved,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               openshiftkarpenterv1.ConditionTypeSupportedVersionSkew,
					Status:             metav1.ConditionTrue,
					Reason:             "AsExpected",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(openshiftNodeClass).
		WithStatusSubresource(openshiftNodeClass).
		Build()
	r := &EC2NodeClassReconciler{hostedClient: fakeClient}

	ec2NodeClass := &awskarpenterv1.EC2NodeClass{
		Status: awskarpenterv1.EC2NodeClassStatus{
			Subnets: []awskarpenterv1.Subnet{{ID: "subnet-0a1b2c3d4e5f60718", Zone: "us-east-1a", ZoneID: "use1-az1"}},
		},
	}
	g.Expect(r.reconcileStatus(t.Context(), ec2NodeClass, openshiftNodeClass, &hyperv1beta1.HostedControlPlane{})).To(Succeed())

	updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
	g.Expect(fakeClient.Get(t.Context(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())
	g.Expect(updated.Status.Subnets).To(HaveLen(1))
	g.Expect(updated.Status.ReleaseImage).To(Equal("quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64"))
	g.Expect(updated.Status.Version).To(Equal("4.21.10"))
	g.Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, openshiftkarpenterv1.ConditionTypeVersionResolved)).To(BeTrue())
	g.Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, openshiftkarpenterv1.ConditionTypeSupportedVersionSkew)).To(BeTrue())
}

func TestReconcileStatusTagConflictCondition(t *testing.T) {
	tests := map[string]struct {
		tags             map[string]string
		hcp              *hyperv1beta1.HostedControlPlane
		expectCondition  bool
		expectedStatus   metav1.ConditionStatus
		expectedReason   string
		expectedContains string
	}{
		"When platform AWS is nil, it should remove the condition": {
			tags: map[string]string{"key": "value"},
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{InfraID: "test-infra"},
			},
			expectCondition: false,
		},
		"When platform ResourceTags is empty, it should remove the condition": {
			tags: map[string]string{"key": "value"},
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS:  &hyperv1beta1.AWSPlatformSpec{ResourceTags: []hyperv1beta1.AWSClusterResourceTag{}},
					},
				},
			},
			expectCondition: false,
		},
		"When nodeclass tags is nil, it should remove the condition": {
			tags: nil,
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{{Key: "k", Value: "v"}},
						},
					},
				},
			},
			expectCondition: false,
		},
		"When tags have equal values, it should report no conflicts": {
			tags: map[string]string{"env": "prod"},
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{{Key: "env", Value: "prod"}},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionFalse,
			expectedReason:   hyperv1beta1.AWSResourceTagNoConflictReason,
			expectedContains: "No AWS resource tag conflicts",
		},
		"When nodeclass conflicts with unset overridePolicy, it should report conflict detected": {
			tags: map[string]string{"env": "staging"},
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{{Key: "env", Value: "prod"}},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionTrue,
			expectedReason:   hyperv1beta1.AWSResourceTagConflictDetectedReason,
			expectedContains: "1 AWS resource tag conflict(s) detected",
		},
		"When platform tag has Allow, it should report overrides applied": {
			tags: map[string]string{"env": "staging"},
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{
								{Key: "env", Value: "prod", OverridePolicy: hyperv1beta1.AWSResourceTagOverridePolicyAllow},
							},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionFalse,
			expectedReason:   hyperv1beta1.AWSResourceTagNoConflictReason,
			expectedContains: "1 AWS resource tag override(s) applied",
		},
		"When platform tag has Deny, it should report conflict detected": {
			tags: map[string]string{"env": "staging"},
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{
								{Key: "env", Value: "prod", OverridePolicy: hyperv1beta1.AWSResourceTagOverridePolicyDeny},
							},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionTrue,
			expectedReason:   hyperv1beta1.AWSResourceTagConflictDetectedReason,
			expectedContains: "1 AWS resource tag conflict(s) detected",
		},
		"When both blocked and allowed overrides exist, it should report both in message": {
			tags: map[string]string{"env": "staging", "team": "other"},
			hcp: &hyperv1beta1.HostedControlPlane{
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					InfraID: "test-infra",
					Platform: hyperv1beta1.PlatformSpec{
						Type: hyperv1beta1.AWSPlatform,
						AWS: &hyperv1beta1.AWSPlatformSpec{
							ResourceTags: []hyperv1beta1.AWSClusterResourceTag{
								{Key: "env", Value: "prod", OverridePolicy: hyperv1beta1.AWSResourceTagOverridePolicyDeny},
								{Key: "team", Value: "platform", OverridePolicy: hyperv1beta1.AWSResourceTagOverridePolicyAllow},
							},
						},
					},
				},
			},
			expectCondition:  true,
			expectedStatus:   metav1.ConditionTrue,
			expectedReason:   hyperv1beta1.AWSResourceTagConflictDetectedReason,
			expectedContains: "1 override(s) applied",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			scheme := runtime.NewScheme()
			g.Expect(openshiftkarpenterv1.AddToScheme(scheme)).To(Succeed())

			openshiftNodeClass := &openshiftkarpenterv1.OpenshiftEC2NodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
				Spec:       openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{Tags: tc.tags},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(openshiftNodeClass).
				WithStatusSubresource(openshiftNodeClass).
				Build()

			r := &EC2NodeClassReconciler{hostedClient: fakeClient}
			ec2NodeClass := &awskarpenterv1.EC2NodeClass{}

			g.Expect(r.reconcileStatus(context.Background(), ec2NodeClass, openshiftNodeClass, tc.hcp)).To(Succeed())

			updated := &openshiftkarpenterv1.OpenshiftEC2NodeClass{}
			g.Expect(fakeClient.Get(context.Background(), client.ObjectKeyFromObject(openshiftNodeClass), updated)).To(Succeed())

			cond := meta.FindStatusCondition(updated.Status.Conditions, hyperv1beta1.NodePoolAWSResourceTagConflictConditionType)
			if !tc.expectCondition {
				g.Expect(cond).To(BeNil())
				return
			}
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(Equal(tc.expectedStatus))
			g.Expect(cond.Reason).To(Equal(tc.expectedReason))
			g.Expect(cond.Message).To(ContainSubstring(tc.expectedContains))
		})
	}
}

func TestHCPPredicate(t *testing.T) {
	const namespace = "clusters-example"

	newHCP := func(instanceProfile string, tags ...hyperv1beta1.AWSClusterResourceTag) *hyperv1beta1.HostedControlPlane {
		hcp := &hyperv1beta1.HostedControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "example",
				Namespace: namespace,
			},
			Spec: hyperv1beta1.HostedControlPlaneSpec{
				Platform: hyperv1beta1.PlatformSpec{
					Type: hyperv1beta1.AWSPlatform,
					AWS: &hyperv1beta1.AWSPlatformSpec{
						ResourceTags: tags,
					},
				},
			},
		}
		if instanceProfile != "" {
			hcp.Annotations = map[string]string{hyperv1beta1.AWSKarpenterDefaultInstanceProfile: instanceProfile}
		}
		return hcp
	}
	costCenter := hyperv1beta1.AWSClusterResourceTag{Key: "cost-center", Value: "1234"}
	upgradingHCP := newHCP("example-4x7kq-worker")
	upgradingHCP.Status.ControlPlaneVersion = hyperv1beta1.ControlPlaneVersionStatus{
		Desired: configv1.Release{Image: "quay.io/openshift-release-dev/ocp-release:4.22.1-x86_64"},
		History: []hyperv1beta1.ControlPlaneUpdateHistory{
			{State: configv1.PartialUpdate, Image: "quay.io/openshift-release-dev/ocp-release:4.22.1-x86_64"},
			{State: configv1.CompletedUpdate, Image: "quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64"},
		},
	}
	upgradedHCP := upgradingHCP.DeepCopy()
	upgradedHCP.Status.ControlPlaneVersion.History[0].State = configv1.CompletedUpdate

	tests := map[string]struct {
		oldHCP         *hyperv1beta1.HostedControlPlane
		newHCP         *hyperv1beta1.HostedControlPlane
		expectedResult bool
	}{
		"When the instance profile annotation changes, it should accept the event": {
			oldHCP:         newHCP("example-4x7kq-worker"),
			newHCP:         newHCP("example-4x7kq-karpenter"),
			expectedResult: true,
		},
		"When a resource tag value changes, it should accept the event": {
			oldHCP:         newHCP("", costCenter),
			newHCP:         newHCP("", hyperv1beta1.AWSClusterResourceTag{Key: "cost-center", Value: "5678"}),
			expectedResult: true,
		},
		"When a resource tag override policy changes, it should accept the event": {
			oldHCP: newHCP("", costCenter),
			newHCP: newHCP("", hyperv1beta1.AWSClusterResourceTag{
				Key: "cost-center", Value: "1234", OverridePolicy: hyperv1beta1.AWSResourceTagOverridePolicyAllow,
			}),
			expectedResult: true,
		},
		"When a resource tag is added, it should accept the event": {
			oldHCP:         newHCP(""),
			newHCP:         newHCP("", costCenter),
			expectedResult: true,
		},
		"When a control plane upgrade completes, it should accept the event": {
			oldHCP:         upgradingHCP,
			newHCP:         upgradedHCP,
			expectedResult: true,
		},
		"When neither the instance profile nor the resource tags change, it should reject the event": {
			oldHCP:         newHCP("example-4x7kq-worker", costCenter),
			newHCP:         newHCP("example-4x7kq-worker", costCenter),
			expectedResult: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			r := &EC2NodeClassReconciler{}

			result := r.hcpPredicate().Update(event.UpdateEvent{ObjectOld: tc.oldHCP, ObjectNew: tc.newHCP})
			g.Expect(result).To(Equal(tc.expectedResult))
		})
	}

	t.Run("When a HostedControlPlane is created, it should accept the event", func(t *testing.T) {
		g := NewWithT(t)
		r := &EC2NodeClassReconciler{}
		g.Expect(r.hcpPredicate().Create(event.CreateEvent{Object: newHCP("example-4x7kq-worker")})).To(BeTrue())
	})
}

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = hyperv1beta1.AddToScheme(scheme)
	_ = openshiftkarpenterv1.AddToScheme(scheme)
	awsKarpenterGV := schema.GroupVersion{Group: "karpenter.k8s.aws", Version: "v1"}
	metav1.AddToGroupVersion(scheme, awsKarpenterGV)
	scheme.AddKnownTypes(awsKarpenterGV, &awskarpenterv1.EC2NodeClass{}, &awskarpenterv1.EC2NodeClassList{})
	return scheme
}
