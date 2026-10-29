package app

import (
	"testing"

	"vx/internal/config"
	"vx/internal/ping"
)

func TestRecordHistoryRing(t *testing.T) {
	a := &App{history: map[string][]int{}}

	// Simulate 5 probe cycles for one node; ring must keep only last 3.
	samples := []int{100, 120, 140, 160, 180}
	for _, ms := range samples {
		a.recordHistory(map[string]ping.Result{
			"n1:443|u": {OK: true, MS: ms},
		})
	}
	h := a.history["n1:443|u"]
	if len(h) != 3 {
		t.Fatalf("ring length = %d, want 3", len(h))
	}
	if h[0] != 140 || h[1] != 160 || h[2] != 180 {
		t.Fatalf("ring = %v, want [140 160 180] (oldest evicted)", h)
	}
}

func TestRecordHistorySkipsFailures(t *testing.T) {
	a := &App{history: map[string][]int{}}
	a.recordHistory(map[string]ping.Result{"k": {OK: true, MS: 50}})
	a.recordHistory(map[string]ping.Result{"k": {OK: false, Err: "timeout"}})
	a.recordHistory(map[string]ping.Result{"k": {OK: true, MS: 70}})
	h := a.history["k"]
	if len(h) != 2 || h[0] != 50 || h[1] != 70 {
		t.Fatalf("failed samples must not enter ring; got %v", h)
	}
}

func TestStabilityMath(t *testing.T) {
	a := &App{history: map[string][]int{}}

	// Not enough samples -> hasFull=false
	a.history["k"] = []int{100}
	avg, sd, full := a.Stability("k")
	if full {
		t.Error("1 sample must not count as full")
	}
	if avg != 100 || sd != 0 {
		t.Errorf("avg=%v sd=%v, want 100/0", avg, sd)
	}

	// Stable node: 100,102,104 -> avg 102, sd ~1.63
	a.history["k"] = []int{100, 102, 104}
	avg, sd, full = a.Stability("k")
	if !full {
		t.Error("3 samples should be full")
	}
	if avg != 102 {
		t.Errorf("avg = %v, want 102", avg)
	}
	if sd < 1.6 || sd > 1.7 {
		t.Errorf("sd = %v, want ~1.633", sd)
	}

	// Jittery node: 80,140,100 -> avg ~106.7, sd ~24.9
	a.history["j"] = []int{80, 140, 100}
	_, sd, _ = a.Stability("j")
	if sd < 24 || sd > 26 {
		t.Errorf("jittery sd = %v, want ~24.9", sd)
	}
}

func TestNodeStatusStableFlag(t *testing.T) {
	// Stable: deviation <= 35% of average
	a := &App{
		cfg: &config.Config{},
		history: map[string][]int{
			"calm": {100, 102, 104},
			"wild": {80, 140, 100},
		},
		pings: map[string]ping.Result{},
	}
	avgC, sdC, fullC := a.Stability("calm")
	if !(fullC && sdC <= 0.35*avgC) {
		t.Errorf("calm node should be stable: avg=%v sd=%v full=%v", avgC, sdC, fullC)
	}
	avgW, sdW, fullW := a.Stability("wild")
	stableW := fullW && sdW <= 0.20*avgW
	if stableW {
		t.Errorf("wild node should NOT be stable: avg=%v sd=%v", avgW, sdW)
	}
}
