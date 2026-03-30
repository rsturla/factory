package factory.authz

default allow = false

# SRE team can do everything
allow if {
    input.groups[_] == "sre-team"
}

# Everyone can read
allow if {
    input.action in {"queues:read", "items:read", "workers:read", "events:stream"}
}

# Echo team can enqueue and retry in the echo queue
allow if {
    input.groups[_] == "echo-team"
    input.queue == "echo"
    input.action in {"enqueue", "items:read", "items:retry"}
}
