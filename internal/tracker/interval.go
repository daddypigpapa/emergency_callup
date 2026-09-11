package tracker

import (
	"math"
	"sync"
	"time"

	"emergencycallup/internal/incident"
)

// Base intervals from SPEC §6.1's table (seconds), before the overload
// doubling.
const (
	nArrivedDeep = 60
	nNear        = 10
	nDefault     = 15
)

// NextInterval implements SPEC §6.1: evaluate the table top-to-bottom, use
// the first matching row, then apply the overload multiplier last.
//
//  1. ARRIVED and sd <= -30m (well inside the boundary)      -> 60
//  2. |sd| <= max(2*rEq, 200m)                                -> 10
//  3. otherwise                                                -> 15
func NextInterval(state string, sd, rEq float64, overloaded bool) int {
	var n int
	switch {
	case state == incident.StateArrived && sd <= -30:
		n = nArrivedDeep
	case math.Abs(sd) <= math.Max(2*rEq, 200):
		n = nNear
	default:
		n = nDefault
	}
	if overloaded {
		n *= 2
	}
	return n
}

// LoadMonitor tracks recent request latencies and throughput to decide the
// SPEC §6.1 overload flag: "요청 처리 p95 > 200ms 또는 초당 요청 > 150".
// It's a small ring buffer, safe for concurrent use from every /f/fix
// request handler.
type LoadMonitor struct {
	mu        sync.Mutex
	latencies []time.Duration // ring buffer of recent request durations
	next      int
	filled    bool
	reqTimes  []time.Time // ring buffer of recent request timestamps, for req/s
	reqNext   int
	reqFilled bool
}

const (
	latencyWindowSize = 200 // recent requests sampled for p95
	reqTimeWindowSize = 300 // enough to cover >150 req/s for a full second plus margin
)

func NewLoadMonitor() *LoadMonitor {
	return &LoadMonitor{
		latencies: make([]time.Duration, latencyWindowSize),
		reqTimes:  make([]time.Time, reqTimeWindowSize),
	}
}

// Record logs one completed request's latency and arrival time.
func (m *LoadMonitor) Record(latency time.Duration, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latencies[m.next] = latency
	m.next = (m.next + 1) % latencyWindowSize
	if m.next == 0 {
		m.filled = true
	}
	m.reqTimes[m.reqNext] = at
	m.reqNext = (m.reqNext + 1) % reqTimeWindowSize
	if m.reqNext == 0 {
		m.reqFilled = true
	}
}

// Overloaded reports whether the server currently meets SPEC §6.1's
// overload condition.
func (m *LoadMonitor) Overloaded(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := latencyWindowSize
	if !m.filled {
		n = m.next
	}
	if n >= 20 { // need a reasonable sample before trusting p95
		buf := make([]time.Duration, n)
		copy(buf, m.latencies[:n])
		p95 := percentile(buf, 0.95)
		if p95 > 200*time.Millisecond {
			return true
		}
	}

	count := 0
	limit := reqTimeWindowSize
	if !m.reqFilled {
		limit = m.reqNext
	}
	for i := 0; i < limit; i++ {
		if now.Sub(m.reqTimes[i]) <= time.Second {
			count++
		}
	}
	return count > 150
}

func percentile(buf []time.Duration, p float64) time.Duration {
	if len(buf) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), buf...)
	insertionSort(sorted)
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func insertionSort(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		v := d[i]
		j := i - 1
		for j >= 0 && d[j] > v {
			d[j+1] = d[j]
			j--
		}
		d[j+1] = v
	}
}
