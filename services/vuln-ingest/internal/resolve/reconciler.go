package resolve

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
	"github.com/hummingbird-org/vuln-ingest/internal/resolve/parser"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

type Reconciler struct {
	store   store.Store
	dataDir string
	parsers map[string]parser.Parser
}

func NewReconciler(s store.Store, dataDir string) *Reconciler {
	osv := &parser.OSVParser{}

	return &Reconciler{
		store:   s,
		dataDir: dataDir,
		parsers: map[string]parser.Parser{
			"ghsa":      osv,
			"rustsec":   osv,
			"govuln":    osv,
			"pypa":      osv,
			"psf":       osv,
			"osv":       osv,
			"kernel":    osv,
			"cvelistv5":     &parser.CVEListV5Parser{},
			"nvd":           &parser.NVDParser{},
			"anchore-nvd-overrides": &parser.NVDOverridesParser{},
		},
	}
}

// RegisterParser adds or overrides a parser for a source prefix.
func (r *Reconciler) RegisterParser(source string, p parser.Parser) {
	r.parsers[source] = p
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	source, _ := splitKey(req.Key)
	log := slog.With("key", req.Key, "source", source, "attempt", req.Attempt)

	fullPath := filepath.Join(r.dataDir, req.Key)
	cleanBase := filepath.Clean(r.dataDir) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(fullPath)+string(os.PathSeparator), cleanBase) {
		log.Error("path traversal detected", "resolved", fullPath)
		return reconciler.Reject("path traversal in key"), nil
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn("file not found, skipping")
			return reconciler.Completed(), nil
		}
		return reconciler.ProcessResponse{}, fmt.Errorf("read file: %w", err)
	}

	// Enrichment sources handled before parser dispatch.
	switch source {
	case "kev":
		return r.handleKEV(ctx, data, log)
	case "epss":
		return r.handleEPSS(ctx, data, log)
	}

	p, ok := r.parsers[source]
	if !ok {
		log.Error("unknown source prefix")
		return reconciler.Reject(fmt.Sprintf("unknown source: %s", source)), nil
	}

	rawHash := fmt.Sprintf("%x", sha256.Sum256(data))

	vulns, err := p.Parse(data)
	if err != nil {
		log.Error("parse failed", "error", err)
		return reconciler.Reject(fmt.Sprintf("parse error: %v", err)), nil
	}

	for i := range vulns {
		v := &vulns[i]

		existing, _ := r.store.GetSourceRecord(ctx, v.ID, source)
		if existing != nil && existing.RawHash == rawHash {
			log.Debug("unchanged, skipping", "vuln", v.ID)
			continue
		}

		if err := r.store.UpsertVulnerability(ctx, v, source); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("upsert %s: %w", v.ID, err)
		}

		rec := &model.SourceRecord{
			VulnID:  v.ID,
			Source:  source,
			RawHash: rawHash,
		}
		if err := r.store.UpsertSourceRecord(ctx, rec); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("upsert source record: %w", err)
		}

		log.Info("upserted", "vuln", v.ID)
	}

	return reconciler.Completed(), nil
}

func (r *Reconciler) handleKEV(ctx context.Context, data []byte, log *slog.Logger) (reconciler.ProcessResponse, error) {
	entries, err := parser.ParseKEVBatch(data)
	if err != nil {
		return reconciler.Reject(fmt.Sprintf("kev parse: %v", err)), nil
	}

	if err := r.store.UpsertKEVEntries(ctx, entries); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert kev: %w", err)
	}

	log.Info("upserted kev entries", "count", len(entries))
	return reconciler.Completed(), nil
}

func (r *Reconciler) handleEPSS(ctx context.Context, data []byte, log *slog.Logger) (reconciler.ProcessResponse, error) {
	scores, _, err := parser.ParseEPSSBatch(data)
	if err != nil {
		return reconciler.Reject(fmt.Sprintf("epss parse: %v", err)), nil
	}

	if err := r.store.UpsertEPSSScores(ctx, scores); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("upsert epss: %w", err)
	}

	log.Info("upserted epss scores", "count", len(scores))
	return reconciler.Completed(), nil
}

func splitKey(key string) (source, relPath string) {
	idx := strings.Index(key, "/")
	if idx == -1 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}
