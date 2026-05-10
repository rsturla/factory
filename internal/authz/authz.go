// Package authz defines the authorization interface for the factory platform.
//
// Authorization is pluggable — swap backends by setting AUTHZ_BACKEND.
// The platform extracts identity from HTTP headers (set by an upstream
// auth proxy like OpenShift OAuth Proxy) and delegates the allow/deny
// decision to the configured backend.
//
// Available backends:
//   - noop: allow everything (development, testing)
//   - cedar: Cedar policies evaluated in-process (no external deps)
//   - opa: Open Policy Agent (external server)
//
// Adding a new backend:
//  1. Implement authz.Authorizer in a new package
//  2. Register it in internal/authzutil/create.go
package authz

import (
	"context"
	"net/http"
)

// Action represents an operation a user wants to perform.
type Action string

const (
	ActionQueuesRead      Action = "queues:read"
	ActionItemsRead       Action = "items:read"
	ActionItemsRetry      Action = "items:retry"
	ActionItemsCancel     Action = "items:cancel"
	ActionDeadLetterPurge Action = "deadletter:purge"
	ActionWorkersRead     Action = "workers:read"
	ActionEventsStream    Action = "events:stream"
	ActionEnqueue         Action = "enqueue"
	ActionClaim           Action = "claim"
	ActionComplete        Action = "complete"
	ActionRequeue         Action = "requeue"
	ActionDeadletter      Action = "deadletter"
	ActionTransition      Action = "transition"
	ActionQueueAdmin      Action = "queue:admin"
)

// Request describes an authorization check.
type Request struct {
	User   string   // from X-Forwarded-User or similar
	Groups []string // from X-Forwarded-Groups or similar
	Action Action   // what they want to do
	Queue  string   // which queue (empty for global operations)
}

// Decision is the result of an authorization check.
type Decision struct {
	Allowed bool
	Reason  string // human-readable reason (for denied requests)
}

// Authorizer makes allow/deny decisions.
// Implementations should be safe for concurrent use.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) Decision
}

// Identity holds the caller's identity extracted from HTTP headers.
type Identity struct {
	User   string
	Groups []string
}

// IdentityFromRequest extracts caller identity from HTTP headers.
// Works with OpenShift OAuth Proxy, Envoy, and similar proxies that
// set X-Forwarded-User and X-Forwarded-Groups.
func IdentityFromRequest(r *http.Request) Identity {
	user := r.Header.Get("X-Forwarded-User")
	if user == "" {
		user = r.Header.Get("X-Remote-User")
	}

	groups := splitGroups(r.Header.Get("X-Forwarded-Groups"))
	if len(groups) == 0 {
		groups = splitGroups(r.Header.Get("X-Remote-Groups"))
	}

	return Identity{User: user, Groups: groups}
}

func splitGroups(header string) []string {
	if header == "" {
		return nil
	}
	var groups []string
	for _, g := range splitCSV(header) {
		if g != "" {
			groups = append(groups, g)
		}
	}
	return groups
}

func splitCSV(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, trimSpace(s[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, trimSpace(s[start:]))
	return parts
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && s[i] == ' ' {
		i++
	}
	for j > i && s[j-1] == ' ' {
		j--
	}
	return s[i:j]
}
