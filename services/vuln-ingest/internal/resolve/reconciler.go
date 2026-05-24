package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
	"github.com/hummingbird-org/vuln-ingest/internal/model"
	"github.com/hummingbird-org/vuln-ingest/internal/resolve/parser"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

type Reconciler struct {
	store   store.Store
	blobs   blob.Store
	parsers map[string]parser.Parser
}

func NewReconciler(s store.Store, blobs blob.Store) *Reconciler {
	osv := &parser.OSVParser{}

	return &Reconciler{
		store: s,
		blobs: blobs,
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

	if strings.Contains(req.Key, "..") || strings.HasPrefix(req.Key, "/") {
		log.Error("invalid key", "key", req.Key)
		return reconciler.Reject("invalid key"), nil
	}

	data, err := r.blobs.Get(ctx, req.Key)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			log.Warn("blob not found, skipping")
			return reconciler.Completed(), nil
		}
		return reconciler.ProcessResponse{}, fmt.Errorf("read blob: %w", err)
	}

	// Enrichment sources handled before parser dispatch.
	switch {
	case source == "kev":
		return r.handleKEV(ctx, data, log)
	case source == "epss":
		return r.handleEPSS(ctx, data, log)
	case source == "vendor-notes-debian":
		return r.handleDebianVendorNotes(ctx, data, log)
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

const debianHashKey = "vendor-notes-debian/content-hashes.json"

func (r *Reconciler) handleDebianVendorNotes(ctx context.Context, data []byte, log *slog.Logger) (reconciler.ProcessResponse, error) {
	entries, err := parser.ParseDebianCVEList(data)
	if err != nil {
		return reconciler.Reject(fmt.Sprintf("debian cve list parse: %v", err)), nil
	}

	prevHashes := r.loadHashMap(ctx, debianHashKey)
	newHashes := make(map[string]string, len(entries))

	var changed []model.VendorNote
	for cveID, content := range entries {
		contentJSON, err := json.Marshal(content)
		if err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("marshal debian content for %s: %w", cveID, err)
		}

		hash := fmt.Sprintf("%x", sha256.Sum256(contentJSON))
		newHashes[cveID] = hash

		if prevHashes[cveID] != hash {
			changed = append(changed, model.VendorNote{
				CVEID:   cveID,
				Vendor:  "debian",
				Content: content,
			})
		}
	}

	if len(changed) > 0 {
		if err := r.store.UpsertVendorNotes(ctx, changed); err != nil {
			return reconciler.ProcessResponse{}, fmt.Errorf("upsert vendor notes: %w", err)
		}
	}

	if err := r.saveHashMap(ctx, debianHashKey, newHashes); err != nil {
		log.Warn("failed to save content hashes", "error", err)
	}

	log.Info("upserted debian vendor notes", "changed", len(changed), "total", len(entries))
	return reconciler.Completed(), nil
}

func (r *Reconciler) loadHashMap(ctx context.Context, key string) map[string]string {
	data, err := r.blobs.Get(ctx, key)
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

func (r *Reconciler) saveHashMap(ctx context.Context, key string, m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return r.blobs.Put(ctx, key, data)
}

func splitKey(key string) (source, relPath string) {
	idx := strings.Index(key, "/")
	if idx == -1 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}
