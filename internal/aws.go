package internal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// AWSBackend wraps an EC2 client for cloud VM operations.
type AWSBackend struct {
	Client *ec2.Client
	Region string
}

// NewAWSBackend loads the default AWS config and creates an EC2 client.
// If region is empty, the SDK reads from ~/.aws/config.
func NewAWSBackend(region string) (*AWSBackend, error) {
	var opts []func(*config.LoadOptions) error
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := ec2.NewFromConfig(cfg)
	return &AWSBackend{Client: client, Region: cfg.Region}, nil
}

// EC2Instance holds summarised info about an EC2 instance.
type EC2Instance struct {
	ID         string
	Name       string
	State      string
	Type       string
	PublicIP   string
	PrivateIP  string
	LaunchTime time.Time
	Platform   string
	KeyName    string
}

// ListInstances returns all EC2 instances, optionally including terminated.
func (a *AWSBackend) ListInstances(includeTerminated bool) ([]EC2Instance, error) {
	var filters []types.Filter
	if !includeTerminated {
		filters = append(filters, types.Filter{
			Name:   aws.String("instance-state-name"),
			Values: []string{"pending", "running", "stopping", "stopped", "shutting-down"},
		})
	}
	input := &ec2.DescribeInstancesInput{}
	if len(filters) > 0 {
		input.Filters = filters
	}
	out, err := a.Client.DescribeInstances(context.Background(), input)
	if err != nil {
		return nil, err
	}
	var instances []EC2Instance
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			ei := EC2Instance{
				ID:        aws.ToString(inst.InstanceId),
				State:     string(inst.State.Name),
				Type:      string(inst.InstanceType),
				PublicIP:  aws.ToString(inst.PublicIpAddress),
				PrivateIP: aws.ToString(inst.PrivateIpAddress),
				KeyName:   aws.ToString(inst.KeyName),
				Platform:  aws.ToString(inst.PlatformDetails),
			}
			if inst.LaunchTime != nil {
				ei.LaunchTime = *inst.LaunchTime
			}
			for _, tag := range inst.Tags {
				if aws.ToString(tag.Key) == "Name" {
					ei.Name = aws.ToString(tag.Value)
				}
			}
			instances = append(instances, ei)
		}
	}
	return instances, nil
}

// GetInstance returns a single instance by ID.
func (a *AWSBackend) GetInstance(id string) (EC2Instance, error) {
	out, err := a.Client.DescribeInstances(context.Background(), &ec2.DescribeInstancesInput{
		InstanceIds: []string{id},
	})
	if err != nil {
		return EC2Instance{}, err
	}
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			ei := EC2Instance{
				ID:        aws.ToString(inst.InstanceId),
				State:     string(inst.State.Name),
				Type:      string(inst.InstanceType),
				PublicIP:  aws.ToString(inst.PublicIpAddress),
				PrivateIP: aws.ToString(inst.PrivateIpAddress),
				KeyName:   aws.ToString(inst.KeyName),
				Platform:  aws.ToString(inst.PlatformDetails),
			}
			if inst.LaunchTime != nil {
				ei.LaunchTime = *inst.LaunchTime
			}
			for _, tag := range inst.Tags {
				if aws.ToString(tag.Key) == "Name" {
					ei.Name = aws.ToString(tag.Value)
				}
			}
			return ei, nil
		}
	}
	return EC2Instance{}, fmt.Errorf("instance %s not found", id)
}

// StartInstances starts one or more stopped instances.
func (a *AWSBackend) StartInstances(ids []string) error {
	_, err := a.Client.StartInstances(context.Background(), &ec2.StartInstancesInput{
		InstanceIds: ids,
	})
	return err
}

// StopInstances stops one or more running instances. If force is true, a hard stop is performed.
func (a *AWSBackend) StopInstances(ids []string, force bool) error {
	_, err := a.Client.StopInstances(context.Background(), &ec2.StopInstancesInput{
		InstanceIds: ids,
		Force:       aws.Bool(force),
	})
	return err
}

// TerminateInstances terminates one or more instances.
func (a *AWSBackend) TerminateInstances(ids []string) error {
	_, err := a.Client.TerminateInstances(context.Background(), &ec2.TerminateInstancesInput{
		InstanceIds: ids,
	})
	return err
}

// WaitUntilRunning polls until the instance reaches the "running" state.
func (a *AWSBackend) WaitUntilRunning(id string, timeout time.Duration) error {
	waiter := ec2.NewInstanceRunningWaiter(a.Client)
	return waiter.Wait(context.Background(), &ec2.DescribeInstancesInput{
		InstanceIds: []string{id},
	}, timeout)
}

// WaitUntilStopped polls until the instance reaches the "stopped" state.
func (a *AWSBackend) WaitUntilStopped(id string, timeout time.Duration) error {
	waiter := ec2.NewInstanceStoppedWaiter(a.Client)
	return waiter.Wait(context.Background(), &ec2.DescribeInstancesInput{
		InstanceIds: []string{id},
	}, timeout)
}

// CreateInstance launches a new EC2 instance with the given parameters.
func (a *AWSBackend) CreateInstance(ami, name, instanceType, keyName, sgID, subnetID string) (string, error) {
	out, err := a.Client.RunInstances(context.Background(), &ec2.RunInstancesInput{
		ImageId:          aws.String(ami),
		InstanceType:     types.InstanceType(instanceType),
		KeyName:          aws.String(keyName),
		SecurityGroupIds: []string{sgID},
		SubnetId:         aws.String(subnetID),
		MinCount:         aws.Int32(1),
		MaxCount:         aws.Int32(1),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeInstance,
			Tags: []types.Tag{
				{Key: aws.String("Name"), Value: aws.String(name)},
				{Key: aws.String("ManagedBy"), Value: aws.String("rift")},
			},
		}},
	})
	if err != nil {
		return "", err
	}
	if len(out.Instances) == 0 {
		return "", fmt.Errorf("no instance returned from RunInstances")
	}
	return aws.ToString(out.Instances[0].InstanceId), nil
}

// CreateKeyPair creates a key pair and returns the PEM-encoded private key material.
func (a *AWSBackend) CreateKeyPair(name string) (string, error) {
	out, err := a.Client.CreateKeyPair(context.Background(), &ec2.CreateKeyPairInput{
		KeyName: aws.String(name),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.KeyMaterial), nil
}

// EnsureSecurityGroup creates or reuses a security group with SSH+RDP ingress.
func (a *AWSBackend) EnsureSecurityGroup(vpcID string) (string, error) {
	sgName := "rift-default"
	// Check if it already exists.
	desc, err := a.Client.DescribeSecurityGroups(context.Background(), &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{Name: aws.String("group-name"), Values: []string{sgName}},
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		},
	})
	if err == nil && len(desc.SecurityGroups) > 0 {
		return aws.ToString(desc.SecurityGroups[0].GroupId), nil
	}
	// Create it.
	create, err := a.Client.CreateSecurityGroup(context.Background(), &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(sgName),
		Description: aws.String("Rift default - SSH and RDP inbound"),
		VpcId:       aws.String(vpcID),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeSecurityGroup,
			Tags:         []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("rift")}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("creating security group: %w", err)
	}
	sgID := aws.ToString(create.GroupId)
	// Authorize SSH (22) and RDP (3389) ingress.
	_, err = a.Client.AuthorizeSecurityGroupIngress(context.Background(), &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: aws.String(sgID),
		IpPermissions: []types.IpPermission{
			{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), IpRanges: []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}},
			{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(3389), ToPort: aws.Int32(3389), IpRanges: []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}},
		},
	})
	if err != nil {
		return sgID, fmt.Errorf("authorizing ingress: %w", err)
	}
	return sgID, nil
}

// GetDefaultVPC returns the default VPC ID.
func (a *AWSBackend) GetDefaultVPC() (string, error) {
	out, err := a.Client.DescribeVpcs(context.Background(), &ec2.DescribeVpcsInput{
		Filters: []types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil {
		return "", err
	}
	if len(out.Vpcs) == 0 {
		return "", fmt.Errorf("no default VPC found")
	}
	return aws.ToString(out.Vpcs[0].VpcId), nil
}

// GetFirstSubnet returns the first available subnet in the given VPC.
func (a *AWSBackend) GetFirstSubnet(vpcID string) (string, error) {
	out, err := a.Client.DescribeSubnets(context.Background(), &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	if err != nil {
		return "", err
	}
	if len(out.Subnets) == 0 {
		return "", fmt.Errorf("no subnets found in VPC %s", vpcID)
	}
	return aws.ToString(out.Subnets[0].SubnetId), nil
}

// AllocateAndAssociateEIP allocates an Elastic IP and associates it with an instance.
// Returns the public IP address and allocation ID.
func (a *AWSBackend) AllocateAndAssociateEIP(instanceID string) (publicIP, allocationID string, err error) {
	alloc, err := a.Client.AllocateAddress(context.Background(), &ec2.AllocateAddressInput{
		Domain: types.DomainTypeVpc,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeElasticIp,
			Tags:         []types.Tag{{Key: aws.String("ManagedBy"), Value: aws.String("rift")}},
		}},
	})
	if err != nil {
		return "", "", fmt.Errorf("allocating EIP: %w", err)
	}
	_, err = a.Client.AssociateAddress(context.Background(), &ec2.AssociateAddressInput{
		AllocationId: alloc.AllocationId,
		InstanceId:   aws.String(instanceID),
	})
	if err != nil {
		return "", "", fmt.Errorf("associating EIP: %w", err)
	}
	return aws.ToString(alloc.PublicIp), aws.ToString(alloc.AllocationId), nil
}

// ReleaseInstanceEIPs finds and releases any Elastic IPs associated with an instance.
func (a *AWSBackend) ReleaseInstanceEIPs(instanceID string) error {
	out, err := a.Client.DescribeAddresses(context.Background(), &ec2.DescribeAddressesInput{
		Filters: []types.Filter{{Name: aws.String("instance-id"), Values: []string{instanceID}}},
	})
	if err != nil {
		return err
	}
	for _, addr := range out.Addresses {
		if addr.AssociationId != nil {
			a.Client.DisassociateAddress(context.Background(), &ec2.DisassociateAddressInput{
				AssociationId: addr.AssociationId,
			})
		}
		if _, err := a.Client.ReleaseAddress(context.Background(), &ec2.ReleaseAddressInput{
			AllocationId: addr.AllocationId,
		}); err != nil {
			return fmt.Errorf("releasing EIP %s: %w", aws.ToString(addr.PublicIp), err)
		}
	}
	return nil
}

// DeleteKeyPair deletes an AWS key pair by name.
func (a *AWSBackend) DeleteKeyPair(name string) error {
	_, err := a.Client.DeleteKeyPair(context.Background(), &ec2.DeleteKeyPairInput{
		KeyName: aws.String(name),
	})
	return err
}

// ReleaseEIP releases an Elastic IP by allocation ID.
func (a *AWSBackend) ReleaseEIP(allocationID string) error {
	// Disassociate first (best-effort).
	addrs, err := a.Client.DescribeAddresses(context.Background(), &ec2.DescribeAddressesInput{
		AllocationIds: []string{allocationID},
	})
	if err == nil {
		for _, addr := range addrs.Addresses {
			if addr.AssociationId != nil {
				a.Client.DisassociateAddress(context.Background(), &ec2.DisassociateAddressInput{
					AssociationId: addr.AssociationId,
				})
			}
		}
	}
	_, err = a.Client.ReleaseAddress(context.Background(), &ec2.ReleaseAddressInput{
		AllocationId: aws.String(allocationID),
	})
	return err
}

// DeleteSecurityGroup deletes a security group by ID.
func (a *AWSBackend) DeleteSecurityGroup(sgID string) error {
	_, err := a.Client.DeleteSecurityGroup(context.Background(), &ec2.DeleteSecurityGroupInput{
		GroupId: aws.String(sgID),
	})
	return err
}

// GuessSSHUser returns a likely default SSH user based on the platform string.
func GuessSSHUser(platform string) string {
	p := strings.ToLower(platform)
	switch {
	case strings.Contains(p, "ubuntu"):
		return "ubuntu"
	case strings.Contains(p, "debian"):
		return "admin"
	case strings.Contains(p, "windows"):
		return "Administrator"
	default:
		return "ec2-user"
	}
}
