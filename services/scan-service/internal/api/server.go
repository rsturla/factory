package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/rsturla/factory/services/scan-service/internal/store"
)

// Server implements the scan query API as REST/JSON.
type Server struct {
	store store.Store
}

// NewServer creates a new API server.
func NewServer(s store.Store) *Server {
	return &Server{store: s}
}

// RegisterRoutes registers all API routes on the given mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/scans/{platformID...}", s.getScans)
	mux.HandleFunc("GET /api/v1/findings/{platformID...}", s.getFindings)
	mux.HandleFunc("GET /api/v1/status", s.getStatus)
}

// getScans returns the latest scan per scanner for a platform.
func (s *Server) getScans(w http.ResponseWriter, r *http.Request) {
	platformID := r.PathValue("platformID")
	if platformID == "" {
		http.Error(w, "platformID required", http.StatusBadRequest)
		return
	}

	// Try each known scanner and return the latest scan for each.
	scannerNames := []string{"grype"}

	type scanResult struct {
		Scanner string `json:"scanner"`
		Scan    any    `json:"scan"`
	}

	var results []scanResult
	for _, name := range scannerNames {
		scan, err := s.store.GetLatestScan(r.Context(), platformID, name)
		if err != nil {
			slog.Error("get latest scan", "error", err, "platform", platformID, "scanner", name)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if scan != nil {
			results = append(results, scanResult{Scanner: name, Scan: scan})
		}
	}

	writeJSON(w, map[string]any{
		"platform_id": platformID,
		"scans":       results,
		"count":       len(results),
	})
}

// getFindings returns all findings for a platform, optionally filtered.
func (s *Server) getFindings(w http.ResponseWriter, r *http.Request) {
	platformID := r.PathValue("platformID")
	if platformID == "" {
		http.Error(w, "platformID required", http.StatusBadRequest)
		return
	}

	scannerFilter := r.URL.Query().Get("scanner")
	severityFilter := strings.ToLower(r.URL.Query().Get("severity"))

	// Default to grype if no scanner specified
	scannerNames := []string{"grype"}
	if scannerFilter != "" {
		scannerNames = []string{scannerFilter}
	}

	type scannerFindings struct {
		Scanner  string `json:"scanner"`
		Findings any    `json:"findings"`
		Count    int    `json:"count"`
	}

	var results []scannerFindings
	totalCount := 0

	for _, name := range scannerNames {
		findings, err := s.store.ListFindingsByPlatform(r.Context(), platformID, name)
		if err != nil {
			slog.Error("list findings", "error", err, "platform", platformID, "scanner", name)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Apply severity filter if specified
		if severityFilter != "" && findings != nil {
			var filtered []any
			for _, f := range findings {
				if strings.ToLower(f.Severity) == severityFilter {
					filtered = append(filtered, f)
				}
			}
			results = append(results, scannerFindings{
				Scanner:  name,
				Findings: filtered,
				Count:    len(filtered),
			})
			totalCount += len(filtered)
		} else {
			results = append(results, scannerFindings{
				Scanner:  name,
				Findings: findings,
				Count:    len(findings),
			})
			totalCount += len(findings)
		}
	}

	writeJSON(w, map[string]any{
		"platform_id": platformID,
		"scanners":    results,
		"total_count": totalCount,
	})
}

// getStatus returns scanner DB versions and last update times.
func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	scannerNames := []string{"grype"}

	type scannerStatus struct {
		Scanner string `json:"scanner"`
		State   any    `json:"state"`
	}

	var statuses []scannerStatus
	for _, name := range scannerNames {
		state, err := s.store.GetDBState(r.Context(), name)
		if err != nil {
			slog.Error("get db state", "error", err, "scanner", name)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		statuses = append(statuses, scannerStatus{
			Scanner: name,
			State:   state,
		})
	}

	writeJSON(w, map[string]any{
		"scanners": statuses,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}
