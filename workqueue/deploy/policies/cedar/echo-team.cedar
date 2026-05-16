// Echo team can enqueue and retry in the echo queue
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
