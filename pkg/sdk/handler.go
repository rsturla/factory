package sdk

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ReconcilerHandler returns an http.Handler that serves the /process endpoint.
// It decodes a ProcessRequest, calls the ReconcileFunc, and writes back the response.
func ReconcilerHandler(fn ReconcileFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req ProcessRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resp, err := fn(r.Context(), req)
		if err != nil {
			// Reconciler returned an error — treat as retriable failure.
			resp = ProcessResponse{
				Action: ActionCompleted,
				Error:  err.Error(),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Error("failed to encode response", "error", err)
		}
	})
}
