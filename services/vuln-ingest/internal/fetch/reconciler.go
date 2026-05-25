package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
	"github.com/hummingbird-org/vuln-ingest/internal/fetch/source"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

const MaxBatchSize = 5000

type Reconciler struct {
	sources      map[string]source.Source
	store        store.Store
	blobs        blob.Store
	receiverURL  string
	resolveQueue string
	httpClient   *http.Client
}

func NewReconciler(s store.Store, blobs blob.Store, receiverURL, resolveQueue string) *Reconciler {
	return &Reconciler{
		sources:      make(map[string]source.Source),
		store:        s,
		blobs:        blobs,
		receiverURL:  receiverURL,
		resolveQueue: resolveQueue,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *Reconciler) RegisterSource(s source.Source) {
	r.sources[s.Name()] = s
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
	log := slog.With("source", req.Key, "attempt", req.Attempt)

	src, ok := r.sources[req.Key]
	if !ok {
		log.Error("unknown source")
		return reconciler.Reject(fmt.Sprintf("unknown source: %s", req.Key)), nil
	}

	cp, err := r.store.GetCheckpoint(ctx, req.Key)
	if err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("get checkpoint for %s: %w", req.Key, err)
	}
	var checkpoint string
	if cp != nil {
		checkpoint = cp.CheckpointValue
	}

	log.Info("fetching", "checkpoint", checkpoint)

	result, err := src.Fetch(ctx, r.blobs, checkpoint)
	if err != nil {
		if cpErr := r.store.SetCheckpointError(ctx, req.Key, err.Error()); cpErr != nil {
			slog.Error("failed to persist checkpoint error", "source", req.Key, "error", cpErr)
		}
		return reconciler.ProcessResponse{}, fmt.Errorf("fetch %s: %w", req.Key, err)
	}

	if len(result.ChangedFiles) == 0 {
		log.Info("converged, no changes")
		if result.NewCheckpoint != "" {
			if err := r.store.UpdateCheckpoint(ctx, req.Key, result.NewCheckpoint, 0); err != nil {
				return reconciler.ProcessResponse{}, fmt.Errorf("update checkpoint for %s: %w", req.Key, err)
			}
		}
		return reconciler.Converged(), nil
	}

	if err := r.batchEnqueue(ctx, result.ChangedFiles); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("enqueue: %w", err)
	}

	if err := r.store.UpdateCheckpoint(ctx, req.Key, result.NewCheckpoint, int64(result.ItemCount)); err != nil {
		return reconciler.ProcessResponse{}, fmt.Errorf("update checkpoint for %s: %w", req.Key, err)
	}

	log.Info("fetch complete", "enqueued", result.ItemCount, "new_checkpoint", result.NewCheckpoint)
	return reconciler.Completed(), nil
}

func (r *Reconciler) batchEnqueue(ctx context.Context, keys []string) error {
	type batchItem struct {
		Key      string `json:"key"`
		Priority int    `json:"priority"`
	}

	for i := 0; i < len(keys); i += MaxBatchSize {
		end := i + MaxBatchSize
		if end > len(keys) {
			end = len(keys)
		}

		items := make([]batchItem, 0, end-i)
		for _, k := range keys[i:end] {
			items = append(items, batchItem{Key: k, Priority: 0})
		}

		payload := map[string]any{
			"queue": r.resolveQueue,
			"items": items,
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal batch payload: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.receiverURL+"/enqueue/batch", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := r.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("batch enqueue: %w", err)
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return fmt.Errorf("batch enqueue status %d: %s", resp.StatusCode, string(errBody))
		}
		resp.Body.Close()
	}

	return nil
}
