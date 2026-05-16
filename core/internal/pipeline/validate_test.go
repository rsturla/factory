package pipeline

import (
	"strings"
	"testing"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		spec    *v1.PipelineSpec
		wantErr string
	}{
		{
			name: "valid pipeline",
			spec: &v1.PipelineSpec{
				Name: "test",
				Resources: map[string]v1.Resource{
					"repo": {Type: "git", Access: "read-write"},
				},
				Stages: []v1.StageSpec{
					{
						Name: "build",
						Agent: v1.AgentConfig{
							Image:     "test-image",
							Command:   []string{"make", "build"},
							Resources: []string{"repo"},
						},
						Output: v1.OutputConfig{Type: "report"},
					},
				},
			},
			wantErr: "",
		},
		{
			name: "missing name",
			spec: &v1.PipelineSpec{
				Stages: []v1.StageSpec{
					{Name: "test", Agent: v1.AgentConfig{Image: "img", Command: []string{"cmd"}}, Output: v1.OutputConfig{Type: "report"}},
				},
			},
			wantErr: "pipeline name is required",
		},
		{
			name: "no stages",
			spec: &v1.PipelineSpec{
				Name:   "test",
				Stages: []v1.StageSpec{},
			},
			wantErr: "pipeline must have at least one stage",
		},
		{
			name: "duplicate stage names",
			spec: &v1.PipelineSpec{
				Name: "test",
				Stages: []v1.StageSpec{
					{Name: "build", Agent: v1.AgentConfig{Image: "img", Command: []string{"cmd"}}, Output: v1.OutputConfig{Type: "report"}},
					{Name: "build", Agent: v1.AgentConfig{Image: "img", Command: []string{"cmd"}}, Output: v1.OutputConfig{Type: "report"}},
				},
			},
			wantErr: "duplicate stage name: build",
		},
		{
			name: "unknown resource reference",
			spec: &v1.PipelineSpec{
				Name:      "test",
				Resources: map[string]v1.Resource{},
				Stages: []v1.StageSpec{
					{
						Name: "build",
						Agent: v1.AgentConfig{
							Image:     "img",
							Command:   []string{"cmd"},
							Resources: []string{"nonexistent"},
						},
						Output: v1.OutputConfig{Type: "report"},
					},
				},
			},
			wantErr: "unknown resource: nonexistent",
		},
		{
			name: "unknown dependency",
			spec: &v1.PipelineSpec{
				Name: "test",
				Stages: []v1.StageSpec{
					{
						Name:      "test",
						DependsOn: []string{"nonexistent"},
						Agent:     v1.AgentConfig{Image: "img", Command: []string{"cmd"}},
						Output:    v1.OutputConfig{Type: "report"},
					},
				},
			},
			wantErr: "unknown dependency: nonexistent",
		},
		{
			name: "dependency cycle",
			spec: &v1.PipelineSpec{
				Name: "test",
				Stages: []v1.StageSpec{
					{
						Name:      "a",
						DependsOn: []string{"b"},
						Agent:     v1.AgentConfig{Image: "img", Command: []string{"cmd"}},
						Output:    v1.OutputConfig{Type: "report"},
					},
					{
						Name:      "b",
						DependsOn: []string{"a"},
						Agent:     v1.AgentConfig{Image: "img", Command: []string{"cmd"}},
						Output:    v1.OutputConfig{Type: "report"},
					},
				},
			},
			wantErr: "dependency cycle detected",
		},
		{
			name: "invalid output type",
			spec: &v1.PipelineSpec{
				Name: "test",
				Stages: []v1.StageSpec{
					{
						Name:   "test",
						Agent:  v1.AgentConfig{Image: "img", Command: []string{"cmd"}},
						Output: v1.OutputConfig{Type: "invalid"},
					},
				},
			},
			wantErr: "invalid output type: invalid",
		},
		{
			name: "fan-in with no inputs",
			spec: &v1.PipelineSpec{
				Name: "test",
				Stages: []v1.StageSpec{
					{
						Name:   "merge",
						Agent:  v1.AgentConfig{Image: "img", Command: []string{"cmd"}},
						Output: v1.OutputConfig{Type: "report"},
						FanIn:  &v1.FanInConfig{Inputs: []string{}, Mode: "deterministic"},
					},
				},
			},
			wantErr: "fan-in requires at least one input",
		},
		{
			name: "fan-in with unknown input",
			spec: &v1.PipelineSpec{
				Name: "test",
				Stages: []v1.StageSpec{
					{
						Name:   "merge",
						Agent:  v1.AgentConfig{Image: "img", Command: []string{"cmd"}},
						Output: v1.OutputConfig{Type: "report"},
						FanIn:  &v1.FanInConfig{Inputs: []string{"nonexistent"}, Mode: "deterministic"},
					},
				},
			},
			wantErr: "fan-in input references unknown stage: nonexistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.spec)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
				}
			}
		})
	}
}
