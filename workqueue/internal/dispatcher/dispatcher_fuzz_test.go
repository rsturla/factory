package dispatcher

import (
	"context"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func FuzzHeartbeatInterval(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1))
	f.Add(int64(-1))
	f.Add(int64(time.Second))
	f.Add(int64(time.Hour))
	f.Add(int64(time.Millisecond))

	f.Fuzz(func(t *testing.T, leaseDurNs int64) {
		s := inmem.New()
		d := &Dispatcher{
			store: s,
			cfg:   Config{LeaseDuration: time.Duration(leaseDurNs)},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		// Must not panic for any LeaseDuration value.
		d.heartbeat(ctx, "q", "k")
	})
}

func FuzzParseTraceparent(f *testing.F) {
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	f.Add("")
	f.Add("00-abc-def-01")
	f.Add("not-a-traceparent")
	f.Add("00-0af7651916cd43dd8448eb211c80319c-b9c7c989f97918e1-00")
	f.Add("00--00f067aa0ba902b7-01")
	f.Add("---")
	f.Add("00-00000000000000000000000000000000-0000000000000000-00")
	f.Fuzz(func(t *testing.T, input string) {
		parseTraceparent(input) // must not panic
	})
}
