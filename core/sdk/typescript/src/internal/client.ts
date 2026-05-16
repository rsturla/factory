// HTTP client for factory-api

import { getConfig } from "./config";
import type {
  CreateRunRequest,
  CreateStageRequest,
  RunRecord,
  StageRecord,
} from "../types";

export class FactoryClient {
  private baseURL: string;

  constructor() {
    this.baseURL = getConfig().apiEndpoint;
  }

  async createRun(req: CreateRunRequest): Promise<RunRecord> {
    const response = await fetch(`${this.baseURL}/api/v1/imperative/runs`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(req),
    });

    if (!response.ok) {
      throw new Error(`Failed to create run: ${response.statusText}`);
    }

    return response.json();
  }

  async createStage(
    runId: string,
    req: CreateStageRequest
  ): Promise<StageRecord> {
    const response = await fetch(
      `${this.baseURL}/api/v1/imperative/runs/${runId}/stages`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(req),
      }
    );

    if (!response.ok) {
      throw new Error(`Failed to create stage: ${response.statusText}`);
    }

    return response.json();
  }

  async getStage(runId: string, stageId: string): Promise<StageRecord> {
    const response = await fetch(
      `${this.baseURL}/api/v1/imperative/runs/${runId}/stages/${stageId}`
    );

    if (!response.ok) {
      throw new Error(`Failed to get stage: ${response.statusText}`);
    }

    return response.json();
  }
}

// Singleton
let client: FactoryClient | null = null;

export function getClient(): FactoryClient {
  if (!client) {
    client = new FactoryClient();
  }
  return client;
}
