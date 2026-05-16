package cedar_test

import (
	"context"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
	cedarauthorizer "github.com/hummingbird-org/factory-workqueue/internal/authz/cedar"
)

func FuzzCedarAuthorize(f *testing.F) {
	f.Add("alice", "sre-team", "enqueue", "rpm-update")
	f.Add("bob", "random-team", "queues:read", "any-queue")
	f.Add("", "", "", "")
	f.Add("user\x00", "group\x00", "action\x00", "queue\x00")
	f.Add("user", "a]b[c", "enqueue", "../queue")
	f.Add("user", "group1,group2", "queues:read", "queue\nwith\nnewlines")

	const policy = `
permit(principal, action, resource)
when { context.groups.contains("sre-team") };

permit(principal, action in [
    Factory::Action::"queues:read",
    Factory::Action::"items:read"
], resource);
`

	a, err := cedarauthorizer.NewFromBytes("fuzz.cedar", []byte(policy))
	if err != nil {
		f.Fatalf("NewFromBytes: %v", err)
	}

	f.Fuzz(func(t *testing.T, user, group, action, queue string) {
		// Must not panic regardless of input.
		d := a.Authorize(context.Background(), authz.Request{
			User:   user,
			Groups: []string{group},
			Action: authz.Action(action),
			Queue:  queue,
		})
		// Decision must be deterministic: allowed or denied.
		_ = d.Allowed
		_ = d.Reason
	})
}

func FuzzCedarPolicyParse(f *testing.F) {
	f.Add([]byte(`permit(principal, action, resource);`))
	f.Add([]byte(`forbid(principal, action, resource);`))
	f.Add([]byte(``))
	f.Add([]byte(`{invalid`))
	f.Add([]byte(`permit(principal, action, resource) when { context.groups.contains("x") };`))
	f.Add([]byte{0x00, 0xff, 0xfe})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic. Parse errors are fine.
		a, err := cedarauthorizer.NewFromBytes("fuzz.cedar", data)
		if err != nil {
			return
		}
		// If parsing succeeded, authorization must not panic.
		a.Authorize(context.Background(), authz.Request{
			User: "u", Groups: []string{"g"}, Action: "enqueue", Queue: "q",
		})
	})
}
