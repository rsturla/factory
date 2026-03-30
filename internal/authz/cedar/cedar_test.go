package cedar_test

import (
	"context"
	"testing"

	"github.com/hummingbird-org/factory/internal/authz"
	cedarauthorizer "github.com/hummingbird-org/factory/internal/authz/cedar"
)

const testPolicy = `
// SRE team can do everything
permit(
    principal,
    action,
    resource
) when {
    context.groups.contains("sre-team")
};

// Everyone can read queues and items
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
    resource == Factory::Queue::"rpm-update"
) when {
    context.groups.contains("rpm-team")
};
`

func newAuthorizer(t *testing.T) *cedarauthorizer.Authorizer {
	t.Helper()
	a, err := cedarauthorizer.NewFromBytes("test.cedar", []byte(testPolicy))
	if err != nil {
		t.Fatalf("NewFromBytes: %v", err)
	}
	return a
}

func TestSREAllowedEverything(t *testing.T) {
	a := newAuthorizer(t)
	ctx := context.Background()

	for _, action := range []authz.Action{
		authz.ActionQueuesRead, authz.ActionItemsRetry,
		authz.ActionDeadLetterPurge, authz.ActionEnqueue,
	} {
		d := a.Authorize(ctx, authz.Request{
			User: "alice", Groups: []string{"sre-team"}, Action: action, Queue: "any-queue",
		})
		if !d.Allowed {
			t.Errorf("SRE should be allowed %s: %s", action, d.Reason)
		}
	}
}

func TestReadAllowed(t *testing.T) {
	a := newAuthorizer(t)
	ctx := context.Background()

	for _, action := range []authz.Action{
		authz.ActionQueuesRead, authz.ActionItemsRead,
		authz.ActionWorkersRead, authz.ActionEventsStream,
	} {
		d := a.Authorize(ctx, authz.Request{
			User: "bob", Groups: []string{"random-team"}, Action: action, Queue: "any-queue",
		})
		if !d.Allowed {
			t.Errorf("read action %s should be allowed for any user: %s", action, d.Reason)
		}
	}
}

func TestWriteDeniedForNonSRE(t *testing.T) {
	a := newAuthorizer(t)
	ctx := context.Background()

	for _, action := range []authz.Action{
		authz.ActionItemsCancel, authz.ActionDeadLetterPurge,
	} {
		d := a.Authorize(ctx, authz.Request{
			User: "bob", Groups: []string{"random-team"}, Action: action, Queue: "rpm-update",
		})
		if d.Allowed {
			t.Errorf("write action %s should be denied for non-SRE", action)
		}
	}
}

func TestRPMTeamQueueScoped(t *testing.T) {
	a := newAuthorizer(t)
	ctx := context.Background()

	// Allowed: rpm-team enqueue to rpm-update
	d := a.Authorize(ctx, authz.Request{
		User: "carol", Groups: []string{"rpm-team"}, Action: authz.ActionEnqueue, Queue: "rpm-update",
	})
	if !d.Allowed {
		t.Errorf("rpm-team should be allowed to enqueue to rpm-update: %s", d.Reason)
	}

	// Denied: rpm-team enqueue to codegen
	d = a.Authorize(ctx, authz.Request{
		User: "carol", Groups: []string{"rpm-team"}, Action: authz.ActionEnqueue, Queue: "codegen",
	})
	if d.Allowed {
		t.Error("rpm-team should be denied enqueue to codegen")
	}

	// Allowed: rpm-team retry in rpm-update
	d = a.Authorize(ctx, authz.Request{
		User: "carol", Groups: []string{"rpm-team"}, Action: authz.ActionItemsRetry, Queue: "rpm-update",
	})
	if !d.Allowed {
		t.Errorf("rpm-team should be allowed to retry in rpm-update: %s", d.Reason)
	}
}

func TestUnauthenticatedDenied(t *testing.T) {
	a := newAuthorizer(t)
	d := a.Authorize(context.Background(), authz.Request{
		User: "", Action: authz.ActionQueuesRead,
	})
	if d.Allowed {
		t.Error("unauthenticated should be denied")
	}
}

func TestMultipleGroups(t *testing.T) {
	a := newAuthorizer(t)
	ctx := context.Background()

	// User in both sre-team and rpm-team — SRE rule should match first.
	d := a.Authorize(ctx, authz.Request{
		User: "eve", Groups: []string{"rpm-team", "sre-team"},
		Action: authz.ActionDeadLetterPurge, Queue: "codegen",
	})
	if !d.Allowed {
		t.Errorf("user in sre-team should be allowed purge: %s", d.Reason)
	}
}
