// Package ec2 implements compute.Provider for AWS EC2 Auto Scaling Groups.
//
// It manages reconciler workers by adjusting the DesiredCapacity of ASGs.
// Each queue maps to an ASG named "{prefix}-{queue}".
package ec2

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	autoscalingtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	"github.com/hummingbird-org/factory/internal/compute"
)

// Config holds configuration for the EC2/ASG provider.
type Config struct {
	// ASGPrefix is prepended to the queue name to form the ASG name.
	// e.g., prefix "factory" + queue "rpm-update" → ASG "factory-rpm-update".
	ASGPrefix string

	// Region is the AWS region. If empty, uses the SDK default.
	Region string
}

// ASGClient is the subset of the autoscaling API we use.
// Defined as an interface for testing.
type ASGClient interface {
	UpdateAutoScalingGroup(ctx context.Context, params *autoscaling.UpdateAutoScalingGroupInput, optFns ...func(*autoscaling.Options)) (*autoscaling.UpdateAutoScalingGroupOutput, error)
	DescribeAutoScalingGroups(ctx context.Context, params *autoscaling.DescribeAutoScalingGroupsInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
}

// Provider implements compute.Provider for AWS EC2 Auto Scaling Groups.
type Provider struct {
	asg    ASGClient
	prefix string
}

// New creates a new EC2/ASG compute provider using default AWS credentials.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	asgClient := autoscaling.NewFromConfig(awsCfg)
	return NewWithClient(asgClient, cfg), nil
}

// NewWithClient creates a provider with an injected ASG client (for testing).
func NewWithClient(asg ASGClient, cfg Config) *Provider {
	prefix := cfg.ASGPrefix
	if prefix == "" {
		prefix = "factory"
	}
	return &Provider{
		asg:    asg,
		prefix: prefix,
	}
}

func (p *Provider) Name() string { return "ec2" }

func (p *Provider) asgName(queue string) string {
	return p.prefix + "-" + queue
}

func (p *Provider) EnsureWorkers(ctx context.Context, queue string, desired int) error {
	name := p.asgName(queue)

	// Check current desired capacity to avoid unnecessary API calls.
	desc, err := p.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{name},
	})
	if err != nil {
		return fmt.Errorf("describe ASG %s: %w", name, err)
	}
	if len(desc.AutoScalingGroups) == 0 {
		return fmt.Errorf("ASG %s not found", name)
	}

	current := desc.AutoScalingGroups[0].DesiredCapacity
	if current != nil && int(*current) == desired {
		return nil // already at desired count
	}

	_, err = p.asg.UpdateAutoScalingGroup(ctx, &autoscaling.UpdateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(name),
		DesiredCapacity:      aws.Int32(int32(desired)),
	})
	if err != nil {
		return fmt.Errorf("scale ASG %s to %d: %w", name, desired, err)
	}
	return nil
}

func (p *Provider) ScaleToZero(ctx context.Context, queue string) error {
	return p.EnsureWorkers(ctx, queue, 0)
}

func (p *Provider) WorkerStatus(ctx context.Context, queue string) ([]compute.WorkerInfo, error) {
	name := p.asgName(queue)

	desc, err := p.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{name},
	})
	if err != nil {
		return nil, fmt.Errorf("describe ASG %s: %w", name, err)
	}
	if len(desc.AutoScalingGroups) == 0 {
		return nil, nil
	}

	var workers []compute.WorkerInfo
	for _, instance := range desc.AutoScalingGroups[0].Instances {
		workers = append(workers, compute.WorkerInfo{
			ID:      aws.ToString(instance.InstanceId),
			Backend: "ec2",
			Status:  instanceStatus(instance),
			Metadata: map[string]string{
				"availability_zone": aws.ToString(instance.AvailabilityZone),
				"lifecycle_state":   string(instance.LifecycleState),
				"instance_type":     aws.ToString(instance.InstanceType),
			},
		})
	}
	return workers, nil
}

func (p *Provider) Cleanup(ctx context.Context, queue string) error {
	// ASG handles instance lifecycle automatically (terminating unhealthy instances,
	// respecting scale-in policies). No manual cleanup needed.
	return nil
}

func instanceStatus(instance autoscalingtypes.Instance) string {
	switch instance.LifecycleState {
	case autoscalingtypes.LifecycleStateInService:
		return "running"
	case autoscalingtypes.LifecycleStatePending,
		autoscalingtypes.LifecycleStatePendingWait,
		autoscalingtypes.LifecycleStatePendingProceed:
		return "pending"
	default:
		return "terminating"
	}
}

// Verify interface compliance.
var _ compute.Provider = (*Provider)(nil)
