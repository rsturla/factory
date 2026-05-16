// Resource helper functions

import type { Resource } from "../types";

export function git(access?: "read-only" | "read-write"): Resource {
  return {
    type: "git",
    access: access || "read-only",
  };
}

export function http(url: string): Resource {
  return {
    type: "http",
    access: "read-only",
    url,
  };
}

export function s3(bucket: string, access?: "read-only" | "read-write"): Resource {
  return {
    type: "s3",
    access: access || "read-only",
    bucket,
  };
}
