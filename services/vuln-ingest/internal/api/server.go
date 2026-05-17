package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

// Server implements the vuln query API as REST/JSON.
// When protobuf codegen is wired, this will also serve gRPC.
type Server struct {
	store store.Store
}

func NewServer(s store.Store) *Server {
	return &Server{store: s}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/vulns/{id}", s.getVulnerability)
	mux.HandleFunc("GET /v1/vulns", s.listVulnerabilities)
	mux.HandleFunc("POST /v1/vulns:batchGet", s.batchGetVulnerabilities)
	mux.HandleFunc("GET /v1/affected", s.listAffected)
	mux.HandleFunc("POST /v1/affected:batchQuery", s.batchQueryAffected)
	mux.HandleFunc("GET /v1/sources", s.getSourceStatus)
}

func (s *Server) getVulnerability(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	v, err := s.store.GetVulnerability(r.Context(), id)
	if err != nil {
		slog.Error("get vulnerability", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// If not found by ID, search aliases.
	if v == nil {
		related, err := s.store.GetRelatedVulnerabilities(r.Context(), id)
		if err != nil {
			slog.Error("get related", "error", err)
		}
		if len(related) > 0 {
			v = related[0]
			related = related[1:]
		}
		if v == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}

	resp, err := s.enrichVuln(r.Context(), v)
	if err != nil {
		slog.Error("enrich vulnerability", "error", err)
	}

	// Find related vulns linked by alias.
	related, _ := s.store.GetRelatedVulnerabilities(r.Context(), v.ID)
	if len(related) > 0 {
		var enrichedRelated []vulnResponse
		for _, rv := range related {
			evr, _ := s.enrichVuln(r.Context(), rv)
			enrichedRelated = append(enrichedRelated, evr)
		}
		writeJSON(w, vulnWithRelated{vulnResponse: resp, Related: enrichedRelated})
	} else {
		writeJSON(w, resp)
	}

}

func (s *Server) listVulnerabilities(w http.ResponseWriter, r *http.Request) {
	opts := store.ListOpts{
		Limit:  intParam(r, "limit", 100),
		Offset: intParam(r, "offset", 0),
	}

	if ms := r.URL.Query().Get("modified_since"); ms != "" {
		t, err := time.Parse(time.RFC3339, ms)
		if err != nil {
			http.Error(w, "invalid modified_since format (RFC3339)", http.StatusBadRequest)
			return
		}
		opts.ModifiedSince = &t
	}

	if us := r.URL.Query().Get("updated_since"); us != "" {
		t, err := time.Parse(time.RFC3339, us)
		if err != nil {
			http.Error(w, "invalid updated_since format (RFC3339)", http.StatusBadRequest)
			return
		}
		opts.UpdatedSince = &t
	}

	vulns, err := s.store.ListVulnerabilities(r.Context(), opts)
	if err != nil {
		slog.Error("list vulnerabilities", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	total, _ := s.store.CountVulnerabilities(r.Context(), opts)

	noEnrich := r.URL.Query().Get("enrich") == "false"
	var resp []vulnResponse
	for _, v := range vulns {
		if noEnrich {
			resp = append(resp, vulnResponse{Vulnerability: v})
		} else {
			vr, _ := s.enrichVuln(r.Context(), v)
			resp = append(resp, vr)
		}
	}

	writeJSON(w, map[string]any{
		"vulnerabilities": resp,
		"total":           total,
	})
}

func (s *Server) batchGetVulnerabilities(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if len(req.IDs) > 1000 {
		http.Error(w, "max 1000 ids", http.StatusBadRequest)
		return
	}

	vulns, err := s.store.BatchGetVulnerabilities(r.Context(), req.IDs)
	if err != nil {
		slog.Error("batch get", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var resp []vulnResponse
	for _, v := range vulns {
		vr, _ := s.enrichVuln(r.Context(), v)
		resp = append(resp, vr)
	}

	writeJSON(w, map[string]any{"vulnerabilities": resp})
}

func (s *Server) listAffected(w http.ResponseWriter, r *http.Request) {
	packageName := r.URL.Query().Get("package_name")
	purl := r.URL.Query().Get("purl")

	if packageName == "" && purl == "" {
		http.Error(w, "package_name or purl required", http.StatusBadRequest)
		return
	}

	opts := store.ListOpts{
		Limit:  intParam(r, "limit", 100),
		Offset: intParam(r, "offset", 0),
	}

	var vulns []*model.Vulnerability
	var total int
	var err error

	if purl != "" {
		vulns, err = s.store.ListAffectedByPurl(r.Context(), purl, opts)
	} else {
		ecosystem := r.URL.Query().Get("ecosystem")
		vulns, err = s.store.ListAffectedByPackage(r.Context(), ecosystem, packageName, opts)
		if err == nil && ecosystem != "" {
			count, countErr := s.store.CountAffectedByPackage(r.Context(), ecosystem, packageName)
			if countErr != nil {
				slog.Error("count affected by package", "error", countErr)
			} else {
				total = count
			}
		}
	}

	if err != nil {
		slog.Error("list affected", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if total == 0 {
		total = len(vulns)
	}

	noEnrich := r.URL.Query().Get("enrich") == "false"
	var resp []vulnResponse
	for _, v := range vulns {
		if noEnrich {
			resp = append(resp, vulnResponse{Vulnerability: v})
		} else {
			vr, _ := s.enrichVuln(r.Context(), v)
			resp = append(resp, vr)
		}
	}

	writeJSON(w, map[string]any{
		"vulnerabilities": resp,
		"total":           total,
	})
}

func (s *Server) batchQueryAffected(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Queries []store.AffectedQuery `json:"queries"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if len(req.Queries) > 500 {
		http.Error(w, "max 500 queries", http.StatusBadRequest)
		return
	}

	for _, q := range req.Queries {
		if q.Purl == "" && q.PackageName == "" {
			http.Error(w, "each query must have purl or package_name", http.StatusBadRequest)
			return
		}
	}

	batchCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	opts := store.ListOpts{Limit: 100}

	results, err := s.store.BatchQueryAffected(batchCtx, req.Queries, opts)
	if err != nil {
		slog.Error("batch query affected", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"results": results})
}

func (s *Server) getSourceStatus(w http.ResponseWriter, r *http.Request) {
	checkpoints, err := s.store.ListCheckpoints(r.Context())
	if err != nil {
		slog.Error("list checkpoints", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"sources": checkpoints})
}

type vulnResponse struct {
	*model.Vulnerability
	KEV  *model.KEVEntry  `json:"kev,omitempty"`
	EPSS *model.EPSSScore `json:"epss,omitempty"`
}

type vulnWithRelated struct {
	vulnResponse
	Related []vulnResponse `json:"related,omitempty"`
}

func (s *Server) enrichVuln(ctx context.Context, v *model.Vulnerability) (vulnResponse, error) {
	resp := vulnResponse{Vulnerability: v}

	id := v.ID
	if !strings.HasPrefix(id, "CVE-") {
		for _, alias := range v.Aliases {
			if strings.HasPrefix(alias, "CVE-") {
				id = alias
				break
			}
		}
	}

	if strings.HasPrefix(id, "CVE-") {
		kev, _ := s.store.GetKEVEntry(ctx, id)
		resp.KEV = kev

		epss, _ := s.store.GetEPSSScore(ctx, id)
		resp.EPSS = epss
	}

	return resp, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func intParam(r *http.Request, name string, def int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return def
	}
	if name == "limit" && v > 1000 {
		return 1000
	}
	return v
}

