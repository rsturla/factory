// Output action helpers

import type { OutputAction } from "../types";

export interface PROptions {
  branch: string;
  labels?: string[];
  reviewers?: string[];
  draft?: boolean;
}

export function pr(opts: PROptions): OutputAction {
  return {
    type: "pr",
    branch: opts.branch,
    labels: opts.labels,
    reviewers: opts.reviewers,
    draft: opts.draft,
  };
}
