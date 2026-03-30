// SRE team can do everything
permit(
    principal,
    action,
    resource
) when {
    context.groups.contains("sre-team")
};

// Everyone can read
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

// RPM team can enqueue and retry in their queue
permit(
    principal,
    action in [
        Factory::Action::"enqueue",
        Factory::Action::"items:read",
        Factory::Action::"items:retry"
    ],
    resource == Factory::Queue::"echo"
) when {
    context.groups.contains("echo-team")
};
