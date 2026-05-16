package controller

import (
	"fmt"
	"time"
)

// JobTemplate generates K8s Job manifest for pipeline execution.
func (c *PipelineController) generateJobManifest(pipeline *Pipeline, params map[string]string) string {
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	jobName := fmt.Sprintf("%s-%s", pipeline.Name, runID)

	// Build env vars from params
	envVars := ""
	for key, value := range params {
		envVars += fmt.Sprintf(`        - name: %s
          value: "%s"
`, key, value)
	}

	manifest := fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  labels:
    pipeline: %s
    run-id: "%s"
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        pipeline: %s
    spec:
      restartPolicy: Never
      initContainers:
      - name: git-clone
        image: bitnami/git:latest
        command:
        - sh
        - -c
        - |
          git clone --depth=1 --branch %s %s /workspace
        volumeMounts:
        - name: workspace
          mountPath: /workspace
      containers:
      - name: pipeline
        image: oven/bun:latest
        workingDir: /workspace/%s
        command: ["bun", "run", "pipeline.ts"]
        env:
        - name: FACTORY_API_ENDPOINT
          value: "http://factory-api:8080"
%s
        volumeMounts:
        - name: workspace
          mountPath: /workspace
      volumes:
      - name: workspace
        emptyDir: {}
`,
		jobName,
		pipeline.Name,
		runID,
		pipeline.Name,
		pipeline.Branch,
		pipeline.RepoURL,
		pipeline.Path,
		envVars,
	)

	return manifest
}
