package authn

import (
	"log/slog"
	"net/http"
)

// Middleware returns HTTP middleware that resolves caller identity and injects
// it as X-Forwarded-User and X-Forwarded-Groups headers. This allows the
// downstream authz middleware to work unchanged.
func Middleware(a Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := a.Identify(r)
			if err != nil {
				slog.Warn("authentication failed", "error", err)
				http.Error(w, "authentication failed: "+err.Error(), http.StatusUnauthorized)
				return
			}

			// Inject identity as headers for the authz layer.
			if id.User != "" {
				r.Header.Set("X-Forwarded-User", id.User)
			}
			if len(id.Groups) > 0 {
				r.Header.Set("X-Forwarded-Groups", joinGroups(id.Groups))
			}

			next.ServeHTTP(w, r)
		})
	}
}

func joinGroups(groups []string) string {
	var s string
	for i, g := range groups {
		if i > 0 {
			s += ","
		}
		s += g
	}
	return s
}
