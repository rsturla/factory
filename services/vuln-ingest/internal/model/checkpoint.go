package model

import "time"

type SourceCheckpoint struct {
	Source          string    `json:"source"`
	CheckpointValue string   `json:"checkpoint_value"`
	LastSyncAt      time.Time `json:"last_sync_at"`
	ItemsSynced     int64     `json:"items_synced"`
	ErrorMessage    string    `json:"error_message,omitempty"`
}
