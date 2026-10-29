package aws

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type fakeEC2Client struct {
	describeInstances   func(*ec2.DescribeInstancesInput) (*ec2.DescribeInstancesOutput, error)
	describeVpcs        func(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error)
	describeDhcpOptions func(*ec2.DescribeDhcpOptionsInput) (*ec2.DescribeDhcpOptionsOutput, error)

	instanceInputs    []*ec2.DescribeInstancesInput
	vpcInputs         []*ec2.DescribeVpcsInput
	dhcpOptionsInputs []*ec2.DescribeDhcpOptionsInput
}

func (f *fakeEC2Client) DescribeInstances(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.instanceInputs = append(f.instanceInputs, input)
	if f.describeInstances == nil {
		return nil, errors.New("unexpected DescribeInstances call")
	}
	return f.describeInstances(input)
}

func (f *fakeEC2Client) DescribeVpcs(_ context.Context, input *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	f.vpcInputs = append(f.vpcInputs, input)
	if f.describeVpcs == nil {
		return nil, errors.New("unexpected DescribeVpcs call")
	}
	return f.describeVpcs(input)
}

func (f *fakeEC2Client) DescribeDhcpOptions(_ context.Context, input *ec2.DescribeDhcpOptionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeDhcpOptionsOutput, error) {
	f.dhcpOptionsInputs = append(f.dhcpOptionsInputs, input)
	if f.describeDhcpOptions == nil {
		return nil, errors.New("unexpected DescribeDhcpOptions call")
	}
	return f.describeDhcpOptions(input)
}
