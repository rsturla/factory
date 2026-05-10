package dispatcher

import "testing"

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
