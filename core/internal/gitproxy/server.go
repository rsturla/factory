package gitproxy

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
)

// Server is the git-proxy HTTP server.
type Server struct {
	minter      *TokenMinter
	policy      Policy
	audit       *AuditLogger
	credentials map[string]string // resource URL -> real Git credential
	logger      *slog.Logger
}

// NewServer creates a git-proxy server.
func NewServer(secret []byte, store runstore.Store, credentials map[string]string, logger *slog.Logger) *Server {
	return &Server{
		minter:      NewTokenMinter(secret),
		policy:      DefaultPolicy(),
		audit:       NewAuditLogger(store),
		credentials: credentials,
		logger:      logger,
	}
}

// Handler returns the HTTP handler for git-proxy.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/r/", s.proxyGit)
	return mux
}

// proxyGit handles Git HTTP protocol requests.
// URL format: /r/{resource-name}/{git-path}
func (s *Server) proxyGit(w http.ResponseWriter, r *http.Request) {
	// Extract resource name from path: /r/package-repo/info/refs
	path := strings.TrimPrefix(r.URL.Path, "/r/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 1 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	resourceName := parts[0]

	// Extract and validate token
	token := extractToken(r)
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	claims, err := s.minter.Validate(token)
	if err != nil {
		s.logger.Warn("invalid token", "error", err)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// Check resource access
	access, ok := claims.Resources[resourceName]
	if !ok {
		s.logger.Warn("resource not in token", "resource", resourceName, "run_id", claims.RunID)
		http.Error(w, "resource access denied", http.StatusForbidden)
		return
	}

	// Determine operation type
	operation := detectOperation(r)
	if err := CheckAccess(access, operation); err != nil {
		s.logger.Warn("access denied", "operation", operation, "resource", resourceName, "error", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Policy check for push operations
	if operation == "push" {
		// Extract ref from request (simplified for MVP)
		ref := extractRef(r)
		if err := s.policy.CheckBranch(ref); err != nil {
			s.logger.Warn("branch policy violation", "ref", ref, "error", err)
			http.Error(w, err.Error(), http.StatusForbidden)

			// Audit the denied operation
			s.audit.Log(r.Context(), GitOperation{
				RunID:      claims.RunID,
				StageID:    claims.StageID,
				Operation:  operation,
				Repository: access.URL,
				Ref:        ref,
				Success:    false,
				Error:      err.Error(),
				Timestamp:  time.Now(),
			})
			return
		}
	}

	// Proxy to real Git provider
	realURL := access.URL
	if !strings.HasPrefix(realURL, "http://") && !strings.HasPrefix(realURL, "https://") {
		// Convert git@github.com:org/repo to https://github.com/org/repo
		if strings.HasPrefix(realURL, "git@") {
			realURL = convertSSHToHTTPS(realURL)
		} else {
			realURL = "https://" + realURL
		}
	}

	targetURL, err := url.Parse(realURL)
	if err != nil {
		http.Error(w, "invalid resource URL", http.StatusInternalServerError)
		return
	}

	// Add git path
	if len(parts) > 1 {
		targetURL.Path = targetURL.Path + "/" + parts[1]
	}
	targetURL.RawQuery = r.URL.RawQuery

	// Get credential for this resource
	cred, ok := s.credentials[access.URL]
	if !ok {
		s.logger.Error("no credential for resource", "url", access.URL)
		http.Error(w, "credential not configured", http.StatusInternalServerError)
		return
	}

	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Director = func(req *http.Request) {
		req.URL = targetURL
		req.Host = targetURL.Host

		// Inject real credential
		auth := base64.StdEncoding.EncodeToString([]byte("token:" + cred))
		req.Header.Set("Authorization", "Basic "+auth)

		// Copy original headers except Authorization
		for k, v := range r.Header {
			if k != "Authorization" {
				req.Header[k] = v
			}
		}
	}

	// Audit successful operation
	defer func() {
		s.audit.Log(r.Context(), GitOperation{
			RunID:      claims.RunID,
			StageID:    claims.StageID,
			Operation:  operation,
			Repository: access.URL,
			Ref:        extractRef(r),
			Success:    true,
			Timestamp:  time.Now(),
		})
	}()

	proxy.ServeHTTP(w, r)
}

// extractToken gets token from Authorization header or query param.
func extractToken(r *http.Request) string {
	// Check Authorization header: Basic :token
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Basic ") {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "Basic "))
			if err == nil {
				parts := strings.SplitN(string(decoded), ":", 2)
				if len(parts) == 2 {
					return parts[1]
				}
			}
		}
	}

	// Check query param (for testing)
	return r.URL.Query().Get("token")
}

// detectOperation determines git operation from HTTP request.
func detectOperation(r *http.Request) string {
	if strings.Contains(r.URL.Path, "git-receive-pack") {
		return "push"
	}
	if strings.Contains(r.URL.Path, "git-upload-pack") {
		return "fetch"
	}
	return "fetch" // default to read operation
}

// extractRef extracts git ref from request (simplified).
func extractRef(r *http.Request) string {
	// For push, ref is in the request body, but parsing is complex.
	// For MVP, return placeholder. Real implementation needs git protocol parsing.
	return "refs/heads/factory/unknown"
}

// convertSSHToHTTPS converts git@github.com:org/repo to https://github.com/org/repo.
func convertSSHToHTTPS(sshURL string) string {
	// git@github.com:org/repo.git -> https://github.com/org/repo.git
	sshURL = strings.TrimPrefix(sshURL, "git@")
	sshURL = strings.Replace(sshURL, ":", "/", 1)
	return "https://" + sshURL
}
