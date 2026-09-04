package aws

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type EC2API interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type Provider struct {
	region          string
	infraName       string
	clusterEndpoint string
	karpenterImage  string
	ec2Client       EC2API
}

func New(ctx context.Context, infra common.InfrastructureInfo) (*Provider, error) {
	if infra.Region == "" {
		return nil, fmt.Errorf("region not available")
	}

	karpenterImage, err := common.RequireEnv(KarpenterImageEnvName)
	if err != nil {
		return nil, err
	}

	if _, err := common.RequireEnv(AWSSharedAuthFileEnvName); err != nil {
		return nil, err
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(infra.Region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &Provider{
		region:          infra.Region,
		infraName:       infra.InfraName,
		clusterEndpoint: infra.ClusterEndpoint,
		karpenterImage:  karpenterImage,
		ec2Client:       ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.Region = infra.Region }),
	}, nil
}
