package factory.authz

# All authenticated users can read
allow if {
    input.action in {"queues:read", "items:read", "workers:read", "events:stream"}
}
