// judge() async function — LLM verification with typed verdict

import { getClient } from "../internal/client";
import { runContext } from "../internal/context";
import { pollStageCompletion } from "../internal/poll";
import type { JudgeOptions, JudgmentResult } from "../types";

export async function judge(
  name: string,
  opts: JudgeOptions
): Promise<JudgmentResult> {
  const runId = await runContext.ensureRun();
  const client = getClient();

  // Smart defaults (opus for judges)
  const image = "quay.io/hummingbird/agent-claude-code:latest";
  const command = [
    "claude-code",
    "--print",
    "--prompt-file",
    "/workspace/.prompt.md",
  ];
  const model = opts.model || "opus";
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
      `Judge ${name} failed with exit code ${completed.exitCode}`
    );
  }

  const output = completed.output || {};

  return {
    name: completed.name,
    output,
    exitCode: completed.exitCode || 0,
    duration: completed.duration || 0,
    logs: completed.logs,
    verdict: output.verdict || "UNCERTAIN",
    reasoning: output.reasoning || "",
    criteria: output.criteria || {},
  };
}
