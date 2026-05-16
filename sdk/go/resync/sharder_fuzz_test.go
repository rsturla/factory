package resync_test

import (
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/resync"
)

func FuzzBucket(f *testing.F) {
	f.Add("queue", "key-123", uint64(86400))
	f.Add("", "", uint64(1))
	f.Add("q", "k", uint64(3600))

	f.Fuzz(func(t *testing.T, queue, key string, periodSeconds uint64) {
		if periodSeconds == 0 {
			t.Skip("periodSeconds must be > 0")
		}

		tick := time.Second
		period := time.Duration(periodSeconds) * time.Second
		if period < tick || period%tick != 0 {
			t.Skip("invalid period/tick combo")
		}

		mock := newMockEnqueuer()
		sh, err := resync.New(queue+"q", tick, mock,
			resync.WithNow(staticNow(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))))
		if err != nil {
			t.Skip("invalid sharder params")
		}

		items, err := sh.Preview(t.Context(), period, []string{key})
		if err != nil {
			t.Fatalf("Preview: %v", err)
		}
		if len(items) > 1 {
			t.Fatalf("single key produced %d items", len(items))
		}
	})
}

func FuzzProcess(f *testing.F) {
	f.Add(uint64(3600), uint64(86400), "key-1\nkey-2\nkey-3")

	f.Fuzz(func(t *testing.T, tickSec, periodSec uint64, keyData string) {
		if tickSec == 0 || periodSec == 0 || periodSec < tickSec || periodSec%tickSec != 0 {
			t.Skip()
		}

		tick := time.Duration(tickSec) * time.Second
		period := time.Duration(periodSec) * time.Second
		if tick < time.Second {
			t.Skip()
		}

		var keys []string
		for _, k := range splitNull(keyData) {
			if k != "" {
				keys = append(keys, k)
			}
		}

		mock := newMockEnqueuer()
		sh, err := resync.New("fuzz-q", tick, mock,
			resync.WithNow(staticNow(time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC))))
		if err != nil {
			t.Skip()
		}

		result, err := sh.Process(t.Context(), period, keys)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		if result.InShard < 0 || result.Enqueued < 0 {
			t.Fatalf("negative counts: InShard=%d Enqueued=%d", result.InShard, result.Enqueued)
		}
		if result.Enqueued > result.InShard {
			t.Fatalf("Enqueued=%d > InShard=%d", result.Enqueued, result.InShard)
		}
	})
}

func splitNull(s string) []string {
	var parts []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
