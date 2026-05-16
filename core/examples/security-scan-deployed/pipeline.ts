// Security scan with multiple triggers
import {
  claude,
  judge,
  git,
  pr,
  triggers,
  isDryRun,
  exitDryRun,
} from "@hummingbird/factory-sdk";

// Multiple trigger types
export const pipelineTriggers = [
  // Schedule: run every 6 hours
  triggers.schedule({
    cron: '0 */6 * * *',
    params: () => ({
      RUN_ID: Date.now().toString(),
      TRIGGER: 'scheduled',
    }),
  }),

  // GitHub: new PR with 'security-review' label
  triggers.github({
    event: 'pull_request',
    filter: (payload) =>
      payload.action === 'labeled' &&
      payload.label.name === 'security-review',
    params: (payload) => ({
      PR_NUMBER: payload.pull_request.number.toString(),
      REPO: payload.repository.full_name,
      SHA: payload.pull_request.head.sha,
      TRIGGER: 'github-pr',
    }),
  }),

  // Manual: triggered via API
  triggers.manual({
    params: () => ({
      TRIGGER: 'manual',
    }),
  }),
];

if (isDryRun()) {
  exitDryRun(pipelineTriggers);
}

// Pipeline logic
const repo = git("read-write");
const policies = git();

console.log(`Starting security scan (trigger: ${process.env.TRIGGER})`);

// Parallel security scans
const scanners = [
  { name: "secrets", prompt: "prompts/scan-secrets.md", model: "opus" as const },
  { name: "injection", prompt: "prompts/scan-injection.md", model: "sonnet" as const },
  { name: "auth", prompt: "prompts/scan-auth.md", model: "sonnet" as const },
  { name: "crypto", prompt: "prompts/scan-crypto.md", model: "sonnet" as const },
];

const scans = await Promise.all(
  scanners.map((s) =>
    claude(`scan-${s.name}`, {
      prompt: s.prompt,
      model: s.model,
      resources: [repo, policies],
      env: { SCAN_FOCUS: s.name },
      timeout: "30m",
    })
  )
);

// Aggregate findings
const scanOutputs = Object.fromEntries(
  scans.map((s, i) => [scanners[i].name, s.output])
);

const aggregate = await claude("aggregate", {
  prompt: "prompts/aggregate.md",
  model: "opus",
  resources: [repo],
  inputs: scanOutputs,
});

console.log(`Found ${aggregate.output.critical} critical findings`);

if (aggregate.output.critical === 0) {
  console.log("No critical findings");
  process.exit(0);
}

// LLM judge review
const review = await judge("security-review", {
  prompt: "prompts/judge.md",
  resources: [repo],
  inputs: { findings: aggregate.output },
});

if (review.verdict !== "VETO") {
  console.log("No immediate action needed");
  process.exit(0);
}

// Generate fixes
const fixes = await claude("generate-fixes", {
  prompt: "prompts/fixes.md",
  model: "opus",
  resources: [repo, policies],
  inputs: { findings: aggregate.output },
  timeout: "60m",
});

// Create PR or GitHub comment depending on trigger
if (process.env.TRIGGER === 'github-pr') {
  // Comment on existing PR
  await claude("post-review-comment", {
    prompt: "prompts/pr-comment.md",
    resources: [repo],
    inputs: { findings: aggregate.output, fixes: fixes.output },
    env: {
      PR_NUMBER: process.env.PR_NUMBER,
    },
  });
  console.log(`Posted review on PR #${process.env.PR_NUMBER}`);
} else {
  // Create new PR with fixes
  await claude("create-pr", {
    prompt: "prompts/pr.md",
    resources: [repo],
    inputs: { findings: aggregate.output, fixes: fixes.output },
    output: pr({
      branch: `factory/security-fixes-${process.env.RUN_ID}`,
      labels: ["security", "automated-fix"],
      reviewers: ["security-team"],
      draft: false,
    }),
  });
  console.log("Created PR with security fixes");
}
