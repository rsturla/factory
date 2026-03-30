package factory.authz

# Echo team can enqueue and retry in the echo queue
allow if {
    input.groups[_] == "echo-team"
    input.queue == "echo"
    input.action in {"enqueue", "items:read", "items:retry"}
}
