// Authenticated users can read everything.
permit(
    principal,
    action in [
        Factory::Action::"queues:read",
        Factory::Action::"items:read",
        Factory::Action::"workers:read",
        Factory::Action::"events:stream"
    ],
    resource
);

// Members of the admin group can perform all write operations.
permit(
    principal,
    action in [
        Factory::Action::"enqueue",
        Factory::Action::"items:retry",
        Factory::Action::"items:cancel",
        Factory::Action::"deadletter:purge"
    ],
    resource
) when {
    context.groups.contains("konflux-hummingbird-admin-access")
};
