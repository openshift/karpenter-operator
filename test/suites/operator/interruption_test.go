package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	pollThreeMinutes = 3 * time.Minute
	pollFiveMinutes  = 5 * time.Minute
	pollTenSecond    = 10 * time.Second
)

var _ = Describe("SQS Interruption", Ordered, func() {
	var (
		ctx       context.Context
		region    string
		infraName string
		queueName string
		queueURL  string
		accountID string
	)

	BeforeAll(func() {
		ctx = context.Background()

		if env.IsManagementCluster() {
			Skip("SQS interruption E2E for HCP requires HyperShift KarpenterAWSConfig.InterruptionQueueURL")
		}
		if !env.IsAWSPlatform() {
			Skip("SQS interruption tests require an AWS cluster")
		}
		if _, err := exec.LookPath("aws"); err != nil {
			Skip("aws CLI not found in PATH")
		}

		region = env.Infrastructure.Region
		infraName = env.Infrastructure.InfraName
		if region == "" || infraName == "" {
			Skip("cluster region or infraName not available")
		}

		out, err := awsCLI("sts", "get-caller-identity", "--query", "Account", "--output", "text", "--region", region)
		if err != nil {
			Skip(fmt.Sprintf("AWS credentials not available: %v", err))
		}
		accountID = strings.TrimSpace(out)
		queueName = infraName + "-interruption-test"
	})

	AfterAll(func() {
		cleanupInterruptionTest(ctx, infraName, queueName, region)
	})

	It("should create the SQS queue", func() {
		out, err := awsCLI("sqs", "create-queue",
			"--queue-name", queueName,
			"--region", region,
			"--attributes", `{"MessageRetentionPeriod":"300","SqsManagedSseEnabled":"true"}`,
			"--query", "QueueUrl", "--output", "text")
		Expect(err).NotTo(HaveOccurred(), "failed to create SQS queue: %s", out)
		queueURL = strings.TrimSpace(out)
		Expect(queueURL).NotTo(BeEmpty())
	})

	It("should create a Karpenter CR with interruptionQueue", func() {
		karp := &autoscalingv1alpha1.Karpenter{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Spec: autoscalingv1alpha1.KarpenterSpec{
				InterruptionQueue: queueName,
			},
		}
		Expect(env.Client.Create(ctx, karp)).To(Succeed())
		waitForOperandReady(ctx)
	})

	It("should set INTERRUPTION_QUEUE env var on the operand", func() {
		Eventually(func(g Gomega) {
			dep := &appsv1.Deployment{}
			g.Expect(env.Client.Get(ctx, types.NamespacedName{
				Name:      operandDeploymentName,
				Namespace: operatorNamespace,
			}, dep)).To(Succeed())

			var found bool
			for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
				if e.Name == "INTERRUPTION_QUEUE" {
					g.Expect(e.Value).To(Equal(queueName))
					found = true
					break
				}
			}
			g.Expect(found).To(BeTrue(), "expected INTERRUPTION_QUEUE env var on operand")
		}, pollOneMinute, pollFiveSecond).Should(Succeed())
	})

	It("should create EC2NodeClass and NodePool for spot instances", func() {
		subnets := discoverSubnets(infraName, region)
		Expect(subnets).NotTo(BeEmpty(), "no subnets found for cluster")

		sgIDs := discoverNodeSecurityGroups(infraName, region)
		Expect(sgIDs).NotTo(BeEmpty(), "no node security groups found")

		amiID := discoverWorkerAMI(infraName, region)
		Expect(amiID).NotTo(BeEmpty(), "no worker AMI found")

		roleName := discoverWorkerRole(infraName, region)
		Expect(roleName).NotTo(BeEmpty(), "no worker IAM role found")

		subnetTerms := make([]awskarpenterv1.SubnetSelectorTerm, len(subnets))
		for i, s := range subnets {
			subnetTerms[i] = awskarpenterv1.SubnetSelectorTerm{ID: s}
		}

		sgTerms := make([]awskarpenterv1.SecurityGroupSelectorTerm, len(sgIDs))
		for i, sg := range sgIDs {
			sgTerms[i] = awskarpenterv1.SecurityGroupSelectorTerm{ID: sg}
		}

		amiFamily := "Custom"
		nc := &awskarpenterv1.EC2NodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "interruption-test"},
			Spec: awskarpenterv1.EC2NodeClassSpec{
				AMISelectorTerms:           []awskarpenterv1.AMISelectorTerm{{ID: amiID}},
				AMIFamily:                  &amiFamily,
				SubnetSelectorTerms:        subnetTerms,
				SecurityGroupSelectorTerms: sgTerms,
				Role:                       roleName,
			},
		}
		Expect(env.Client.Create(ctx, nc)).To(Succeed())

		np := &karpenterv1.NodePool{
			ObjectMeta: metav1.ObjectMeta{Name: "interruption-test"},
			Spec: karpenterv1.NodePoolSpec{
				Template: karpenterv1.NodeClaimTemplate{
					Spec: karpenterv1.NodeClaimTemplateSpec{
						Requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
							{
								Key:      "karpenter.sh/capacity-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"spot"},
							},
							{
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"m5.large", "m5.xlarge", "m5a.large", "m5a.xlarge", "m6i.large", "c5.large", "c5a.large"},
							},
						},
						NodeClassRef: &karpenterv1.NodeClassReference{
							Group: "karpenter.k8s.aws",
							Kind:  "EC2NodeClass",
							Name:  "interruption-test",
						},
					},
				},
				Disruption: karpenterv1.Disruption{
					ConsolidateAfter: karpenterv1.MustParseNillableDuration("Never"),
				},
				Limits: karpenterv1.Limits(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("10"),
				}),
			},
		}
		Expect(env.Client.Create(ctx, np)).To(Succeed())
	})

	It("should provision a spot node for the test workload", func() {
		replicas := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "interruption-test-workload",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "interruption-test"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "interruption-test"},
					},
					Spec: corev1.PodSpec{
						NodeSelector: map[string]string{
							"karpenter.sh/capacity-type": "spot",
						},
						Containers: []corev1.Container{{
							Name:  "pause",
							Image: "registry.k8s.io/pause:3.9",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("250m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						}},
					},
				},
			},
		}
		Expect(env.Client.Create(ctx, dep)).To(Succeed())

		Eventually(func(g Gomega) {
			claims := &karpenterv1.NodeClaimList{}
			g.Expect(env.Client.List(ctx, claims)).To(Succeed())
			g.Expect(len(claims.Items)).To(BeNumerically(">=", 1), "expected at least one NodeClaim")

			var launched bool
			for _, nc := range claims.Items {
				if nc.Status.ProviderID != "" {
					launched = true
					break
				}
			}
			g.Expect(launched).To(BeTrue(), "expected at least one NodeClaim with a provider ID (launched)")
		}, pollFiveMinutes, pollTenSecond).Should(Succeed())
	})

	It("should handle a simulated spot interruption", func() {
		claims := &karpenterv1.NodeClaimList{}
		Expect(env.Client.List(ctx, claims)).To(Succeed())
		Expect(claims.Items).NotTo(BeEmpty())

		claim := claims.Items[0]
		instanceID := extractInstanceID(claim.Status.ProviderID)
		Expect(instanceID).NotTo(BeEmpty(), "could not extract instance ID from providerID %s", claim.Status.ProviderID)
		originalClaimName := claim.Name

		msg := fmt.Sprintf(`{
			"version": "0",
			"id": "test-interruption-e2e",
			"detail-type": "EC2 Spot Instance Interruption Warning",
			"source": "aws.ec2",
			"account": "%s",
			"time": "%s",
			"region": "%s",
			"resources": ["arn:aws:ec2:%s:%s:instance/%s"],
			"detail": {
				"instance-id": "%s",
				"instance-action": "terminate"
			}
		}`, accountID, time.Now().UTC().Format(time.RFC3339), region, region, accountID, instanceID, instanceID)

		out, err := awsCLI("sqs", "send-message",
			"--queue-url", queueURL,
			"--message-body", msg,
			"--region", region)
		Expect(err).NotTo(HaveOccurred(), "failed to send SQS message: %s", out)

		// Karpenter should delete the interrupted NodeClaim and launch a replacement.
		Eventually(func(g Gomega) {
			claims := &karpenterv1.NodeClaimList{}
			g.Expect(env.Client.List(ctx, claims)).To(Succeed())

			originalGone := true
			for _, nc := range claims.Items {
				if nc.Name == originalClaimName && nc.DeletionTimestamp == nil {
					originalGone = false
				}
			}
			g.Expect(originalGone).To(BeTrue(),
				"original NodeClaim %s should be deleted or deleting after spot interruption", originalClaimName)
		}, pollThreeMinutes, pollFiveSecond).Should(Succeed())
	})

})

func cleanupInterruptionTest(ctx context.Context, infraName, queueName, region string) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "interruption-test-workload", Namespace: "default"},
	}
	_ = client.IgnoreNotFound(env.Client.Delete(ctx, dep))

	np := &karpenterv1.NodePool{ObjectMeta: metav1.ObjectMeta{Name: "interruption-test"}}
	_ = client.IgnoreNotFound(env.Client.Delete(ctx, np))

	// Wait for NodeClaims to drain before deleting EC2NodeClass. The operand
	// processes NodePool/NodeClaim finalizers which terminate EC2 instances.
	Eventually(func(g Gomega) {
		claims := &karpenterv1.NodeClaimList{}
		g.Expect(env.Client.List(ctx, claims)).To(Succeed())
		g.Expect(claims.Items).To(BeEmpty(), "waiting for NodeClaims to be fully removed")
	}, pollThreeMinutes, pollFiveSecond).Should(Succeed())

	nc := &awskarpenterv1.EC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: "interruption-test"}}
	_ = client.IgnoreNotFound(env.Client.Delete(ctx, nc))

	// Wait for EC2NodeClass finalizer to be processed before deleting the
	// Karpenter CR (which shuts down the operand that handles finalizers).
	Eventually(func(g Gomega) {
		err := env.Client.Get(ctx, types.NamespacedName{Name: "interruption-test"}, &awskarpenterv1.EC2NodeClass{})
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "waiting for EC2NodeClass to be fully removed")
	}, pollThreeMinutes, pollFiveSecond).Should(Succeed())

	cleanupTaggedResources(infraName, region)

	deleteKarpenterCR(ctx)

	if queueName != "" && region != "" {
		out, _ := awsCLI("sts", "get-caller-identity", "--query", "Account", "--output", "text", "--region", region)
		accountID := strings.TrimSpace(out)
		if accountID != "" {
			queueURL := fmt.Sprintf("https://sqs.%s.amazonaws.com/%s/%s", region, accountID, queueName)
			_, _ = awsCLI("sqs", "delete-queue", "--queue-url", queueURL, "--region", region)
		}
	}
}

// cleanupTaggedResources finds EC2 fleets and spot instance requests tagged
// with the cluster ownership tag and deletes/cancels them, then removes the
// tag. Karpenter's CreateFleet (type "instant") leaves these behind after
// instance termination, and openshift-install destroy loops on them because
// it doesn't handle these resource types.
//
// We use the resource tagging API for discovery because describe-fleets
// tag filters don't work for instant-type fleets.
//
//nolint:gocyclo
func cleanupTaggedResources(infraName, region string) {
	if infraName == "" || region == "" {
		return
	}
	clusterTag := fmt.Sprintf("kubernetes.io/cluster/%s", infraName)
	tagFilter := fmt.Sprintf("Key=%s,Values=owned", clusterTag)

	for _, resourceType := range []string{"ec2:fleet", "ec2:spot-instances-request"} {
		out, err := awsCLI("resourcegroupstaggingapi", "get-resources",
			"--tag-filters", tagFilter,
			"--resource-type-filters", resourceType,
			"--query", "ResourceTagMappingList[*].ResourceARN",
			"--output", "json",
			"--region", region)
		if err != nil {
			continue
		}
		var arns []string
		if json.Unmarshal([]byte(out), &arns) != nil || len(arns) == 0 {
			continue
		}

		var ids []string
		for _, arn := range arns {
			parts := strings.Split(arn, "/")
			if len(parts) >= 2 {
				ids = append(ids, parts[len(parts)-1])
			}
		}
		if len(ids) == 0 {
			continue
		}

		var successIDs []string
		switch resourceType {
		case "ec2:fleet":
			args := append([]string{"ec2", "delete-fleets", "--terminate-instances", "--fleet-ids"}, ids...)
			args = append(args, "--region", region)
			out, err = awsCLI(args...)
			if err != nil {
				continue
			}
			var result struct {
				SuccessfulFleetDeletions []struct {
					FleetId string `json:"FleetId"`
				} `json:"SuccessfulFleetDeletions"`
			}
			if json.Unmarshal([]byte(out), &result) == nil {
				for _, f := range result.SuccessfulFleetDeletions {
					successIDs = append(successIDs, f.FleetId)
				}
			}
		case "ec2:spot-instances-request":
			args := append([]string{"ec2", "cancel-spot-instance-requests", "--spot-instance-request-ids"}, ids...)
			args = append(args, "--region", region)
			out, err = awsCLI(args...)
			if err != nil {
				continue
			}
			var result struct {
				CancelledSpotInstanceRequests []struct {
					SpotInstanceRequestId string `json:"SpotInstanceRequestId"`
				} `json:"CancelledSpotInstanceRequests"`
			}
			if json.Unmarshal([]byte(out), &result) == nil {
				for _, r := range result.CancelledSpotInstanceRequests {
					successIDs = append(successIDs, r.SpotInstanceRequestId)
				}
			}
		}

		// Only untag resources that were successfully deleted/canceled so
		// we don't orphan active resources by hiding them from tag-based cleanup.
		if len(successIDs) > 0 {
			untagArgs := append([]string{"ec2", "delete-tags", "--tags", "Key=" + clusterTag, "--resources"}, successIDs...)
			untagArgs = append(untagArgs, "--region", region)
			_, _ = awsCLI(untagArgs...)
		}
	}
}

func awsCLI(args ...string) (string, error) {
	cmd := exec.Command("aws", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func discoverSubnets(infraName, region string) []string {
	out, err := awsCLI("ec2", "describe-subnets",
		"--filters",
		fmt.Sprintf("Name=tag:kubernetes.io/cluster/%s,Values=owned", infraName),
		"Name=tag:kubernetes.io/role/internal-elb,Values=*",
		"--query", "Subnets[*].SubnetId",
		"--output", "json",
		"--region", region)
	if err != nil {
		return nil
	}
	var subnets []string
	if json.Unmarshal([]byte(out), &subnets) != nil {
		return nil
	}
	return subnets
}

func discoverNodeSecurityGroups(infraName, region string) []string {
	out, err := awsCLI("ec2", "describe-instances",
		"--filters",
		fmt.Sprintf("Name=tag:kubernetes.io/cluster/%s,Values=owned", infraName),
		"Name=instance-state-name,Values=running",
		fmt.Sprintf("Name=tag:Name,Values=%s-worker-*", infraName),
		"--query", "Reservations[0].Instances[0].SecurityGroups[*].GroupId",
		"--output", "json",
		"--region", region)
	if err != nil {
		return nil
	}
	var sgIDs []string
	if json.Unmarshal([]byte(out), &sgIDs) != nil {
		return nil
	}
	return sgIDs
}

func discoverWorkerAMI(infraName, region string) string {
	out, err := awsCLI("ec2", "describe-instances",
		"--filters",
		fmt.Sprintf("Name=tag:kubernetes.io/cluster/%s,Values=owned", infraName),
		"Name=instance-state-name,Values=running",
		fmt.Sprintf("Name=tag:Name,Values=%s-worker-*", infraName),
		"--query", "Reservations[0].Instances[0].ImageId",
		"--output", "text",
		"--region", region)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func discoverWorkerRole(infraName, region string) string {
	out, err := awsCLI("iam", "get-instance-profile",
		"--instance-profile-name", infraName+"-worker-profile",
		"--query", "InstanceProfile.Roles[0].RoleName",
		"--output", "text")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

var providerIDRe = regexp.MustCompile(`i-[0-9a-f]+`)

func extractInstanceID(providerID string) string {
	match := providerIDRe.FindString(providerID)
	return match
}
