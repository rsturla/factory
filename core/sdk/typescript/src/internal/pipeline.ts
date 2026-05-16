import { PipelineSpec, StageSpec, Resource, StageDefinition } from "../types";

export interface PipelineOptions {
  name: string;
  resources?: Record<string, Resource>;
  stages: StageSpec[] | StageDefinition;
}

/**
 * Define a pipeline with typed configuration.
 *
 * @example
 * ```typescript
 * export default pipeline({
 *   name: "code-review",
 *   resources: {
 *     targetRepo: resource(git({ access: "read-write" }))
 *   },
 *   stages: ({ params, resources }) => [
 *     stage("review", {
 *       agent: { ... },
 *       output: { type: "review" }
 *     })
 *   ]
 * });
 * ```
 */
export function pipeline(opts: PipelineOptions): PipelineSpec {
  const spec: PipelineSpec = {
    name: opts.name,
    resources: opts.resources,
    stages: []
  };

  if (typeof opts.stages === "function") {
    // Dynamic stages - called with empty params/resources
    // Real params/resources resolved at runtime
    spec.stages = opts.stages({ params: {}, resources: {} });
  } else {
    spec.stages = opts.stages;
  }

  return spec;
}
