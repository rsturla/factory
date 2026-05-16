// claude() async function — enqueue Claude Code stage

import { getClient } from "../internal/client";
import { runContext } from "../internal/context";
import { pollStageCompletion } from "../internal/poll";
import type { ClaudeOptions, StageResult } from "../types";

export async function claude(
  name: string,
  opts: ClaudeOptions
): Promise<StageResult> {
  const runId = await runContext.ensureRun();
  const client = getClient();

  // Smart defaults
  const image = "quay.io/hummingbird/agent-claude-code:latest";
  const command = [
    "claude-code",
    "--print",
    "--prompt-file",
    "/workspace/.prompt.md",
  ];
  const model = opts.model || "sonnet";
  const timeout = opts.timeout || "10m";
  const retry = opts.retry || 1;

  const credentials = [{ name: "anthropic", provider: "anthropic" }];

  // Create stage
  const stage = await client.createStage(runId, {
    name,
    image,
    command,
    prompt: opts.prompt,
    model,
    credentials,
    resources: opts.resources,
    environment: opts.env,
    timeout,
    retry,
    inputs: opts.inputs,
    output: opts.output,
    changeDetection: opts.changeDetection || "auto",
  });

  // Poll for completion
  const completed = await pollStageCompletion(runId, stage.id);

  if (completed.status === "failed") {
    throw new Error(
      `Stage ${name} failed with exit code ${completed.exitCode}`
    );
  }

  return {
    name: completed.name,
    output: completed.output || {},
    exitCode: completed.exitCode || 0,
    duration: completed.duration || 0,
    logs: completed.logs,
  };
}
