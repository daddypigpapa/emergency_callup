package transit

import (
	"math/rand"
	"testing"

	"emergencycallup/internal/area"
)

// Stops 2 km apart on an east-west line (too far to walk between), plus
// stop 4 300 m north of stop 3 (a walking transfer).
//
//	route A: 0 → 1 → 2   120 s per hop, headway 10 min (wait 300 s)
//	route B: 2 → 3       60 s,          headway 6 min  (wait 180 s)
func testNet(hwA int) (*Network, []area.Point) {
	const lat = 35.87
	dLng := 2000 / (111320 * 0.8100) // ≈ 2 km at this latitude
	pts := []area.Point{
		{Lat: lat, Lng: 128.50},
		{Lat: lat, Lng: 128.50 + dLng},
		{Lat: lat, Lng: 128.50 + 2*dLng},
		{Lat: lat, Lng: 128.50 + 3*dLng},
		{Lat: lat + 300/111320.0, Lng: 128.50 + 3*dLng},
	}
	stops := make([]Stop, len(pts))
	for i, p := range pts {
		stops[i] = Stop{ID: string(rune('a' + i)), P: p, Kind: KindMetro}
	}
	pats := []Pattern{
		{RouteNo: "A", Kind: KindMetro, FirstMin: 360, LastMin: 1380, Headway: [3]int{hwA, hwA, hwA}, Stops: []int{0, 1, 2}, RunSec: []int{0, 120, 120}},
		{RouteNo: "B", Kind: KindMetro, FirstMin: 360, LastMin: 1380, Headway: [3]int{6, 6, 6}, Stops: []int{2, 3}, RunSec: []int{0, 60}},
	}
	return NewNetwork(stops, pats), pts
}

func q(clock int) Query { return Query{Day: Weekday, ClockMin: clock, Params: DefaultParams()} }

// T1, T4: hand-computed arrival times; riding on through stop 1 waits once.
func TestFrom_HandComputed(t *testing.T) {
	n, pts := testNet(10)
	r := n.From(pts[0], q(8*60))
	want := []int32{0, 300 + 120, 300 + 240, 540 + 180 + 60, 780 + 351}
	for s, w := range want {
		if r.sec[s] != w {
			t.Errorf("stop %d: %d s, want %d", s, r.sec[s], w)
		}
	}
	if got := r.Seconds(pts[4]); got != 1131 {
		t.Errorf("Seconds(stop 4) = %d, want 1131", got)
	}
}

// T2: after the last departure only walking is left.
func TestFrom_AfterLastDeparture(t *testing.T) {
	n, pts := testNet(10)
	r := n.From(pts[0], q(23*60+30))
	if r.sec[1] != unreached {
		t.Errorf("stop 1 reached after last bus: %d s", r.sec[1])
	}
	if got := r.Seconds(pts[1]); got != int(q(0).walkSec(area.Haversine(pts[0], pts[1]))) {
		t.Errorf("Seconds(stop 1) = %d, want walking time", got)
	}
}

// T3: unknown headway falls back to the default (20 min → 600 s wait).
func TestFrom_DefaultHeadway(t *testing.T) {
	n, pts := testNet(0)
	r := n.From(pts[0], q(8*60))
	if r.sec[1] != 600+120 {
		t.Errorf("stop 1: %d s, want 720", r.sec[1])
	}
	if !n.MissingHeadway(Weekday) {
		t.Error("MissingHeadway = false, want true")
	}
}

// T5: travel time from a to b is the same searched forward from a or
// backward from b.
func TestToMatchesFrom(t *testing.T) {
	n, pts := testNet(10)
	fwd := n.From(pts[0], q(8*60)).Seconds(pts[4])
	rev := n.To(pts[4], q(8*60)).Seconds(pts[0])
	if fwd != rev {
		t.Errorf("forward %d s, reverse %d s", fwd, rev)
	}
	if back := n.To(pts[0], q(8*60)).Seconds(pts[4]); back == fwd {
		t.Errorf("4→0 should not ride the one-way routes (got %d s)", back)
	}
}

// T6: every cell is in exactly one band, and the first band holds the
// cells nearest the origin.
func TestIsochrone_BandsDisjoint(t *testing.T) {
	n, pts := testNet(10)
	bands := n.From(pts[0], q(8*60)).Isochrone(250, []int{15, 30, 45, 60})
	seen := map[area.Cell]int{}
	for b, cells := range bands {
		for _, c := range cells {
			if prev, dup := seen[c]; dup {
				t.Fatalf("cell %v in bands %d and %d", c, prev, b)
			}
			seen[c] = b
		}
	}
	if b, ok := seen[area.CellOf(250, pts[0])]; !ok || b != 0 {
		t.Errorf("origin cell band = %d (found %v), want 0", b, ok)
	}
	if b, ok := seen[area.CellOf(250, pts[3])]; !ok || b != 0 {
		t.Errorf("stop 3 cell band = %d (found %v), want 0 (reached in 13 min)", b, ok)
	}
}

// T9: a Daegu-sized synthetic network. Run with -bench to see the time;
// the spec target is 200 ms per search.
func BenchmarkFrom(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	stops := make([]Stop, 5000)
	for i := range stops {
		stops[i] = Stop{P: area.Point{Lat: 35.75 + rng.Float64()*0.25, Lng: 128.45 + rng.Float64()*0.35}, Kind: KindBus}
	}
	pats := make([]Pattern, 400)
	for k := range pats {
		s := make([]int, 60)
		for i := range s {
			s[i] = rng.Intn(len(stops))
		}
		pats[k] = Pattern{FirstMin: -1, LastMin: -1, Headway: [3]int{12, 15, 20}, Stops: s}
	}
	n := NewNetwork(stops, pats)
	origin := area.Point{Lat: 35.87, Lng: 128.60}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n.From(origin, q(8*60)).Isochrone(250, []int{15, 30, 45, 60})
	}
}
