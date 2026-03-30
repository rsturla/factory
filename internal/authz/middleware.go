package authz

import (
	"log/slog"
	"net/http"
)

// Middleware returns HTTP middleware that checks authorization before
// passing the request to the wrapped handler.
//
// The action and queue are determined per-route by the caller. The
// identity is extracted from request headers.
func Middleware(authorizer Authorizer, action Action, queue string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := IdentityFromRequest(r)
			decision := authorizer.Authorize(r.Context(), Request{
				User:   id.User,
				Groups: id.Groups,
				Action: action,
				Queue:  queue,
			})

			if !decision.Allowed {
				slog.Warn("authorization denied",
					"user", id.User,
					"groups", id.Groups,
					"action", action,
					"queue", queue,
					"reason", decision.Reason,
				)
				http.Error(w, "forbidden: "+decision.Reason, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Wrap is a convenience for wrapping a single handler with authorization.
func Wrap(authorizer Authorizer, action Action, queue string, handler http.Handler) http.Handler {
	return Middleware(authorizer, action, queue)(handler)
}
