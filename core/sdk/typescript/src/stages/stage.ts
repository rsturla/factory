// Low-level stage builder — for custom stage types

import { getClient } from "../internal/client";
import { runContext } from "../internal/context";
import { pollStageCompletion } from "../internal/poll";
import type { CreateStageRequest, StageResult } from "../types";

/**
 * Low-level stage execution.
 * Use this to build custom stage types beyond claude/judge/run.
 *
 * @example
 * ```typescript
 * // Custom GPT-4 stage
 * export async function gpt4(name: string, opts: {...}) {
 *   return stage(name, {
 *     image: "quay.io/example/gpt4-agent:latest",
 *     command: ["gpt4-cli", "--prompt-file", "/workspace/.prompt.md"],
 *     prompt: opts.prompt,
 *     model: opts.model || "gpt-4-turbo",
 *     ...
 *   });
 * }
 * ```
 */
export async function stage(
  name: string,
  req: CreateStageRequest
): Promise<StageResult> {
  const runId = await runContext.ensureRun();
  const client = getClient();

  const created = await client.createStage(runId, req);
  const completed = await pollStageCompletion(runId, created.id);

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
