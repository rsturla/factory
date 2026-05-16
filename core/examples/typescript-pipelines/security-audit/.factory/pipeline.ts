// Comprehensive security audit with multiple specialized judges
import { pipeline, stage, resource, git } from "@hummingbird/factory-sdk";

export default pipeline({
  name: "security-audit",

  resources: {
    targetRepo: resource(git({
      access: "read-only",
      description: "Repository to audit"
    })),

    securityPolicies: resource(git({
      access: "read-only",
      url: "github.com/company/security-policies",
      description: "Company security standards"
    })),

    previousAudits: resource(git({
      access: "read-only",
      url: "github.com/company/audit-reports",
      description: "Historical audit reports"
    })),
  },

  stages: ({ params, resources }) => {
    const stages = [];

    // Parallel specialized scans
    const scanners = [
      {
        name: "secrets-scan",
        prompt: "prompts/scan-secrets.md",
        focus: "credentials, API keys, tokens, private keys",
        severity: "critical"
      },
      {
        name: "injection-scan",
        prompt: "prompts/scan-injection.md",
        focus: "SQL injection, XSS, command injection, path traversal",
        severity: "high"
      },
      {
        name: "auth-scan",
        prompt: "prompts/scan-auth.md",
        focus: "authentication, authorization, session management",
        severity: "high"
      },
      {
        name: "crypto-scan",
        prompt: "prompts/scan-crypto.md",
        focus: "encryption algorithms, key management, TLS configuration",
        severity: "high"
      },
      {
        name: "dependency-scan",
        prompt: "prompts/scan-dependencies.md",
        focus: "vulnerable dependencies, license compliance, supply chain",
        severity: "medium"
      },
      {
        name: "config-scan",
        prompt: "prompts/scan-config.md",
        focus: "security misconfigurations, CORS, CSP, rate limiting",
        severity: "medium"
      },
      {
        name: "data-scan",
        prompt: "prompts/scan-data.md",
        focus: "PII handling, data retention, encryption at rest",
        severity: "medium"
      },
    ];

    // Create parallel scan stages
    scanners.forEach(scanner => {
      stages.push(
        stage(scanner.name, {
          agent: {
            image: "quay.io/hummingbird/agent-claude-code:latest",
            command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
            prompt: scanner.prompt,
            model: scanner.severity === "critical" ? "claude-opus-4" : "claude-sonnet-4",
            credentials: [{ name: "anthropic", provider: "anthropic" }],
            resources: [resources.targetRepo, resources.securityPolicies],
            environment: {
              SCAN_FOCUS: scanner.focus,
              SEVERITY_LEVEL: scanner.severity
            },
            timeout: "30m",
          },
          output: {
            type: "report",
            schema: {
              findings: "array",  // [{file, line, severity, description, cwe_id}]
              severity_counts: "object",
              false_positive_likelihood: "number",
              remediation_priority: "array"
            }
          },
        })
      );
    });

    const scanStageNames = scanners.map(s => s.name);

    // Aggregate all findings
    stages.push(
      stage("aggregate-findings", {
        dependsOn: scanStageNames,
        fanIn: {
          inputs: scanStageNames,
          mode: "agent",
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/aggregate-findings.md",
          model: "claude-opus-4",
          resources: [resources.targetRepo, resources.previousAudits],
        },
        output: {
          type: "report",
          schema: {
            total_findings: "number",
            critical: "number",
            high: "number",
            medium: "number",
            low: "number",
            deduped_findings: "array",
            new_vs_previous: "object",
            risk_score: "number"  // 0-100
          }
        },
      })
    );

    // Multiple LLM judges for critical findings
    // (Bias mitigation via multiple independent reviews)
    if (params.enable_multi_judge !== "false") {
      const judgeModels = ["claude-opus-4", "claude-sonnet-4"];

      judgeModels.forEach((model, idx) => {
        stages.push(
          stage(`judge-${idx + 1}`, {
            dependsOn: ["aggregate-findings"],
            when: {
              condition: "outputs.aggregate-findings.critical > 0"
            },
            agent: {
              command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
              prompt: "prompts/security-judge.md",
              model: model,
              resources: [resources.targetRepo, resources.securityPolicies],
              environment: {
                JUDGE_ID: `judge-${idx + 1}`,
                FINDINGS: "{{outputs.aggregate-findings.deduped_findings}}",
                // Each judge gets different prompt variations to reduce bias correlation
                PROMPT_VARIATION: idx.toString()
              }
            },
            output: {
              type: "verification",
              schema: {
                verdict: "APPROVE|VETO|UNCERTAIN",
                reasoning: "string",
                false_positives_identified: "array",
                severity_adjustments: "array",
                additional_concerns: "array"
              }
            },
          })
        );
      });

      // Calibration stage - reconcile judge disagreements
      stages.push(
        stage("calibrate-judgments", {
          dependsOn: judgeModels.map((_, idx) => `judge-${idx + 1}`),
          fanIn: {
            inputs: judgeModels.map((_, idx) => `judge-${idx + 1}`),
            mode: "agent",
          },
          agent: {
            command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
            prompt: "prompts/calibrate-judges.md",
            model: "claude-opus-4",
          },
          output: {
            type: "verification",
            schema: {
              verdict: "APPROVE|VETO|UNCERTAIN",
              consensus_level: "unanimous|majority|split",
              final_severity: "string",
              judge_disagreements: "array",
              recommended_action: "string"
            }
          },
        })
      );
    }

    // Generate fixes for auto-fixable issues
    stages.push(
      stage("generate-fixes", {
        dependsOn: params.enable_multi_judge !== "false"
          ? ["calibrate-judgments"]
          : ["aggregate-findings"],
        when: {
          condition: params.enable_multi_judge !== "false"
            ? "outputs.calibrate-judgments.verdict == 'VETO'"
            : "outputs.aggregate-findings.critical > 0"
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/generate-fixes.md",
          model: "claude-opus-4",
          resources: [resources.targetRepo, resources.securityPolicies],
          environment: {
            FINDINGS: params.enable_multi_judge !== "false"
              ? "{{outputs.calibrate-judgments.content}}"
              : "{{outputs.aggregate-findings.deduped_findings}}",
            AUTO_FIX_ONLY: "true"  // Only trivial fixes
          },
          timeout: "60m",
        },
        output: {
          type: "changeset",
          schema: {
            fixes_applied: "array",
            manual_fixes_required: "array",
            verification_needed: "array"
          }
        },
      })
    );

    // Verify fixes don't introduce new issues (adversarial testing)
    stages.push(
      stage("adversarial-review", {
        dependsOn: ["generate-fixes"],
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/adversarial-review.md",
          model: "claude-opus-4",
          resources: [resources.targetRepo],
          environment: {
            ROLE: "attacker",  // Red team mindset
            FIXES_APPLIED: "{{outputs.generate-fixes.fixes_applied}}"
          }
        },
        output: {
          type: "verification",
          schema: {
            verdict: "APPROVE|VETO|UNCERTAIN",
            new_vulnerabilities: "array",
            bypass_attempts: "array",
            security_regression: "boolean"
          }
        },
      })
    );

    // Create comprehensive report
    stages.push(
      stage("final-report", {
        dependsOn: ["adversarial-review"],
        fanIn: {
          inputs: [...scanStageNames, "aggregate-findings", "generate-fixes", "adversarial-review"],
          mode: "agent",
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/final-report.md",
          model: "claude-opus-4",
          resources: [resources.targetRepo, resources.previousAudits],
        },
        output: {
          type: "report",
          format: "markdown",
          schema: {
            executive_summary: "string",
            risk_score: "number",
            critical_findings: "array",
            fixes_applied: "array",
            manual_action_required: "array",
            comparison_to_previous: "object",
            compliance_status: "object",
            recommended_next_steps: "array"
          }
        },
      })
    );

    // Create PR with fixes (if safe)
    stages.push(
      stage("create-fix-pr", {
        dependsOn: ["final-report"],
        when: {
          condition: "outputs.adversarial-review.verdict == 'APPROVE' && outputs.generate-fixes.fixes_applied.length > 0"
        },
        agent: {
          command: ["claude-code", "--print", "--prompt-file", "/workspace/.prompt.md"],
          prompt: "prompts/pr-description.md",
          resources: [resources.targetRepo],
        },
        output: {
          type: "pr",
          branchPrefix: "factory/security-fixes-",
          labels: ["security", "automated-fix", "audit"],
          reviewers: ["security-team", "codeowners"],
          draft: params.severity === "critical" ? false : true,
        },
      })
    );

    return stages;
  },
});
