// Trigger definitions for pipeline deployment

export interface TriggerDefinition {
  type: string;
  [key: string]: any;
}

export interface JiraTriggerOptions {
  query: string;
  poll: string;
  params?: (issue: any) => Record<string, string>;
}

export interface WebhookTriggerOptions {
  path: string;
  secret?: string;
  params?: (payload: any) => Record<string, string>;
}

export interface ScheduleTriggerOptions {
  cron: string;
  params?: () => Record<string, string>;
}

export interface GitHubTriggerOptions {
  event: string;
  filter?: (payload: any) => boolean;
  params?: (payload: any) => Record<string, string>;
}

export interface ManualTriggerOptions {
  params?: () => Record<string, string>;
}

/**
 * Trigger builders for pipeline deployment.
 *
 * Triggers define when and how pipelines execute in deployed environments.
 *
 * @example
 * ```typescript
 * // Jira trigger
 * export const trigger = triggers.jira({
 *   query: 'project = HUM AND labels = cve-needs-attention',
 *   poll: '5m',
 *   params: (issue) => ({
 *     CVE_ID: issue.customFields.cve_id,
 *     ISSUE_KEY: issue.key,
 *   }),
 * });
 *
 * // Multiple triggers
 * export const triggers = [
 *   triggers.jira({ query: '...', poll: '5m' }),
 *   triggers.webhook({ path: '/webhook/github', secret: 'secret' }),
 *   triggers.schedule({ cron: '0 * * * *' }),
 * ];
 * ```
 */
export const triggers = {
  /**
   * Jira trigger - poll Jira API for matching issues.
   *
   * @example
   * ```typescript
   * triggers.jira({
   *   query: 'project = HUM AND labels = cve-needs-attention AND status != Closed',
   *   poll: '5m',
   *   params: (issue) => ({
   *     CVE_ID: issue.customFields.cve_id,
   *     SEVERITY: issue.priority.name.toLowerCase(),
   *     ISSUE_KEY: issue.key,
   *   }),
   * })
   * ```
   */
  jira: (opts: JiraTriggerOptions): TriggerDefinition => ({
    type: "jira",
    query: opts.query,
    poll: opts.poll,
    params: opts.params ? opts.params.toString() : undefined,
  }),

  /**
   * Webhook trigger - HTTP endpoint for external systems.
   *
   * @example
   * ```typescript
   * triggers.webhook({
   *   path: '/webhook/github',
   *   secret: 'webhook-secret',
   *   params: (payload) => ({
   *     PR_NUMBER: payload.pull_request.number,
   *     REPO: payload.repository.full_name,
   *     ACTION: payload.action,
   *   }),
   * })
   * ```
   */
  webhook: (opts: WebhookTriggerOptions): TriggerDefinition => ({
    type: "webhook",
    path: opts.path,
    secret: opts.secret,
    params: opts.params ? opts.params.toString() : undefined,
  }),

  /**
   * Schedule trigger - cron-based execution.
   *
   * @example
   * ```typescript
   * triggers.schedule({
   *   cron: '0 */6 * * *',  // Every 6 hours
   *   params: () => ({
   *     RUN_ID: Date.now().toString(),
   *   }),
   * })
   * ```
   */
  schedule: (opts: ScheduleTriggerOptions): TriggerDefinition => ({
    type: "schedule",
    cron: opts.cron,
    params: opts.params ? opts.params.toString() : undefined,
  }),

  /**
   * GitHub trigger - GitHub webhook events.
   *
   * @example
   * ```typescript
   * triggers.github({
   *   event: 'pull_request',
   *   filter: (payload) => payload.action === 'opened',
   *   params: (payload) => ({
   *     PR_NUMBER: payload.pull_request.number,
   *     REPO: payload.repository.full_name,
   *     SHA: payload.pull_request.head.sha,
   *   }),
   * })
   * ```
   */
  github: (opts: GitHubTriggerOptions): TriggerDefinition => ({
    type: "github",
    event: opts.event,
    filter: opts.filter ? opts.filter.toString() : undefined,
    params: opts.params ? opts.params.toString() : undefined,
  }),

  /**
   * Manual trigger - user-initiated execution.
   *
   * @example
   * ```typescript
   * triggers.manual({
   *   params: () => ({
   *     // Params provided by user at trigger time
   *   }),
   * })
   * ```
   */
  manual: (opts?: ManualTriggerOptions): TriggerDefinition => ({
    type: "manual",
    params: opts?.params ? opts.params.toString() : undefined,
  }),
};
