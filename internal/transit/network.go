// Package transit estimates bus/metro travel times on stored public route
// data (docs/SPEC_TRANSIT.md): reachability bands from a point (§5.4 가)
// and per-member arrival estimates to a rally point (§5.4 나).
//
// The model is frequency based: a ride costs an average wait (headway ×
// WaitFactor) plus hop times along the route. The query's clock time only
// picks the day's headway, the bus speed band (peak/off-peak) and which
// routes are running; travel itself is measured as elapsed seconds.
// ponytail: elapsed-time model, so a trip that crosses rush hour or the last
// bus is not re-timed mid-way; switch to clock-time labels if that matters.
package transit

import (
	"math"

	"emergencycallup/internal/area"
)

// Kind is the vehicle type of a stop or pattern.
const (
	KindBus   = "bus"
	KindMetro = "metro"
)

// Day selects which headway column applies.
type Day int

const (
	Weekday Day = iota
	Saturday
	Sunday
)

// Stop is a bus stop or metro station.
type Stop struct {
	ID   string
	Name string
	P    area.Point
	Kind string
}

// Pattern is one direction of one route: an ordered stop list.
type Pattern struct {
	RouteID  string
	RouteNo  string
	Kind     string
	FirstMin int    // first departure, minutes after midnight; -1 = unknown
	LastMin  int    // last departure; -1 = unknown
	Headway  [3]int // minutes, indexed by Day; 0 = unknown
	Stops    []int  // indices into Network.Stops
	RunSec   []int  // RunSec[i]: seconds from Stops[i-1] to Stops[i]; nil or 0 = estimate from distance
}

// Params are the calibration values of SPEC_TRANSIT §5.3.
type Params struct {
	WalkKmh        float64
	BusKmhPeak     float64
	BusKmhOffpeak  float64
	WaitFactor     float64
	DefaultHeadway int // minutes, used when a pattern's headway is unknown
}

// DefaultParams returns the §5.3 defaults (estimates, to be calibrated).
func DefaultParams() Params {
	return Params{WalkKmh: 4, BusKmhPeak: 15, BusKmhOffpeak: 20, WaitFactor: 0.5, DefaultHeadway: 20}
}

const (
	walkLinkM    = 400  // transfer walk between stops
	accessM      = 800  // walk from a point to a stop (and stop to a point)
	detour       = 1.3  // straight line → street distance
	dwellSec     = 20   // per bus stop
	maxRides     = 4    // up to 3 transfers
	defaultFirst = 330  // 05:30, when a pattern's first departure is unknown
	defaultLast  = 1380 // 23:00
	bucketSize   = 1000 // spatial index cell (m); must exceed accessM
)

type patRef struct{ pat, idx int }

type walkEdge struct {
	to    int
	distM float64
}

// Network is the in-memory route graph. Build it once per data sync.
type Network struct {
	Stops    []Stop
	Patterns []Pattern

	reversed []Pattern // same routes, stop order reversed (for searches toward a target)
	fwdAt    [][]patRef
	revAt    [][]patRef
	walk     [][]walkEdge
	buckets  map[area.Cell][]int
}

// NewNetwork indexes stops and patterns and links stops within walking
// distance of each other.
func NewNetwork(stops []Stop, patterns []Pattern) *Network {
	n := &Network{Stops: stops, Patterns: patterns, buckets: map[area.Cell][]int{}}
	for i, s := range stops {
		c := area.CellOf(bucketSize, s.P)
		n.buckets[c] = append(n.buckets[c], i)
	}
	n.walk = make([][]walkEdge, len(stops))
	for i, s := range stops {
		n.near(s.P, walkLinkM, func(j int, d float64) {
			if j != i {
				n.walk[i] = append(n.walk[i], walkEdge{j, d})
			}
		})
	}
	n.reversed = make([]Pattern, len(patterns))
	for k, p := range patterns {
		r := p
		m := len(p.Stops)
		r.Stops = make([]int, m)
		for i := range p.Stops {
			r.Stops[i] = p.Stops[m-1-i]
		}
		if p.RunSec != nil {
			r.RunSec = make([]int, m)
			for i := 1; i < m; i++ {
				r.RunSec[i] = p.RunSec[m-i]
			}
		}
		n.reversed[k] = r
	}
	n.fwdAt = stopIndex(len(stops), patterns)
	n.revAt = stopIndex(len(stops), n.reversed)
	return n
}

func stopIndex(nStops int, pats []Pattern) [][]patRef {
	at := make([][]patRef, nStops)
	for k, p := range pats {
		for i, s := range p.Stops {
			at[s] = append(at[s], patRef{k, i})
		}
	}
	return at
}

// near calls fn for every stop within radiusM of p.
func (n *Network) near(p area.Point, radiusM float64, fn func(stop int, distM float64)) {
	c := area.CellOf(bucketSize, p)
	for di := int64(-1); di <= 1; di++ {
		for dj := int64(-1); dj <= 1; dj++ {
			for _, s := range n.buckets[area.Cell{I: c.I + di, J: c.J + dj}] {
				if d := area.Haversine(p, n.Stops[s].P); d <= radiusM {
					fn(s, d)
				}
			}
		}
	}
}

// MissingHeadway reports whether any pattern lacks a headway for day, so
// the default was used somewhere (SPEC_TRANSIT §6 missingHeadway).
func (n *Network) MissingHeadway(day Day) bool {
	for _, p := range n.Patterns {
		if p.Headway[day] == 0 {
			return true
		}
	}
	return false
}

// Query fixes the conditions of one search.
type Query struct {
	Day      Day
	ClockMin int // minutes after midnight
	Params   Params
	LimitSec int // stop searching beyond this; 0 = 90 minutes
}

func (q Query) limit() int32 {
	if q.LimitSec > 0 {
		return int32(q.LimitSec)
	}
	return 90 * 60
}

func (q Query) walkSec(distM float64) int32 {
	return int32(math.Round(distM * detour / (q.Params.WalkKmh / 3.6)))
}

func (q Query) busKmh() float64 {
	c := q.ClockMin
	if (c >= 7*60 && c < 9*60) || (c >= 17*60 && c < 19*60) {
		return q.Params.BusKmhPeak
	}
	return q.Params.BusKmhOffpeak
}

const unreached = math.MaxInt32

// Result holds the best elapsed seconds per stop for one search.
type Result struct {
	n      *Network
	q      Query
	anchor area.Point
	sec    []int32 // forward: anchor → stop; reverse: stop → anchor
}

// From searches outward from origin (reachability, §5.4 가).
func (n *Network) From(origin area.Point, q Query) *Result {
	return n.search(origin, q, n.Patterns, n.fwdAt)
}

// To searches backward from target (arrival estimates, §5.4 나): the
// result's Seconds(p) is the travel time from p to target.
func (n *Network) To(target area.Point, q Query) *Result {
	return n.search(target, q, n.reversed, n.revAt)
}

// search is RAPTOR over elapsed seconds: round k allows k rides.
func (n *Network) search(anchor area.Point, q Query, pats []Pattern, at [][]patRef) *Result {
	limit := q.limit()
	best := make([]int32, len(n.Stops))
	for i := range best {
		best[i] = unreached
	}
	marked := make([]bool, len(n.Stops))
	var list []int
	mark := func(s int) {
		if !marked[s] {
			marked[s] = true
			list = append(list, s)
		}
	}
	n.near(anchor, accessM, func(s int, d float64) {
		if t := q.walkSec(d); t < best[s] {
			best[s] = t
			mark(s)
		}
	})
	n.relaxWalk(q, best, list, mark, limit)

	prev := make([]int32, len(best))
	for round := 1; round <= maxRides && len(list) > 0; round++ {
		copy(prev, best)
		// earliest marked position per pattern
		start := map[int]int{}
		for _, s := range list {
			marked[s] = false
			for _, r := range at[s] {
				if i, ok := start[r.pat]; !ok || r.idx < i {
					start[r.pat] = r.idx
				}
			}
		}
		list = list[:0]
		for k, i0 := range start {
			p := &pats[k]
			wait, ok := q.wait(p)
			if !ok {
				continue
			}
			ride := int32(unreached)
			for i := i0; i < len(p.Stops); i++ {
				s := p.Stops[i]
				if ride != unreached {
					ride += q.hop(n, p, i)
					if ride <= limit && ride < best[s] {
						best[s] = ride
						mark(s)
					}
				}
				if prev[s] != unreached && prev[s]+wait < ride {
					ride = prev[s] + wait
				}
			}
		}
		n.relaxWalk(q, best, append([]int(nil), list...), mark, limit)
	}
	return &Result{n: n, q: q, anchor: anchor, sec: best}
}

// relaxWalk applies one transfer walk from each stop in from.
func (n *Network) relaxWalk(q Query, best []int32, from []int, mark func(int), limit int32) {
	for _, s := range from {
		for _, e := range n.walk[s] {
			if t := best[s] + q.walkSec(e.distM); t <= limit && t < best[e.to] {
				best[e.to] = t
				mark(e.to)
			}
		}
	}
}

// wait returns the boarding wait for p, or false if p isn't running at the
// query's clock time.
func (q Query) wait(p *Pattern) (int32, bool) {
	first, last := p.FirstMin, p.LastMin
	if first < 0 {
		first = defaultFirst
	}
	if last < 0 {
		last = defaultLast
	}
	if q.ClockMin < first || q.ClockMin > last {
		return 0, false
	}
	hw := p.Headway[q.Day]
	if hw == 0 {
		hw = q.Params.DefaultHeadway
	}
	return int32(math.Round(float64(hw) * 60 * q.Params.WaitFactor)), true
}

// hop is the ride time from p.Stops[i-1] to p.Stops[i].
func (q Query) hop(n *Network, p *Pattern, i int) int32 {
	if p.RunSec != nil && p.RunSec[i] > 0 {
		return int32(p.RunSec[i])
	}
	d := area.Haversine(n.Stops[p.Stops[i-1]].P, n.Stops[p.Stops[i]].P) * detour
	return int32(math.Round(d/(q.busKmh()/3.6))) + dwellSec
}

// Seconds is the travel time between the search anchor and p (walking all
// the way counts too), or -1 if p is out of range.
func (r *Result) Seconds(p area.Point) int {
	t := int32(unreached)
	if d := area.Haversine(r.anchor, p); d <= accessM*5 {
		t = r.q.walkSec(d)
	}
	r.n.near(p, accessM, func(s int, d float64) {
		if r.sec[s] != unreached {
			if v := r.sec[s] + r.q.walkSec(d); v < t {
				t = v
			}
		}
	})
	if t > r.q.limit() {
		return -1
	}
	return int(t)
}

// Isochrone returns, per band (minutes, ascending), the grid cells first
// reached within that band. A cell appears in exactly one band.
func (r *Result) Isochrone(size int, bandsMin []int) [][]area.Cell {
	maxSec := int32(bandsMin[len(bandsMin)-1] * 60)
	cells := map[area.Cell]int32{}
	spread := func(from area.Point, base int32, radiusM int) {
		cs, err := area.CellsWithinRadius(size, from, radiusM)
		if err != nil {
			return
		}
		for _, c := range cs {
			t := base + r.q.walkSec(area.Haversine(from, area.CellCenter(size, c)))
			if t > maxSec {
				continue
			}
			if old, ok := cells[c]; !ok || t < old {
				cells[c] = t
			}
		}
	}
	walkOnlyM := int(float64(maxSec) * r.q.Params.WalkKmh / 3.6 / detour)
	spread(r.anchor, 0, walkOnlyM)
	for s, t := range r.sec {
		if t < maxSec {
			spread(r.n.Stops[s].P, t, accessM)
		}
	}
	out := make([][]area.Cell, len(bandsMin))
	for c, t := range cells {
		for b, m := range bandsMin {
			if t <= int32(m*60) {
				out[b] = append(out[b], c)
				break
			}
		}
	}
	for b := range out {
		out[b] = area.DedupeSortCells(out[b])
	}
	return out
}
