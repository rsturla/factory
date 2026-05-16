package factory.authz

# SRE team can do everything
allow if {
    input.groups[_] == "sre-team"
}
