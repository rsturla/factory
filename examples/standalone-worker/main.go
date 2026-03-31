// standalone-worker is an example of a self-dispatching reconciler worker.
//
// Unlike the echo-reconciler (which is an HTTP server invoked by the
// dispatcher), this worker drives its own claim loop — it claims items
// from the workqueue, processes them locally, and reports results back.
//
// Use this pattern when the reconciler does heavy local work (rpmbuild,
// container builds, AI inference) on dedicated compute (EC2 instances,
// bare metal) where having the dispatcher hold an HTTP connection open
// for the duration of the work is impractical.
//
// The worker:
//  1. Claims a batch of items from the queue
//  2. Heartbeats to keep leases alive while working
//  3. Runs the reconcile function locally
//  4. Reports success/failure back to the store
//  5. Repeats
//
// If the worker crashes mid-work, the lease expires and the reaper
// reclaims the item — no work is lost.
//
// Usage:
//
//	QUEUE_NAME=rpm-update \
//	DATABASE_URL=postgres://factory:factory@localhost:5432/factory?sslmode=disable \
//	go run ./examples/standalone-worker/
//
// Environment variables:
//
//	QUEUE_NAME        - Queue to process (required)
//	WORKQUEUE_API     - Receiver/workqueue API endpoint (required, e.g. "http://factory-receiver:8081")
//	WORKER_ID         - Unique worker identifier (default: hostname)
//	BATCH_SIZE        - Items to claim per cycle (default: 1)
//	LEASE_DURATION    - Lease duration (default: 2h)
//	POLL_INTERVAL     - How often to check for work when idle (default: 5s)
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/pkg/client"
)

func main() {
	queueName := requireEnv("QUEUE_NAME")
	apiEndpoint := requireEnv("WORKQUEUE_API")
	workerID := envOr("WORKER_ID", hostname())
	batchSize := envInt("BATCH_SIZE", 1)
	leaseDuration := envDuration("LEASE_DURATION", 2*time.Hour)
	pollInterval := envDuration("POLL_INTERVAL", 5*time.Second)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Connect to the workqueue via HTTP — no direct database access.
	wq := client.NewWorkqueueClient(apiEndpoint)

	slog.Info("standalone worker starting",
		"queue", queueName,
		"api", apiEndpoint,
		"worker_id", workerID,
		"batch_size", batchSize,
		"lease_duration", leaseDuration,
		"poll_interval", pollInterval,
	)

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return
		default:
		}

		items, err := wq.ClaimBatch(ctx, queueName, batchSize, workerID, leaseDuration)
		if err != nil {
			slog.Error("claim failed", "error", err)
			time.Sleep(pollInterval)
			continue
		}

		if len(items) == 0 {
			time.Sleep(pollInterval)
			continue
		}

		slog.Info("claimed items", "count", len(items))

		// Process items concurrently.
		var wg sync.WaitGroup
		for _, item := range items {
			wg.Add(1)
			go func() {
				defer wg.Done()
				processItem(ctx, wq, item, leaseDuration)
			}()
		}
		wg.Wait()
	}
}

func processItem(ctx context.Context, s *client.WorkqueueClient, item store.WorkItem, leaseDuration time.Duration) {
	slog.Info("processing", "key", item.Key, "attempt", item.Attempts, "priority", item.Priority)

	// Transition to running.
	if err := s.Transition(ctx, item.Queue, item.Key, store.StatusClaimed, store.StatusRunning); err != nil {
		slog.Error("transition failed", "key", item.Key, "error", err)
		return
	}

	// Heartbeat in the background to keep the lease alive.
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()

	go func() {
		ticker := time.NewTicker(leaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := s.ExtendLease(heartbeatCtx, item.Queue, item.Key, leaseDuration); err != nil {
					slog.Warn("heartbeat failed", "key", item.Key, "error", err)
					return
				}
				slog.Debug("heartbeat", "key", item.Key)
			}
		}
	}()

	// ================================================================
	// YOUR RECONCILE LOGIC HERE
	//
	// This is where you do the actual work — rpmbuild, container build,
	// AI inference, test execution, etc. This can take minutes or hours.
	//
	// Example:
	//   fix := findUpstreamFix(item.Key)
	//   branch := cherryPick(fix)
	//   err := exec.CommandContext(ctx, "rpmbuild", "-ba", spec).Run()
	//
	// For this example, we just simulate work:
	err := doWork(ctx, item.Key)
	// ================================================================

	heartbeatCancel() // stop heartbeating

	if err != nil {
		slog.Error("processing failed", "key", item.Key, "error", err)
		s.Fail(ctx, item.Queue, item.Key, err.Error())
		return
	}

	if err := s.Complete(ctx, item.Queue, item.Key); err != nil {
		slog.Error("complete failed", "key", item.Key, "error", err)
		return
	}

	slog.Info("completed", "key", item.Key)
}

// doWork simulates a long-running reconciliation.
// Replace this with your actual reconcile logic.
func doWork(ctx context.Context, key string) error {
	duration := envDuration("WORK_DURATION", 5*time.Second)

	select {
	case <-time.After(duration):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return fallback
}

func hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "unknown"
	}
	return h
}
