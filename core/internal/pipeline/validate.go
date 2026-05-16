package pipeline

import (
	"fmt"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

// Validate checks a pipeline spec for common errors.
func Validate(spec *v1.PipelineSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("pipeline name is required")
	}

	if len(spec.Stages) == 0 {
		return fmt.Errorf("pipeline must have at least one stage")
	}

	stageNames := make(map[string]bool)
	for _, stage := range spec.Stages {
		// Check for duplicate stage names
		if stageNames[stage.Name] {
			return fmt.Errorf("duplicate stage name: %s", stage.Name)
		}
		stageNames[stage.Name] = true

		// Validate stage
		if err := validateStage(stage, spec); err != nil {
			return fmt.Errorf("stage %s: %w", stage.Name, err)
		}
	}

	// Check for dependency cycles
	if err := checkCycles(spec); err != nil {
		return err
	}

	return nil
}

func validateStage(stage v1.StageSpec, spec *v1.PipelineSpec) error {
	if stage.Name == "" {
		return fmt.Errorf("stage name is required")
	}

	// Validate agent config
	if stage.Agent.Image == "" {
		return fmt.Errorf("agent image is required")
	}
	if len(stage.Agent.Command) == 0 {
		return fmt.Errorf("agent command is required")
	}

	// Validate resource references
	for _, resName := range stage.Agent.Resources {
		if _, exists := spec.Resources[resName]; !exists {
			return fmt.Errorf("unknown resource: %s", resName)
		}
	}

	// Validate dependencies
	for _, dep := range stage.DependsOn {
		found := false
		for _, s := range spec.Stages {
			if s.Name == dep {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown dependency: %s", dep)
		}
	}

	// Validate fan-in
	if stage.FanIn != nil {
		if len(stage.FanIn.Inputs) == 0 {
			return fmt.Errorf("fan-in requires at least one input")
		}
		if stage.FanIn.Mode != "deterministic" && stage.FanIn.Mode != "agent" {
			return fmt.Errorf("fan-in mode must be 'deterministic' or 'agent'")
		}
		// Verify fan-in inputs reference valid stages
		for _, input := range stage.FanIn.Inputs {
			found := false
			for _, s := range spec.Stages {
				if s.Name == input {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("fan-in input references unknown stage: %s", input)
			}
		}
	}

	// Validate output type
	validOutputTypes := map[string]bool{
		"pr": true, "review": true, "report": true,
		"patch": true, "changeset": true, "custom": true,
	}
	if !validOutputTypes[stage.Output.Type] {
		return fmt.Errorf("invalid output type: %s (must be pr, review, report, patch, changeset, or custom)", stage.Output.Type)
	}

	return nil
}

// checkCycles detects dependency cycles using DFS.
func checkCycles(spec *v1.PipelineSpec) error {
	graph := make(map[string][]string)
	for _, stage := range spec.Stages {
		graph[stage.Name] = stage.DependsOn
	}

	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var dfs func(string) error
	dfs = func(node string) error {
		visited[node] = true
		recStack[node] = true

		for _, neighbor := range graph[node] {
			if !visited[neighbor] {
				if err := dfs(neighbor); err != nil {
					return err
				}
			} else if recStack[neighbor] {
				return fmt.Errorf("dependency cycle detected involving stage: %s", node)
			}
		}

		recStack[node] = false
		return nil
	}

	for _, stage := range spec.Stages {
		if !visited[stage.Name] {
			if err := dfs(stage.Name); err != nil {
				return err
			}
		}
	}

	return nil
}
