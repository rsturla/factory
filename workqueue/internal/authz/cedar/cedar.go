// Package cedar implements authz.Authorizer using Cedar policies evaluated
// in-process via the cedar-go SDK. No external server needed.
//
// Cedar uses the PARC model: Principal, Action, Resource, Context.
// Factory maps to this as:
//
//	Principal: Factory::User::"alice"
//	Action:    Factory::Action::"items:retry"
//	Resource:  Factory::Queue::"rpm-update"
//	Context:   {"groups": ["sre-team", "on-call"]}
//
// Example Cedar policy:
//
//	// SRE team can do everything
//	permit(
//	    principal,
//	    action,
//	    resource
//	) when {
//	    context.groups.contains("sre-team")
//	};
//
//	// Everyone can read
//	permit(
//	    principal,
//	    action in [
//	        Factory::Action::"queues:read",
//	        Factory::Action::"items:read",
//	        Factory::Action::"workers:read",
//	        Factory::Action::"events:stream"
//	    ],
//	    resource
//	);
//
//	// RPM team can enqueue and retry in their queue
//	permit(
//	    principal,
//	    action in [
//	        Factory::Action::"enqueue",
//	        Factory::Action::"items:read",
//	        Factory::Action::"items:retry"
//	    ],
//	    resource == Factory::Queue::"rpm-update"
//	) when {
//	    context.groups.contains("rpm-team")
//	};
package cedar

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cedarlib "github.com/cedar-policy/cedar-go"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

const (
	entityTypeUser   cedarlib.EntityType = "Factory::User"
	entityTypeAction cedarlib.EntityType = "Factory::Action"
	entityTypeQueue  cedarlib.EntityType = "Factory::Queue"
)

// Authorizer evaluates Cedar policies in-process.
type Authorizer struct {
	policies *cedarlib.PolicySet
}

// New creates a Cedar authorizer from a policy set.
func New(policies *cedarlib.PolicySet) *Authorizer {
	return &Authorizer{policies: policies}
}

// NewFromPath loads Cedar policies from a file or a directory of .cedar files.
func NewFromPath(path string) (*Authorizer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat cedar policy path: %w", err)
	}

	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read cedar policy: %w", err)
		}
		return NewFromBytes(path, data)
	}

	// Directory — load all .cedar files and concatenate.
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read cedar policy dir: %w", err)
	}

	var combined strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cedar") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		combined.Write(data)
		combined.WriteString("\n")
	}

	if combined.Len() == 0 {
		return nil, fmt.Errorf("no .cedar files found in %s", path)
	}

	return NewFromBytes(path, []byte(combined.String()))
}

// NewFromBytes parses Cedar policies from raw bytes.
func NewFromBytes(filename string, data []byte) (*Authorizer, error) {
	ps, err := cedarlib.NewPolicySetFromBytes(filename, data)
	if err != nil {
		return nil, fmt.Errorf("parse cedar policy: %w", err)
	}
	return New(ps), nil
}

func (a *Authorizer) Authorize(_ context.Context, req authz.Request) authz.Decision {
	if req.User == "" {
		return authz.Decision{Allowed: false, Reason: "unauthenticated"}
	}

	// Build the groups set for context.
	groupValues := make([]cedarlib.Value, len(req.Groups))
	for i, g := range req.Groups {
		groupValues[i] = cedarlib.String(g)
	}

	// Map resource — use a global resource if no queue is specified.
	resource := cedarlib.NewEntityUID(entityTypeQueue, "_global")
	if req.Queue != "" {
		resource = cedarlib.NewEntityUID(entityTypeQueue, cedarlib.String(req.Queue))
	}

	cedarReq := cedarlib.Request{
		Principal: cedarlib.NewEntityUID(entityTypeUser, cedarlib.String(req.User)),
		Action:    cedarlib.NewEntityUID(entityTypeAction, cedarlib.String(string(req.Action))),
		Resource:  resource,
		Context: cedarlib.NewRecord(cedarlib.RecordMap{
			"groups": cedarlib.NewSet(groupValues...),
		}),
	}

	// Entities — just the user with their groups as attributes.
	entities := cedarlib.EntityMap{}
	entities[cedarReq.Principal] = cedarlib.Entity{
		UID: cedarReq.Principal,
		Attributes: cedarlib.NewRecord(cedarlib.RecordMap{
			"groups": cedarlib.NewSet(groupValues...),
		}),
	}

	decision, diagnostic := a.policies.IsAuthorized(entities, cedarReq)

	if decision == cedarlib.Allow {
		return authz.Decision{Allowed: true}
	}

	reason := "denied by cedar policy"
	if len(diagnostic.Errors) > 0 {
		reason = fmt.Sprintf("cedar policy error: %s", diagnostic.Errors[0].Message)
	}

	return authz.Decision{Allowed: false, Reason: reason}
}

var _ authz.Authorizer = (*Authorizer)(nil)
