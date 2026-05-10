package ec2

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	autoscalingtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
)

// mockASG implements ASGClient for testing.
type mockASG struct {
	groups        map[string]autoscalingtypes.AutoScalingGroup
	updateCalls   int
	describeCalls int
}

func newMockASG() *mockASG {
	return &mockASG{groups: make(map[string]autoscalingtypes.AutoScalingGroup)}
}

func (m *mockASG) addGroup(name string, desired int32, instances ...autoscalingtypes.Instance) {
	m.groups[name] = autoscalingtypes.AutoScalingGroup{
		AutoScalingGroupName: aws.String(name),
		DesiredCapacity:      aws.Int32(desired),
		Instances:            instances,
	}
}

func (m *mockASG) DescribeAutoScalingGroups(_ context.Context, params *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	m.describeCalls++
	var groups []autoscalingtypes.AutoScalingGroup
	for _, name := range params.AutoScalingGroupNames {
		if g, ok := m.groups[name]; ok {
			groups = append(groups, g)
		}
	}
	return &autoscaling.DescribeAutoScalingGroupsOutput{
		AutoScalingGroups: groups,
	}, nil
}

func (m *mockASG) UpdateAutoScalingGroup(_ context.Context, params *autoscaling.UpdateAutoScalingGroupInput, _ ...func(*autoscaling.Options)) (*autoscaling.UpdateAutoScalingGroupOutput, error) {
	m.updateCalls++
	name := aws.ToString(params.AutoScalingGroupName)
	if g, ok := m.groups[name]; ok {
		g.DesiredCapacity = params.DesiredCapacity
		m.groups[name] = g
		return &autoscaling.UpdateAutoScalingGroupOutput{}, nil
	}
	return nil, fmt.Errorf("ASG %s not found", name)
}

func TestNewWithClient_Defaults(t *testing.T) {
	p := NewWithClient(newMockASG(), Config{})
	if p.prefix != "factory" {
		t.Errorf("expected default prefix factory, got %s", p.prefix)
	}
}

func TestNewWithClient_Custom(t *testing.T) {
	p := NewWithClient(newMockASG(), Config{ASGPrefix: "myapp"})
	if p.prefix != "myapp" {
		t.Errorf("expected prefix myapp, got %s", p.prefix)
	}
}

func TestName(t *testing.T) {
	p := NewWithClient(newMockASG(), Config{})
	if p.Name() != "ec2" {
		t.Errorf("expected name ec2, got %s", p.Name())
	}
}

func TestEnsureWorkers_ScalesASG(t *testing.T) {
	mock := newMockASG()
	mock.addGroup("factory-build", 1)
	p := NewWithClient(mock, Config{})

	if err := p.EnsureWorkers(context.Background(), "build", 5); err != nil {
		t.Fatalf("EnsureWorkers: %v", err)
	}

	g := mock.groups["factory-build"]
	if aws.ToInt32(g.DesiredCapacity) != 5 {
		t.Errorf("expected desired capacity 5, got %d", aws.ToInt32(g.DesiredCapacity))
	}
	if mock.updateCalls != 1 {
		t.Errorf("expected 1 update call, got %d", mock.updateCalls)
	}
}

func TestEnsureWorkers_NoopWhenAlreadyAtDesired(t *testing.T) {
	mock := newMockASG()
	mock.addGroup("factory-build", 3)
	p := NewWithClient(mock, Config{})

	if err := p.EnsureWorkers(context.Background(), "build", 3); err != nil {
		t.Fatalf("EnsureWorkers: %v", err)
	}

	if mock.updateCalls != 0 {
		t.Errorf("expected 0 update calls (noop), got %d", mock.updateCalls)
	}
}

func TestEnsureWorkers_ASGNotFound(t *testing.T) {
	mock := newMockASG()
	p := NewWithClient(mock, Config{})

	err := p.EnsureWorkers(context.Background(), "missing", 1)
	if err == nil {
		t.Fatal("expected error for missing ASG")
	}
}

func TestScaleToZero(t *testing.T) {
	mock := newMockASG()
	mock.addGroup("factory-build", 5)
	p := NewWithClient(mock, Config{})

	if err := p.ScaleToZero(context.Background(), "build"); err != nil {
		t.Fatalf("ScaleToZero: %v", err)
	}

	g := mock.groups["factory-build"]
	if aws.ToInt32(g.DesiredCapacity) != 0 {
		t.Errorf("expected desired capacity 0, got %d", aws.ToInt32(g.DesiredCapacity))
	}
}

func TestWorkerStatus(t *testing.T) {
	mock := newMockASG()
	mock.addGroup("factory-build", 2,
		autoscalingtypes.Instance{
			InstanceId:       aws.String("i-abc123"),
			AvailabilityZone: aws.String("us-east-1a"),
			LifecycleState:   autoscalingtypes.LifecycleStateInService,
			InstanceType:     aws.String("m5.xlarge"),
		},
		autoscalingtypes.Instance{
			InstanceId:       aws.String("i-def456"),
			AvailabilityZone: aws.String("us-east-1b"),
			LifecycleState:   autoscalingtypes.LifecycleStatePending,
			InstanceType:     aws.String("m5.xlarge"),
		},
	)
	p := NewWithClient(mock, Config{})

	workers, err := p.WorkerStatus(context.Background(), "build")
	if err != nil {
		t.Fatalf("WorkerStatus: %v", err)
	}
	if len(workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(workers))
	}

	statusMap := map[string]string{}
	for _, w := range workers {
		statusMap[w.ID] = w.Status
		if w.Backend != "ec2" {
			t.Errorf("expected backend ec2, got %s", w.Backend)
		}
	}
	if statusMap["i-abc123"] != "running" {
		t.Errorf("expected InService to be running, got %s", statusMap["i-abc123"])
	}
	if statusMap["i-def456"] != "pending" {
		t.Errorf("expected Pending to be pending, got %s", statusMap["i-def456"])
	}
}

func TestWorkerStatus_NoASG(t *testing.T) {
	mock := newMockASG()
	p := NewWithClient(mock, Config{})

	workers, err := p.WorkerStatus(context.Background(), "missing")
	if err != nil {
		t.Fatalf("WorkerStatus: %v", err)
	}
	if workers != nil {
		t.Errorf("expected nil workers for missing ASG, got %v", workers)
	}
}

func TestCleanup_NoOp(t *testing.T) {
	mock := newMockASG()
	p := NewWithClient(mock, Config{})

	// Cleanup is a no-op for EC2/ASG — ASG handles instance lifecycle.
	if err := p.Cleanup(context.Background(), "build"); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
}

func TestInstanceStatus(t *testing.T) {
	tests := []struct {
		state    autoscalingtypes.LifecycleState
		expected string
	}{
		{autoscalingtypes.LifecycleStateInService, "running"},
		{autoscalingtypes.LifecycleStatePending, "pending"},
		{autoscalingtypes.LifecycleStatePendingWait, "pending"},
		{autoscalingtypes.LifecycleStatePendingProceed, "pending"},
		{autoscalingtypes.LifecycleStateTerminating, "terminating"},
		{autoscalingtypes.LifecycleStateTerminatingWait, "terminating"},
		{autoscalingtypes.LifecycleStateDetaching, "terminating"},
	}
	for _, tt := range tests {
		got := instanceStatus(autoscalingtypes.Instance{LifecycleState: tt.state})
		if got != tt.expected {
			t.Errorf("instanceStatus(%s) = %s, want %s", tt.state, got, tt.expected)
		}
	}
}
