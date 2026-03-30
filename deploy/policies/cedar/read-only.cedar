// All authenticated users can read
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
