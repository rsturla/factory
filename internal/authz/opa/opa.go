// Package opa implements authz.Authorizer using Open Policy Agent.
//
// It calls an OPA server's REST API to evaluate policies. The OPA server
// is deployed separately (as a sidecar or standalone service).
//
// The input sent to OPA:
//
//	{
//	  "input": {
//	    "user": "alice",
//	    "groups": ["sre-team"],
//	    "action": "items:retry",
//	    "queue": "rpm-update"
//	  }
//	}
//
// OPA must return:
//
//	{"result": {"allow": true}}
//	{"result": {"allow": false, "reason": "..."}}
//
// Example Rego policy:
//
//	package factory.authz
//
//	default allow = false
//
//	allow {
//	    input.groups[_] == "sre-team"
//	}
//
//	allow {
//	    input.action == "queues:read"
//	}
//
//	allow {
//	    input.groups[_] == "rpm-team"
//	    input.queue == "rpm-update"
//	    input.action in {"enqueue", "items:read", "items:retry"}
//	}
package opa

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

// Config holds OPA connection settings.
type Config struct {
	// Endpoint is the OPA REST API URL, e.g. "http://localhost:8181".
	Endpoint string

	// PolicyPath is the OPA document path to query,
	// e.g. "v1/data/factory/authz".
	PolicyPath string

	// CACertPath is an optional path to a PEM-encoded CA certificate
	// for verifying the OPA server's TLS certificate. If empty, the
	// system default CA pool is used.
	CACertPath string
}

// Authorizer queries OPA for authorization decisions.
type Authorizer struct {
	endpoint string
	client   *http.Client
}

// New creates an OPA authorizer. It returns an error if CACertPath is set but
// cannot be read or parsed.
func New(cfg Config) (*Authorizer, error) {
	path := cfg.PolicyPath
	if path == "" {
		path = "v1/data/factory/authz"
	}

	client := &http.Client{Timeout: 5 * time.Second}

	if cfg.CACertPath != "" {
		caCert, err := os.ReadFile(cfg.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("read OPA CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("OPA CA cert file contains no valid PEM certificates: %s", cfg.CACertPath)
		}
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		}
	}

	return &Authorizer{
		endpoint: cfg.Endpoint + "/" + path,
		client:   client,
	}, nil
}

type opaInput struct {
	Input opaRequest `json:"input"`
}

type opaRequest struct {
	User   string   `json:"user"`
	Groups []string `json:"groups"`
	Action string   `json:"action"`
	Queue  string   `json:"queue"`
}

type opaResponse struct {
	Result struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	} `json:"result"`
}

func (a *Authorizer) Authorize(ctx context.Context, req authz.Request) authz.Decision {
	if req.User == "" {
		return authz.Decision{Allowed: false, Reason: "unauthenticated"}
	}

	body, err := json.Marshal(opaInput{
		Input: opaRequest{
			User:   req.User,
			Groups: req.Groups,
			Action: string(req.Action),
			Queue:  req.Queue,
		},
	})
	if err != nil {
		return authz.Decision{Allowed: false, Reason: "failed to marshal OPA request"}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return authz.Decision{Allowed: false, Reason: "failed to create OPA request"}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return authz.Decision{Allowed: false, Reason: fmt.Sprintf("OPA unreachable: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return authz.Decision{Allowed: false, Reason: fmt.Sprintf("OPA returned status %d", resp.StatusCode)}
	}

	var opaResp opaResponse
	if err := json.NewDecoder(resp.Body).Decode(&opaResp); err != nil {
		return authz.Decision{Allowed: false, Reason: "failed to decode OPA response"}
	}

	reason := opaResp.Result.Reason
	if !opaResp.Result.Allow && reason == "" {
		reason = "denied by policy"
	}

	return authz.Decision{
		Allowed: opaResp.Result.Allow,
		Reason:  reason,
	}
}

var _ authz.Authorizer = (*Authorizer)(nil)
