// Package sdk provides the public API surface for factory reconciler authors.
//
// Reconciler authors import this package to:
//   - Define a ReconcileFunc that processes work items by key
//   - Use ReconcilerHandler to serve it as an HTTP endpoint
//   - Return structured responses (Completed, Converged, RequeueAfter, FanOut)
//   - Enqueue work into other factory queues via EnqueueClient
package sdk

import (
	"context"
	"time"
)

// ProcessRequest is sent by the dispatcher to the reconciler's /process endpoint.
type ProcessRequest struct {
	Key      string `json:"key"`
	Attempt  int    `json:"attempt"`
	Priority int    `json:"priority"`
}

// ProcessResponse is returned by the reconciler to the dispatcher.
type ProcessResponse struct {
	// Action describes the outcome: "completed", "converged", "requeue", "fan_out".
	Action string `json:"action"`

	// RequeueAfter is a duration string (e.g. "30s", "5m") for "requeue" actions.
	// Does not consume retry budget.
	RequeueAfter string `json:"requeue_after,omitempty"`

	// FanOutKeys lists keys to enqueue when action is "fan_out".
	// The current item is completed, and these keys are enqueued.
	FanOutKeys []string `json:"fan_out_keys,omitempty"`

	// Error is set when the reconciler encountered a retriable failure.
	// The dispatcher will requeue with exponential backoff.
	Error string `json:"error,omitempty"`
}

// Action constants for ProcessResponse.
const (
	ActionCompleted = "completed"
	ActionConverged = "converged"
	ActionRequeue   = "requeue"
	ActionFanOut    = "fan_out"
)

// ReconcileFunc is the signature reconciler authors implement.
// It receives a key and returns a response indicating what happened.
// Returning a non-nil error signals a retriable failure.
type ReconcileFunc func(ctx context.Context, req ProcessRequest) (ProcessResponse, error)

// QueueKey identifies a key to enqueue in a specific queue (for cross-queue fan-out).
type QueueKey struct {
	Queue    string `json:"queue"`
	Key      string `json:"key"`
	Priority int    `json:"priority"`
}

// Completed returns a response indicating the reconciler successfully
// completed the work for this key.
func Completed() ProcessResponse {
	return ProcessResponse{Action: ActionCompleted}
}

// Converged returns a response indicating the desired state is already met.
// The item is completed without any work being done.
func Converged() ProcessResponse {
	return ProcessResponse{Action: ActionConverged}
}

// RequeueAfter returns a response requesting the item be re-enqueued
// after the given delay. This does not consume retry budget.
func RequeueAfter(d time.Duration) ProcessResponse {
	return ProcessResponse{
		Action:       ActionRequeue,
		RequeueAfter: d.String(),
	}
}

// FanOut returns a response that completes the current item and enqueues
// the given keys into the same queue.
func FanOut(keys ...string) ProcessResponse {
	return ProcessResponse{
		Action:     ActionFanOut,
		FanOutKeys: keys,
	}
}
