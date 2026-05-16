package resync_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/pkg/types"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/resync"
)

// mockEnqueuer records all batches passed to EnqueueBatch.
type mockEnqueuer struct {
	mu       sync.Mutex
	batches  [][]types.BatchEnqueueItem
	failAt   int // return error on the Nth call (0-indexed), -1 = never
}

func newMockEnqueuer() *mockEnqueuer {
	return &mockEnqueuer{failAt: -1}
}

func (m *mockEnqueuer) EnqueueBatch(_ context.Context, _ string, items []types.BatchEnqueueItem) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAt >= 0 && len(m.batches) == m.failAt {
		return 0, fmt.Errorf("simulated failure")
	}
	cp := make([]types.BatchEnqueueItem, len(items))
	copy(cp, items)
	m.batches = append(m.batches, cp)
	return len(items), nil
}

func (m *mockEnqueuer) allItems() []types.BatchEnqueueItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []types.BatchEnqueueItem
	for _, b := range m.batches {
		all = append(all, b...)
	}
	return all
}

func (m *mockEnqueuer) batchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.batches)
}

func staticNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// --- Constructor validation ---

func TestNew_Validation(t *testing.T) {
	mock := newMockEnqueuer()
	cases := []struct {
		name    string
		queue   string
		tick    time.Duration
		eq      resync.Enqueuer
		wantErr bool
	}{
		{name: "valid", queue: "q", tick: time.Hour, eq: mock},
		{name: "empty queue", queue: "", tick: time.Hour, eq: mock, wantErr: true},
		{name: "zero tick", queue: "q", tick: 0, eq: mock, wantErr: true},
		{name: "negative tick", queue: "q", tick: -time.Second, eq: mock, wantErr: true},
		{name: "sub-second tick", queue: "q", tick: 500 * time.Millisecond, eq: mock, wantErr: true},
		{name: "fractional second tick", queue: "q", tick: 1500 * time.Millisecond, eq: mock, wantErr: true},
		{name: "nil enqueuer", queue: "q", tick: time.Hour, eq: nil, wantErr: true},
		{name: "1s tick", queue: "q", tick: time.Second, eq: mock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resync.New(tc.queue, tc.tick, tc.eq)
			if (err != nil) != tc.wantErr {
				t.Errorf("New: err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// --- Process validation ---

func TestProcess_Validation(t *testing.T) {
	sh, err := resync.New("q", time.Hour, newMockEnqueuer(),
		resync.WithNow(staticNow(time.Date(2026, 1, 1, 3, 17, 0, 0, time.UTC))))
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		period  time.Duration
		wantErr bool
	}{
		{name: "valid", period: 24 * time.Hour},
		{name: "period equals tick", period: time.Hour},
		{name: "zero", period: 0, wantErr: true},
		{name: "negative", period: -time.Hour, wantErr: true},
		{name: "not multiple of tick", period: 90 * time.Minute, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sh.Process(t.Context(), tc.period, nil)
			if (err != nil) != tc.wantErr {
				t.Errorf("Process: err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// --- Full coverage: every key exactly once per period ---

func TestProcess_FullCoverage(t *testing.T) {
	period := 24 * time.Hour
	tick := time.Hour

	keys := make([]string, 1000)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", rand.Int64())
	}

	periodStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Truncate(period)
	ticks := int(period / tick)

	seen := make(map[string]int)
	for i := range ticks {
		now := periodStart.Add(time.Duration(i)*tick + 15*time.Minute)
		mock := newMockEnqueuer()
		sh, err := resync.New("q", tick, mock, resync.WithNow(staticNow(now)))
		if err != nil {
			t.Fatal(err)
		}
		result, err := sh.Process(t.Context(), period, keys)
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if result.InShard != result.Enqueued {
			t.Errorf("tick %d: InShard=%d != Enqueued=%d", i, result.InShard, result.Enqueued)
		}
		for _, item := range mock.allItems() {
			seen[item.Key]++
		}
	}

	missing, extra := 0, 0
	for _, k := range keys {
		switch seen[k] {
		case 1:
		case 0:
			missing++
		default:
			extra++
		}
	}
	if missing != 0 || extra != 0 {
		t.Errorf("coverage: missing=%d extra=%d (want 0/0 across %d keys)", missing, extra, len(keys))
	}
}

// --- Full coverage with non-day-dividing period ---

func TestProcess_FullCoverage_7hPeriod(t *testing.T) {
	period := 7 * time.Hour
	tick := time.Hour

	keys := make([]string, 500)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", rand.Int64())
	}

	periodStart := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Truncate(period)
	ticks := int(period / tick)

	seen := make(map[string]int)
	for i := range ticks {
		now := periodStart.Add(time.Duration(i)*tick + 10*time.Minute)
		mock := newMockEnqueuer()
		sh, err := resync.New("q", tick, mock, resync.WithNow(staticNow(now)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sh.Process(t.Context(), period, keys); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		for _, item := range mock.allItems() {
			seen[item.Key]++
		}
	}

	missing, extra := 0, 0
	for _, k := range keys {
		switch seen[k] {
		case 1:
		case 0:
			missing++
		default:
			extra++
		}
	}
	if missing != 0 || extra != 0 {
		t.Errorf("7h period coverage: missing=%d extra=%d (want 0/0)", missing, extra)
	}
}

// --- Stability: same tick → same result ---

func TestProcess_Stability(t *testing.T) {
	period := 24 * time.Hour
	tick := time.Hour
	periodStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Truncate(period)

	keys := make([]string, 200)
	for i := range keys {
		keys[i] = fmt.Sprintf("k-%d", i)
	}

	t1 := periodStart.Add(2*tick + 5*time.Minute)
	t2 := periodStart.Add(2*tick + 45*time.Minute)

	m1 := newMockEnqueuer()
	sh1, _ := resync.New("q", tick, m1, resync.WithNow(staticNow(t1)))
	sh1.Process(t.Context(), period, keys)

	m2 := newMockEnqueuer()
	sh2, _ := resync.New("q", tick, m2, resync.WithNow(staticNow(t2)))
	sh2.Process(t.Context(), period, keys)

	items1 := m1.allItems()
	items2 := m2.allItems()
	if len(items1) != len(items2) {
		t.Fatalf("stability: %d vs %d items", len(items1), len(items2))
	}

	set1 := make(map[string]time.Time)
	for _, item := range items1 {
		set1[item.Key] = *item.NotBefore
	}
	for _, item := range items2 {
		if nb, ok := set1[item.Key]; !ok {
			t.Errorf("key %q in t2 but not t1", item.Key)
		} else if !nb.Equal(*item.NotBefore) {
			t.Errorf("key %q: NotBefore %v vs %v", item.Key, nb, *item.NotBefore)
		}
	}
}

// --- Reshuffle: different periods → different shard assignments ---

func TestProcess_Reshuffle(t *testing.T) {
	period := 24 * time.Hour
	tick := time.Hour
	periodStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Truncate(period)

	keys := make([]string, 5000)
	for i := range keys {
		keys[i] = fmt.Sprintf("k-%d", i)
	}

	m1 := newMockEnqueuer()
	sh1, _ := resync.New("q", tick, m1, resync.WithNow(staticNow(periodStart.Add(time.Minute))))
	sh1.Process(t.Context(), period, keys)

	m2 := newMockEnqueuer()
	sh2, _ := resync.New("q", tick, m2, resync.WithNow(staticNow(periodStart.Add(period+time.Minute))))
	sh2.Process(t.Context(), period, keys)

	set1 := make(map[string]struct{})
	for _, item := range m1.allItems() {
		set1[item.Key] = struct{}{}
	}

	overlap := 0
	for _, item := range m2.allItems() {
		if _, ok := set1[item.Key]; ok {
			overlap++
		}
	}

	upper := len(m1.allItems()) / 4
	if upper == 0 {
		upper = 1
	}
	if overlap > upper {
		t.Errorf("reshuffle: overlap %d/%d, expected roughly 1/24 of shard", overlap, len(m1.allItems()))
	}
}

// --- Cross-queue decorrelation ---

func TestProcess_CrossQueueDecorrelation(t *testing.T) {
	period := 24 * time.Hour
	tick := time.Hour
	now := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)

	keys := make([]string, 5000)
	for i := range keys {
		keys[i] = fmt.Sprintf("k-%d", i)
	}

	m1 := newMockEnqueuer()
	sh1, _ := resync.New("queue-a", tick, m1, resync.WithNow(staticNow(now)))
	sh1.Process(t.Context(), period, keys)

	m2 := newMockEnqueuer()
	sh2, _ := resync.New("queue-b", tick, m2, resync.WithNow(staticNow(now)))
	sh2.Process(t.Context(), period, keys)

	set1 := make(map[string]struct{})
	for _, item := range m1.allItems() {
		set1[item.Key] = struct{}{}
	}

	overlap := 0
	for _, item := range m2.allItems() {
		if _, ok := set1[item.Key]; ok {
			overlap++
		}
	}

	upper := len(m1.allItems()) / 4
	if upper == 0 {
		upper = 1
	}
	if overlap > upper {
		t.Errorf("decorrelation: overlap %d/%d between queues", overlap, len(m1.allItems()))
	}
}

// --- Priority flows through ---

func TestProcess_Priority(t *testing.T) {
	mock := newMockEnqueuer()
	sh, _ := resync.New("q", time.Hour, mock,
		resync.WithNow(staticNow(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))),
		resync.WithPriority(func(key string) int {
			if key == "important" {
				return 100
			}
			return 0
		}))

	keys := []string{"important", "normal-1", "normal-2", "normal-3"}
	// Use period=tick so all keys land in the shard.
	sh.Process(t.Context(), time.Hour, keys)

	for _, item := range mock.allItems() {
		if item.Key == "important" && item.Priority != 100 {
			t.Errorf("important: priority=%d, want 100", item.Priority)
		}
		if item.Key != "important" && item.Priority != 0 {
			t.Errorf("%s: priority=%d, want 0", item.Key, item.Priority)
		}
	}
}

// --- Empty keys ---

func TestProcess_EmptyKeys(t *testing.T) {
	mock := newMockEnqueuer()
	sh, _ := resync.New("q", time.Hour, mock,
		resync.WithNow(staticNow(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))))

	for _, keys := range [][]string{nil, {}} {
		result, err := sh.Process(t.Context(), 24*time.Hour, keys)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if result.InShard != 0 || result.Enqueued != 0 {
			t.Errorf("empty: InShard=%d Enqueued=%d", result.InShard, result.Enqueued)
		}
	}
	if mock.batchCount() != 0 {
		t.Errorf("expected 0 batch calls, got %d", mock.batchCount())
	}
}

// --- Absolute timestamps in [tickStart, tickStart+tick) ---

func TestProcess_AbsoluteTimestamps(t *testing.T) {
	tick := time.Hour
	period := 24 * time.Hour
	now := time.Date(2026, 1, 1, 3, 17, 0, 0, time.UTC)
	tickStart := now.Truncate(tick)
	tickEnd := tickStart.Add(tick)

	keys := make([]string, 500)
	for i := range keys {
		keys[i] = fmt.Sprintf("k-%d", i)
	}

	mock := newMockEnqueuer()
	sh, _ := resync.New("q", tick, mock, resync.WithNow(staticNow(now)))
	sh.Process(t.Context(), period, keys)

	for _, item := range mock.allItems() {
		nb := *item.NotBefore
		if nb.Before(tickStart) || !nb.Before(tickEnd) {
			t.Errorf("key %q: NotBefore %v outside [%v, %v)", item.Key, nb, tickStart, tickEnd)
		}
	}
}

// --- Batch splitting for >10k items ---

func TestProcess_BatchSplitting(t *testing.T) {
	// period=tick guarantees all keys land in the shard.
	tick := time.Hour
	nKeys := 25_000
	keys := make([]string, nKeys)
	for i := range keys {
		keys[i] = fmt.Sprintf("k-%d", i)
	}

	mock := newMockEnqueuer()
	sh, _ := resync.New("q", tick, mock,
		resync.WithNow(staticNow(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))))

	result, err := sh.Process(t.Context(), tick, keys)
	if err != nil {
		t.Fatal(err)
	}

	if result.InShard != nKeys {
		t.Errorf("InShard=%d, want %d", result.InShard, nKeys)
	}
	if result.Enqueued != nKeys {
		t.Errorf("Enqueued=%d, want %d", result.Enqueued, nKeys)
	}

	batches := mock.batchCount()
	if batches != 3 {
		t.Errorf("batch calls=%d, want 3 (10k+10k+5k)", batches)
	}
}

// --- Error propagation ---

func TestProcess_EnqueueError(t *testing.T) {
	mock := newMockEnqueuer()
	mock.failAt = 0

	sh, _ := resync.New("q", time.Hour, mock,
		resync.WithNow(staticNow(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))))

	keys := []string{"a", "b", "c"}
	_, err := sh.Process(t.Context(), time.Hour, keys)
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Preview returns same items without enqueuing ---

func TestPreview(t *testing.T) {
	tick := time.Hour
	period := 24 * time.Hour
	now := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

	keys := make([]string, 200)
	for i := range keys {
		keys[i] = fmt.Sprintf("k-%d", i)
	}

	mock := newMockEnqueuer()
	sh, _ := resync.New("q", tick, mock, resync.WithNow(staticNow(now)))

	preview, err := sh.Preview(t.Context(), period, keys)
	if err != nil {
		t.Fatal(err)
	}

	if mock.batchCount() != 0 {
		t.Errorf("Preview should not call EnqueueBatch, got %d calls", mock.batchCount())
	}

	result, err := sh.Process(t.Context(), period, keys)
	if err != nil {
		t.Fatal(err)
	}

	if len(preview) != result.InShard {
		t.Errorf("Preview returned %d items, Process had InShard=%d", len(preview), result.InShard)
	}

	processItems := mock.allItems()
	previewMap := make(map[string]time.Time)
	for _, item := range preview {
		previewMap[item.Key] = *item.NotBefore
	}
	for _, item := range processItems {
		if nb, ok := previewMap[item.Key]; !ok {
			t.Errorf("key %q in Process but not Preview", item.Key)
		} else if !nb.Equal(*item.NotBefore) {
			t.Errorf("key %q: Preview NotBefore %v != Process NotBefore %v", item.Key, nb, *item.NotBefore)
		}
	}
}

// --- Context cancellation ---

func TestProcess_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mock := &cancelAwareEnqueuer{}
	sh, _ := resync.New("q", time.Hour, mock,
		resync.WithNow(staticNow(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))))

	_, err := sh.Process(ctx, time.Hour, []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

type cancelAwareEnqueuer struct{}

func (c *cancelAwareEnqueuer) EnqueueBatch(ctx context.Context, _ string, _ []types.BatchEnqueueItem) (int, error) {
	return 0, ctx.Err()
}

