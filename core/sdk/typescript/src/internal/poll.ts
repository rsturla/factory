// Stage completion polling with adaptive backoff

import { getClient } from "./client";
import { getConfig } from "./config";
import type { StageRecord } from "../types";

export async function pollStageCompletion(
  runId: string,
  stageId: string
): Promise<StageRecord> {
  const client = getClient();
  const config = getConfig();

  let interval = config.pollIntervalMs;
  const startTime = Date.now();
  const maxPollDuration = 24 * 60 * 60 * 1000; // 24 hours maximum

  while (true) {
    // Check timeout
    if (Date.now() - startTime > maxPollDuration) {
      throw new Error(
        `Stage ${stageId} polling timeout after ${maxPollDuration / 1000}s`
      );
    }

    const stage = await client.getStage(runId, stageId);

    if (stage.status === "completed" || stage.status === "failed") {
      return stage;
    }

    await sleep(interval);

    // Adaptive backoff
    interval = Math.min(
      interval * config.pollBackoffMultiplier,
      config.maxPollIntervalMs
    );
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
