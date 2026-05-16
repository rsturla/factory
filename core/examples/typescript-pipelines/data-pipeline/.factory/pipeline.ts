// Data processing pipeline with dynamic parallelism
// Scenario: Process 1000s of customer data files
import { pipeline, stage, resource, git, s3 } from "@hummingbird/factory-sdk";

export default pipeline({
  name: "batch-data-processor",

  resources: {
    dataRepo: resource(git({
      access: "read-write",
      description: "Results repository"
    })),

    inputBucket: resource(s3({
      access: "read-only",
      bucket: params.input_bucket,
      description: "Raw data files"
    })),

    outputBucket: resource(s3({
      access: "write",
      bucket: params.output_bucket,
      description: "Processed results"
    })),

    schemaRegistry: resource(git({
      access: "read-only",
      url: "github.com/company/data-schemas",
      description: "Data validation schemas"
    })),
  },

  stages: ({ params, resources }) => {
    const stages = [];

    // Stage 1: Discover input files
    stages.push(
      stage("discover-files", {
        agent: {
          image: "quay.io/hummingbird/data-tools:latest",
          command: ["python", "scripts/discover.py"],
          resources: [resources.inputBucket],
          environment: {
            PREFIX: params.file_prefix || "",
            MAX_FILES: params.max_files || "1000",
            FILE_PATTERN: params.pattern || "*.json"
          }
        },
        output: {
          type: "report",
          schema: {
            files: "array",  // List of S3 keys
            total_size_bytes: "number",
            file_count: "number",
            estimated_batches: "number"
          }
        },
      })
    );

    // Stage 2: Batch files for parallel processing
    stages.push(
      stage("create-batches", {
        dependsOn: ["discover-files"],
        agent: {
          image: "quay.io/hummingbird/data-tools:latest",
          command: ["python", "scripts/batch.py"],
          environment: {
            BATCH_SIZE: params.batch_size || "100",
            FILES: "{{outputs.discover-files.files}}"
          }
        },
        output: {
          type: "report",
          schema: {
            batches: "array",  // [{batch_id, files[], size}]
            total_batches: "number"
          }
        },
      })
    );

    // Stage 3: Process batches in parallel (dynamic fan-out)
    // Number of stages determined at runtime
    const maxParallelBatches = parseInt(params.max_parallel || "20");

    // Generate processing stages dynamically
    // In real implementation, orchestrator would create N stages based on outputs.create-batches.total_batches
    for (let i = 0; i < maxParallelBatches; i++) {
      stages.push(
        stage(`process-batch-${i}`, {
          dependsOn: ["create-batches"],
          when: {
            // Only create stage if batch exists
            condition: `outputs.create-batches.total_batches > ${i}`
          },
          agent: {
            image: "quay.io/hummingbird/agent-claude-code:latest",
            command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
            prompt: "prompts/process-batch.md",
            model: "claude-sonnet-4",
            credentials: [{ name: "anthropic", provider: "anthropic" }],
            resources: [
              resources.inputBucket,
              resources.outputBucket,
              resources.schemaRegistry
            ],
            environment: {
              BATCH_ID: i.toString(),
              BATCH_FILES: `{{outputs.create-batches.batches[${i}].files}}`,
              VALIDATION_MODE: params.validation_mode || "strict"
            },
            timeout: "60m",
          },
          output: {
            type: "report",
            schema: {
              batch_id: "number",
              processed: "number",
              failed: "number",
              validation_errors: "array",
              output_files: "array",
              processing_time_seconds: "number"
            }
          },
        })
      );
    }

    const batchStageNames = Array.from(
      { length: maxParallelBatches },
      (_, i) => `process-batch-${i}`
    );

    // Stage 4: Aggregate results
    stages.push(
      stage("aggregate-results", {
        dependsOn: batchStageNames,
        fanIn: {
          inputs: batchStageNames,
          mode: "deterministic",  // Simple concatenation
        },
        agent: {
          image: "quay.io/hummingbird/data-tools:latest",
          command: ["python", "scripts/aggregate.py"],
        },
        output: {
          type: "report",
          schema: {
            total_processed: "number",
            total_failed: "number",
            success_rate: "number",
            validation_errors_by_type: "object",
            output_manifest: "array",
            total_processing_time_seconds: "number"
          }
        },
      })
    );

    // Stage 5: Quality analysis with LLM
    stages.push(
      stage("quality-analysis", {
        dependsOn: ["aggregate-results"],
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/quality-analysis.md",
          model: "claude-opus-4",
          resources: [resources.outputBucket, resources.schemaRegistry],
          environment: {
            RESULTS: "{{outputs.aggregate-results.content}}",
            SAMPLE_SIZE: "100"  // Analyze sample of outputs
          }
        },
        output: {
          type: "report",
          schema: {
            quality_score: "number",
            data_anomalies: "array",
            schema_violations: "array",
            recommendations: "array"
          }
        },
      })
    );

    // Stage 6: Generate summary report
    stages.push(
      stage("final-summary", {
        dependsOn: ["quality-analysis"],
        fanIn: {
          inputs: ["aggregate-results", "quality-analysis"],
          mode: "agent",
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/final-summary.md",
          model: "claude-sonnet-4",
          resources: [resources.dataRepo],
        },
        output: {
          type: "report",
          format: "markdown",
          schema: {
            pipeline_summary: "string",
            key_metrics: "object",
            quality_assessment: "string",
            issues_detected: "array",
            next_actions: "array"
          }
        },
      })
    );

    // Stage 7: Commit results to data repo
    stages.push(
      stage("commit-results", {
        dependsOn: ["final-summary"],
        when: {
          condition: "outputs.quality-analysis.quality_score >= 0.95"
        },
        agent: {
          image: "quay.io/hummingbird/data-tools:latest",
          command: ["./commit-results.sh"],
          resources: [resources.dataRepo],
          environment: {
            MANIFEST: "{{outputs.aggregate-results.output_manifest}}",
            SUMMARY: "{{outputs.final-summary.content}}",
            COMMIT_MESSAGE: `Data pipeline run ${params.run_id}: processed {{outputs.aggregate-results.total_processed}} files`
          }
        },
        output: {
          type: "report",
          schema: {
            commit_sha: "string",
            files_committed: "array"
          }
        },
      })
    );

    return stages;
  },
});
