// SRE team can do everything
permit(
    principal,
    action,
    resource
) when {
    context.groups.contains("sre-team")
};
