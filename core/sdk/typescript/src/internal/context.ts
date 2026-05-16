// Runtime context for current pipeline run

import { getClient } from "./client";
import type { RunRecord } from "../types";

class RunContext {
  private currentRun: RunRecord | null = null;

  async ensureRun(): Promise<string> {
    if (!this.currentRun) {
      const client = getClient();
      this.currentRun = await client.createRun({});
    }
    return this.currentRun.id;
  }

  getRunId(): string | null {
    return this.currentRun?.id || null;
  }

  reset(): void {
    this.currentRun = null;
  }
}

export const runContext = new RunContext();
