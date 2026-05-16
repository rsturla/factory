// run() async function — custom agent execution

import { getClient } from "../internal/client";
import { runContext } from "../internal/context";
import { pollStageCompletion } from "../internal/poll";
import type { RunOptions, StageResult } from "../types";

export async function run(
  name: string,
  opts: RunOptions
): Promise<StageResult> {
  const runId = await runContext.ensureRun();
  const client = getClient();

  const timeout = opts.timeout || "10m";
  const retry = opts.retry || 1;

  // Create stage
  const stage = await client.createStage(runId, {
    name,
    image: opts.image,
    command: opts.command,
    resources: opts.resources,
    environment: opts.env,
    timeout,
    retry,
    inputs: opts.inputs,
    changeDetection: opts.changeDetection || "auto",
  });

  // Poll for completion
  const completed = await pollStageCompletion(runId, stage.id);

  if (completed.status === "failed") {
    throw new Error(
      `Run ${name} failed with exit code ${completed.exitCode}`
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
