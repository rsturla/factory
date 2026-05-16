// Cross-repository refactoring pipeline
// Scenario: Update API across 15 microservices
import { pipeline, stage, resource, git } from "@hummingbird/factory-sdk";

export default pipeline({
  name: "multi-repo-refactor",

  resources: {
    // Dynamic resource generation from params
    ...(function() {
      const repos = JSON.parse(params.repos || "[]"); // ["api-gateway", "auth-service", ...]
      const resources: Record<string, any> = {};

      repos.forEach((repo: string) => {
        resources[`repo_${repo}`] = resource(git({
          access: "read-write",
          url: `github.com/${params.org}/${repo}`,
          ref: params.base_branch || "main"
        }));
      });

      return resources;
    })(),

    designDoc: resource(git({
      access: "read-only",
      description: "Architecture design doc repository"
    })),
  },

  stages: ({ params, resources }) => {
    const repos = JSON.parse(params.repos || "[]");
    const stages = [];

    // Stage 1: Analyze all repos in parallel
    const analyzeStages = repos.map((repo: string) =>
      stage(`analyze-${repo}`, {
        agent: {
          image: "quay.io/hummingbird/agent-claude-code:latest",
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/analyze-api-usage.md",
          model: "claude-sonnet-4",
          credentials: [{ name: "anthropic", provider: "anthropic" }],
          resources: [resources[`repo_${repo}`], resources.designDoc],
          environment: {
            REPO_NAME: repo,
            TARGET_API: params.target_api,
            NEW_API_VERSION: params.new_version
          }
        },
        output: {
          type: "report",
          schema: {
            api_calls: "array",
            migration_complexity: "number",
            breaking_changes: "array",
            dependencies: "array",
            estimated_hours: "number"
          }
        },
      })
    );

    stages.push(...analyzeStages);

    // Stage 2: Synthesize migration plan
    stages.push(
      stage("create-migration-plan", {
        dependsOn: analyzeStages.map(s => s.name),
        fanIn: {
          inputs: analyzeStages.map(s => s.name),
          mode: "agent",
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/synthesize-plan.md",
          model: "claude-opus-4", // Need best reasoning for cross-repo planning
        },
        output: {
          type: "report",
          schema: {
            migration_order: "array",  // Ordered list of repos based on dependencies
            rollback_strategy: "string",
            risk_level: "low|medium|high",
            total_estimated_hours: "number"
          }
        },
      })
    );

    // Stage 3: Refactor repos in dependency order
    // (Dynamically determined by migration plan)
    const refactorStages = repos.map((repo: string) =>
      stage(`refactor-${repo}`, {
        dependsOn: ["create-migration-plan"],
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/refactor-implementation.md",
          model: "claude-sonnet-4",
          resources: [resources[`repo_${repo}`], resources.designDoc],
          environment: {
            REPO_NAME: repo,
            TARGET_API: params.target_api,
            NEW_API_VERSION: params.new_version,
            // Pass migration plan context
            MIGRATION_PLAN: "{{outputs.create-migration-plan.content}}"
          },
          timeout: "45m",
        },
        output: {
          type: "changeset",
          schema: {
            files_modified: "array",
            files_added: "array",
            files_deleted: "array",
            api_calls_migrated: "number"
          }
        },
      })
    );

    stages.push(...refactorStages);

    // Stage 4: Cross-repo validation
    stages.push(
      stage("validate-consistency", {
        dependsOn: refactorStages.map(s => s.name),
        fanIn: {
          inputs: refactorStages.map(s => s.name),
          mode: "agent",
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/validate-consistency.md",
          model: "claude-opus-4",
          // All repos available for cross-checking
          resources: Object.values(resources).filter(r => r.type === "git"),
        },
        output: {
          type: "verification",
          schema: {
            verdict: "APPROVE|VETO|UNCERTAIN",
            consistency_checks: "object",
            api_compatibility_score: "number",
            breaking_changes_detected: "array"
          }
        },
      })
    );

    // Stage 5: Run integration tests across all services
    stages.push(
      stage("integration-tests", {
        dependsOn: ["validate-consistency"],
        when: {
          condition: "outputs.validate-consistency.verdict == 'APPROVE'"
        },
        agent: {
          image: "quay.io/hummingbird/integration-test:latest",
          command: ["./run-cross-service-tests.sh"],
          // All refactored repos available for testing
          resources: Object.values(resources).filter(r => r.type === "git"),
          timeout: "60m",
        },
        output: {
          type: "report",
          schema: {
            tests_passed: "number",
            tests_failed: "number",
            services_tested: "array",
            failure_details: "array"
          }
        },
      })
    );

    // Stage 6: Create PRs for each repo (if tests pass)
    const prStages = repos.map((repo: string) =>
      stage(`pr-${repo}`, {
        dependsOn: ["integration-tests"],
        when: {
          condition: "outputs.integration-tests.tests_failed == 0"
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/pr-description.md",
          resources: [resources[`repo_${repo}`]],
          environment: {
            REPO_NAME: repo,
            MIGRATION_CONTEXT: "{{outputs.create-migration-plan.content}}",
            TEST_RESULTS: "{{outputs.integration-tests.content}}"
          }
        },
        output: {
          type: "pr",
          branchPrefix: `factory/migrate-${params.new_version}-`,
          labels: ["refactor", "api-migration", params.new_version],
          reviewers: ["platform-team", "api-owners"],
        },
      })
    );

    stages.push(...prStages);

    return stages;
  },
});
