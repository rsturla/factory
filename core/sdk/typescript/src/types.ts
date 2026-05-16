// TypeScript types for imperative Factory SDK

export interface StageResult {
  name: string;
  output: Record<string, any>;
  exitCode: number;
  duration: number;
  logs?: string;
}

export interface JudgmentResult extends StageResult {
  verdict: "APPROVE" | "VETO" | "UNCERTAIN";
  reasoning: string;
  criteria: Record<string, CriterionResult>;
}

export interface CriterionResult {
  passed: boolean;
  message?: string;
}

export interface Resource {
  type: "git" | "http" | "s3";
  access?: "read-only" | "read-write";
  url?: string;
  bucket?: string;
  ref?: string;
  description?: string;
}

export interface OutputAction {
  type: "pr" | "review" | "report" | "patch" | "changeset";
  branch?: string;
  labels?: string[];
  reviewers?: string[];
  draft?: boolean;
}

export interface ClaudeOptions {
  prompt: string;
  model?: "sonnet" | "opus" | "haiku";
  resources?: Resource[];
  env?: Record<string, string>;
  timeout?: string;
  retry?: number;
  inputs?: Record<string, any>;
  output?: OutputAction;
  changeDetection?: "git" | "explicit" | "auto"; // Default: "auto"
}

export interface JudgeOptions extends Omit<ClaudeOptions, "model"> {
  model?: "opus" | "sonnet";
}

export interface RunOptions {
  image: string;
  command: string[];
  resources?: Resource[];
  env?: Record<string, string>;
  timeout?: string;
  retry?: number;
  inputs?: Record<string, any>;
  changeDetection?: "git" | "explicit" | "auto";
}

// Internal SDK types

export interface StageRecord {
  id: string;
  runId: string;
  name: string;
  status: "pending" | "running" | "completed" | "failed";
  output?: Record<string, any>;
  exitCode?: number;
  duration?: number;
  logs?: string;
  createdAt: string;
  updatedAt: string;
}

export interface CreateStageRequest {
  name: string;
  image: string;
  command: string[];
  prompt?: string;
  model?: string;
  credentials?: CredentialBinding[];
  resources?: Resource[];
  environment?: Record<string, string>;
  timeout?: string;
  retry?: number;
  inputs?: Record<string, any>;
  output?: OutputAction;
  changeDetection?: "git" | "explicit" | "auto";
}

export interface CredentialBinding {
  name: string;
  provider: string;
}

export interface RunRecord {
  id: string;
  status: "pending" | "running" | "completed" | "failed";
  createdAt: string;
  updatedAt: string;
}

export interface CreateRunRequest {
  name?: string;
}
