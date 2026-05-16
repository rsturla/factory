// Dry-run mode for trigger extraction

/**
 * Check if running in dry-run mode.
 * In dry-run, stages don't execute — only trigger exports are evaluated.
 */
export function isDryRun(): boolean {
  return process.argv.includes("--dry-run");
}

/**
 * Exit dry-run with trigger definitions.
 * Controller calls pipeline with --dry-run, pipeline exports triggers,
 * this function serializes them to stdout and exits.
 *
 * @example
 * ```typescript
 * import { triggers, exitDryRun, isDryRun } from "@hummingbird/factory-sdk";
 *
 * export const trigger = triggers.jira({
 *   query: 'project = HUM AND labels = cve-needs-attention',
 *   poll: '5m',
 * });
 *
 * if (isDryRun()) {
 *   exitDryRun(trigger);
 * }
 *
 * // Pipeline logic (only runs if not dry-run)
 * const result = await claude("analyze", { ... });
 * ```
 */
export function exitDryRun(
  triggers: any | any[]
): never {
  const triggerArray = Array.isArray(triggers) ? triggers : [triggers];
  console.log(JSON.stringify(triggerArray, null, 2));
  process.exit(0);
}
