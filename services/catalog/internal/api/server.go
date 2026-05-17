package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/rsturla/factory/services/catalog/internal/store"
)

// Server implements the catalog query API as REST/JSON.
type Server struct {
	store store.Store
}

// NewServer creates a new API server.
func NewServer(s store.Store) *Server {
	return &Server{store: s}
}

// RegisterRoutes registers all API routes on the given mux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/images", s.listImages)
	mux.HandleFunc("GET /api/v1/images/{id}", s.getImage)
	mux.HandleFunc("GET /api/v1/images/{id}/packages", s.getImagePackages)
	mux.HandleFunc("GET /api/v1/images/{id}/sbom", s.getImageSBOM)
	mux.HandleFunc("GET /api/v1/packages", s.searchPackages)
	mux.HandleFunc("GET /api/v1/packages/images", s.getPackageImages)
	mux.HandleFunc("GET /api/v1/search/images-by-package", s.searchImagesByPackage)
	mux.HandleFunc("GET /api/v1/diff/packages", s.diffPackages)
}

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 100)
	offset := intParam(r, "offset", 0)

	images, total, err := s.store.ListImages(r.Context(), limit, offset)
	if err != nil {
		slog.Error("list images", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"images":   images,
		"count":    len(images),
		"total":    total,
		"offset":   offset,
		"has_more": offset+len(images) < total,
	})
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	img, err := s.store.GetImage(r.Context(), id)
	if err != nil {
		slog.Error("get image", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if img == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeJSON(w, img)
}

func (s *Server) getImagePackages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	img, err := s.store.GetImage(r.Context(), id)
	if err != nil {
		slog.Error("get image", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if img == nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	archFilter := r.URL.Query().Get("arch")

	platforms, err := s.store.ListPlatformsByImage(r.Context(), id)
	if err != nil {
		slog.Error("list platforms", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type platformPackages struct {
		PlatformID   string `json:"platform_id"`
		Architecture string `json:"architecture"`
		Packages     any    `json:"packages"`
	}

	var result []platformPackages
	for _, p := range platforms {
		if archFilter != "" && p.Architecture != archFilter {
			continue
		}

		pkgs, err := s.store.ListPackagesByPlatform(r.Context(), p.ID)
		if err != nil {
			slog.Error("list packages", "error", err, "platform", p.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		result = append(result, platformPackages{
			PlatformID:   p.ID,
			Architecture: p.Architecture,
			Packages:     pkgs,
		})
	}

	writeJSON(w, map[string]any{
		"image_id":  id,
		"platforms": result,
	})
}

func (s *Server) getImageSBOM(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	source := r.URL.Query().Get("source")
	if source == "" {
		source = "syft"
	}
	archFilter := r.URL.Query().Get("arch")
	if archFilter == "" {
		archFilter = "amd64"
	}

	img, err := s.store.GetImage(r.Context(), id)
	if err != nil {
		slog.Error("get image", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if img == nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	platforms, err := s.store.ListPlatformsByImage(r.Context(), id)
	if err != nil {
		slog.Error("list platforms", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for _, p := range platforms {
		if p.Architecture != archFilter {
			continue
		}

		sbom, err := s.store.GetSBOM(r.Context(), p.ID, source)
		if err != nil {
			slog.Error("get sbom", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if sbom == nil {
			http.Error(w, "sbom not found", http.StatusNotFound)
			return
		}

		// Return raw SBOM content.
		w.Header().Set("Content-Type", "application/json")
		w.Write(sbom.Raw) //nolint:errcheck
		return
	}

	http.Error(w, "platform not found for architecture", http.StatusNotFound)
}

func (s *Server) searchPackages(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query parameter required", http.StatusBadRequest)
		return
	}

	limit := intParam(r, "limit", 100)

	packages, err := s.store.SearchPackages(r.Context(), name, limit)
	if err != nil {
		slog.Error("search packages", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"packages": packages,
		"count":    len(packages),
	})
}

func (s *Server) getPackageImages(w http.ResponseWriter, r *http.Request) {
	purl := r.URL.Query().Get("purl")
	if purl == "" {
		http.Error(w, "purl query parameter required", http.StatusBadRequest)
		return
	}

	images, err := s.store.GetImagesByPackage(r.Context(), purl)
	if err != nil {
		slog.Error("get images by package", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"purl":   purl,
		"images": images,
		"count":  len(images),
	})
}

func (s *Server) searchImagesByPackage(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name query parameter required", http.StatusBadRequest)
		return
	}

	version := r.URL.Query().Get("version")
	limit := intParam(r, "limit", 100)

	images, err := s.store.GetImagesByPackageName(r.Context(), name, version, limit)
	if err != nil {
		slog.Error("search images by package", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	result := map[string]any{
		"package_name": name,
		"images":       images,
		"count":        len(images),
	}
	if version != "" {
		result["package_version"] = version
	}
	writeJSON(w, result)
}

func (s *Server) diffPackages(w http.ResponseWriter, r *http.Request) {
	fromDigest := r.URL.Query().Get("from")
	toDigest := r.URL.Query().Get("to")
	if fromDigest == "" || toDigest == "" {
		http.Error(w, "from and to query parameters required", http.StatusBadRequest)
		return
	}

	fromPlatform, err := s.store.GetPlatform(r.Context(), fromDigest)
	if err != nil {
		slog.Error("get from platform", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if fromPlatform == nil {
		http.Error(w, "from platform not found", http.StatusNotFound)
		return
	}

	toPlatform, err := s.store.GetPlatform(r.Context(), toDigest)
	if err != nil {
		slog.Error("get to platform", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if toPlatform == nil {
		http.Error(w, "to platform not found", http.StatusNotFound)
		return
	}

	added, removed, err := s.store.DiffPackages(r.Context(), fromPlatform.ID, toPlatform.ID)
	if err != nil {
		slog.Error("diff packages", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"from":          fromDigest,
		"to":            toDigest,
		"added":         added,
		"removed":       removed,
		"added_count":   len(added),
		"removed_count": len(removed),
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

func intParam(r *http.Request, name string, def int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return def
	}
	if strings.EqualFold(name, "limit") && v > 1000 {
		return 1000
	}
	return v
}
