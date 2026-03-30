// Package headergroups implements authz.Authorizer using simple group-based
// rules. No external dependencies — rules are configured via a JSON file
// or environment variable.
//
// Example config:
//
//	{
//	  "rules": [
//	    {"groups": ["sre-team"], "actions": ["*"]},
//	    {"groups": ["rpm-team"], "actions": ["queues:read", "items:read", "items:retry", "enqueue"], "queues": ["rpm-update"]},
//	    {"groups": ["*"], "actions": ["queues:read", "items:read", "workers:read"]}
//	  ]
//	}
//
// Rules are evaluated top-down. First match wins.
// "*" in actions means all actions. "*" in groups means any authenticated user.
// If no rule matches and the user is unauthenticated (empty user), access is denied.
package headergroups

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/hummingbird-org/factory/internal/authz"
)

// Config holds the authorization rules.
type Config struct {
	Rules []Rule `json:"rules"`
}

// Rule maps groups to allowed actions, optionally scoped to specific queues.
type Rule struct {
	Groups  []string `json:"groups"`  // group names, or ["*"] for any authenticated user
	Actions []string `json:"actions"` // action names, or ["*"] for all actions
	Queues  []string `json:"queues"`  // queue names to scope to (empty = all queues)
}

// Authorizer checks group-based rules.
type Authorizer struct {
	rules []Rule
}

// New creates an Authorizer from a Config.
func New(cfg Config) *Authorizer {
	return &Authorizer{rules: cfg.Rules}
}

// NewFromFile loads rules from a JSON file.
func NewFromFile(path string) (*Authorizer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authz config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse authz config: %w", err)
	}
	return New(cfg), nil
}

// NewFromJSON loads rules from a JSON string.
func NewFromJSON(data string) (*Authorizer, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse authz config: %w", err)
	}
	return New(cfg), nil
}

func (a *Authorizer) Authorize(_ context.Context, req authz.Request) authz.Decision {
	if req.User == "" {
		return authz.Decision{Allowed: false, Reason: "unauthenticated"}
	}

	for _, rule := range a.rules {
		if !matchesGroup(rule.Groups, req.Groups) {
			continue
		}
		if !matchesAction(rule.Actions, req.Action) {
			continue
		}
		if !matchesQueue(rule.Queues, req.Queue) {
			continue
		}
		return authz.Decision{Allowed: true}
	}

	return authz.Decision{
		Allowed: false,
		Reason:  fmt.Sprintf("no rule grants %s on queue %q for user %s", req.Action, req.Queue, req.User),
	}
}

func matchesGroup(ruleGroups, userGroups []string) bool {
	for _, rg := range ruleGroups {
		if rg == "*" {
			return true
		}
		for _, ug := range userGroups {
			if rg == ug {
				return true
			}
		}
	}
	return false
}

func matchesAction(ruleActions []string, action authz.Action) bool {
	for _, ra := range ruleActions {
		if ra == "*" || ra == string(action) {
			return true
		}
	}
	return false
}

func matchesQueue(ruleQueues []string, queue string) bool {
	if len(ruleQueues) == 0 {
		return true // no queue restriction
	}
	for _, rq := range ruleQueues {
		if rq == queue || rq == "*" {
			return true
		}
	}
	return false
}

var _ authz.Authorizer = (*Authorizer)(nil)
