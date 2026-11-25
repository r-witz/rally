//go:build !race

package bench

import (
	"net/http"
	"testing"
)

func TestRallyAllocations(t *testing.T) {
	want := map[string]float64{"Complex": 14, "GetUser": 4, "CreateUser": 7}
	h := rallyServer()
	for _, s := range scenarios {
		r, rb := newRequest(s)
		w := &discardWriter{h: http.Header{}}
		got := testing.AllocsPerRun(100, func() { serve(h, r, rb, w) })
		if w.status != s.status {
			t.Fatalf("%s: status %d: %s", s.name, w.status, w.body.String())
		}
		if got != want[s.name] {
			t.Errorf("%s: %v allocations per request, want %v (docs/BENCHMARKS.md quotes these numbers)", s.name, got, want[s.name])
		}
	}
}
