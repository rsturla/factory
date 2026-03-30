package headergroups_test

import (
	"context"
	"testing"

	"github.com/hummingbird-org/factory/internal/authz"
	"github.com/hummingbird-org/factory/internal/authz/headergroups"
)

func TestWildcardGroup(t *testing.T) {
	a := headergroups.New(headergroups.Config{
		Rules: []headergroups.Rule{
			{Groups: []string{"*"}, Actions: []string{"queues:read"}},
		},
	})

	d := a.Authorize(context.Background(), authz.Request{
		User: "alice", Groups: []string{"any-team"}, Action: authz.ActionQueuesRead,
	})
	if !d.Allowed {
		t.Errorf("expected allowed for wildcard group, got: %s", d.Reason)
	}
}

func TestWildcardAction(t *testing.T) {
	a := headergroups.New(headergroups.Config{
		Rules: []headergroups.Rule{
			{Groups: []string{"sre-team"}, Actions: []string{"*"}},
		},
	})

	for _, action := range []authz.Action{
		authz.ActionQueuesRead, authz.ActionItemsRetry, authz.ActionDeadLetterPurge,
	} {
		d := a.Authorize(context.Background(), authz.Request{
			User: "bob", Groups: []string{"sre-team"}, Action: action,
		})
		if !d.Allowed {
			t.Errorf("expected allowed for action %s with wildcard, got: %s", action, d.Reason)
		}
	}
}

func TestQueueScoping(t *testing.T) {
	a := headergroups.New(headergroups.Config{
		Rules: []headergroups.Rule{
			{Groups: []string{"rpm-team"}, Actions: []string{"enqueue", "items:read"}, Queues: []string{"rpm-update"}},
		},
	})

	// Allowed: rpm-team enqueuing to rpm-update.
	d := a.Authorize(context.Background(), authz.Request{
		User: "carol", Groups: []string{"rpm-team"}, Action: authz.ActionEnqueue, Queue: "rpm-update",
	})
	if !d.Allowed {
		t.Errorf("expected allowed for rpm-team on rpm-update: %s", d.Reason)
	}

	// Denied: rpm-team enqueuing to codegen.
	d = a.Authorize(context.Background(), authz.Request{
		User: "carol", Groups: []string{"rpm-team"}, Action: authz.ActionEnqueue, Queue: "codegen",
	})
	if d.Allowed {
		t.Error("expected denied for rpm-team on codegen queue")
	}
}

func TestUnauthenticatedDenied(t *testing.T) {
	a := headergroups.New(headergroups.Config{
		Rules: []headergroups.Rule{
			{Groups: []string{"*"}, Actions: []string{"*"}},
		},
	})

	d := a.Authorize(context.Background(), authz.Request{
		User: "", Action: authz.ActionQueuesRead,
	})
	if d.Allowed {
		t.Error("expected denied for unauthenticated user")
	}
}

func TestNoMatchingRule(t *testing.T) {
	a := headergroups.New(headergroups.Config{
		Rules: []headergroups.Rule{
			{Groups: []string{"sre-team"}, Actions: []string{"*"}},
		},
	})

	d := a.Authorize(context.Background(), authz.Request{
		User: "dave", Groups: []string{"dev-team"}, Action: authz.ActionItemsRetry,
	})
	if d.Allowed {
		t.Error("expected denied for non-matching group")
	}
}

func TestFirstRuleWins(t *testing.T) {
	a := headergroups.New(headergroups.Config{
		Rules: []headergroups.Rule{
			{Groups: []string{"sre-team"}, Actions: []string{"*"}},
			{Groups: []string{"*"}, Actions: []string{"queues:read"}},
		},
	})

	// SRE can do anything.
	d := a.Authorize(context.Background(), authz.Request{
		User: "alice", Groups: []string{"sre-team"}, Action: authz.ActionDeadLetterPurge,
	})
	if !d.Allowed {
		t.Error("expected SRE allowed for purge")
	}

	// Others can only read.
	d = a.Authorize(context.Background(), authz.Request{
		User: "bob", Groups: []string{"dev-team"}, Action: authz.ActionDeadLetterPurge,
	})
	if d.Allowed {
		t.Error("expected dev-team denied for purge")
	}

	d = a.Authorize(context.Background(), authz.Request{
		User: "bob", Groups: []string{"dev-team"}, Action: authz.ActionQueuesRead,
	})
	if !d.Allowed {
		t.Error("expected dev-team allowed for read")
	}
}

func TestFromJSON(t *testing.T) {
	cfg := `{"rules": [{"groups": ["admin"], "actions": ["*"]}]}`
	a, err := headergroups.NewFromJSON(cfg)
	if err != nil {
		t.Fatalf("NewFromJSON: %v", err)
	}

	d := a.Authorize(context.Background(), authz.Request{
		User: "root", Groups: []string{"admin"}, Action: authz.ActionDeadLetterPurge,
	})
	if !d.Allowed {
		t.Error("expected allowed from JSON config")
	}
}

func TestMultipleGroups(t *testing.T) {
	a := headergroups.New(headergroups.Config{
		Rules: []headergroups.Rule{
			{Groups: []string{"rpm-team"}, Actions: []string{"enqueue"}, Queues: []string{"rpm-update"}},
			{Groups: []string{"container-team"}, Actions: []string{"enqueue"}, Queues: []string{"container-build"}},
		},
	})

	// User in both groups can enqueue to both queues.
	d := a.Authorize(context.Background(), authz.Request{
		User: "eve", Groups: []string{"rpm-team", "container-team"}, Action: authz.ActionEnqueue, Queue: "rpm-update",
	})
	if !d.Allowed {
		t.Error("expected allowed for user in rpm-team")
	}

	d = a.Authorize(context.Background(), authz.Request{
		User: "eve", Groups: []string{"rpm-team", "container-team"}, Action: authz.ActionEnqueue, Queue: "container-build",
	})
	if !d.Allowed {
		t.Error("expected allowed for user in container-team")
	}
}
