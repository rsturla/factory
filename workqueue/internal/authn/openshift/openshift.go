// Package openshift implements authn.Authenticator by validating Bearer tokens
// against the OpenShift user API.
//
// It calls /apis/user.openshift.io/v1/users/~ with the caller's token to
// resolve the username and group memberships.
package openshift

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

// Authenticator validates OpenShift Bearer tokens.
type Authenticator struct {
	apiURL string
	client *http.Client
}

// NewWithClient creates an OpenShift authenticator with the given API URL and HTTP client.
// Useful for testing.
func NewWithClient(apiURL string, client *http.Client) *Authenticator {
	return &Authenticator{apiURL: apiURL, client: client}
}

// New creates an OpenShift authenticator.
// It reads the in-cluster CA and API server address automatically.
func New() (*Authenticator, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not running in a Kubernetes cluster (KUBERNETES_SERVICE_HOST/PORT not set)")
	}

	apiURL := fmt.Sprintf("https://%s:%s", host, port)

	client := &http.Client{Timeout: 5 * 1e9} // 5 seconds

	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	if caCert, err := os.ReadFile(caPath); err == nil {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(caCert)
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		}
	}

	return &Authenticator{apiURL: apiURL, client: client}, nil
}

type userResponse struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Groups []string `json:"groups"`
}

func (a *Authenticator) Identify(r *http.Request) (authz.Identity, error) {
	token := extractBearer(r)
	if token == "" {
		// No token — return empty identity (authz layer will reject as unauthenticated).
		return authz.Identity{}, nil
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		a.apiURL+"/apis/user.openshift.io/v1/users/~", nil)
	if err != nil {
		return authz.Identity{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.client.Do(req)
	if err != nil {
		return authz.Identity{}, fmt.Errorf("openshift api: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return authz.Identity{}, fmt.Errorf("invalid or expired token")
	}
	if resp.StatusCode != http.StatusOK {
		return authz.Identity{}, fmt.Errorf("openshift api returned %d", resp.StatusCode)
	}

	var user userResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return authz.Identity{}, fmt.Errorf("decode user response: %w", err)
	}

	return authz.Identity{
		User:   user.Metadata.Name,
		Groups: user.Groups,
	}, nil
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return auth[7:]
	}
	return ""
}
