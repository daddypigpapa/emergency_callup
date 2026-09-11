package tracker

import (
	"testing"
	"time"

	"emergencycallup/internal/incident"
)

func TestNextInterval_Table(t *testing.T) {
	cases := []struct {
		name  string
		state string
		sd    float64
		rEq   float64
		want  int
	}{
		{"arrived deep inside", incident.StateArrived, -40, 100, 60},
		{"arrived but near boundary (not deep)", incident.StateArrived, -10, 100, 10}, // falls into "near" band
		{"near boundary moving", incident.StateMoving, 50, 100, 10},
		{"far away moving", incident.StateMoving, 5000, 100, 15},
		{"left far away", incident.StateLeft, 1000, 50, 15},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NextInterval(c.state, c.sd, c.rEq, false)
			if got != c.want {
				t.Errorf("NextInterval(%s, sd=%f, rEq=%f) = %d, want %d", c.state, c.sd, c.rEq, got, c.want)
			}
		})
	}
}

func TestNextInterval_OverloadDoubles(t *testing.T) {
	got := NextInterval(incident.StateMoving, 5000, 100, true)
	if got != 30 {
		t.Errorf("overloaded far-away interval = %d, want 30", got)
	}
}

func TestNextInterval_NearBandUsesMax2ReqOr200(t *testing.T) {
	// rEq=200 -> near band is |sd|<=400; rEq=50 -> near band is |sd|<=200 (floor).
	if got := NextInterval(incident.StateMoving, 350, 200, false); got != 10 {
		t.Errorf("sd=350 rEq=200 (band=400): got %d, want 10", got)
	}
	if got := NextInterval(incident.StateMoving, 250, 50, false); got != 15 {
		t.Errorf("sd=250 rEq=50 (band=200 floor): got %d, want 15", got)
	}
}

func TestLoadMonitor_HighLatencyTriggersOverload(t *testing.T) {
	m := NewLoadMonitor()
	now := time.Now()
	for i := 0; i < 30; i++ {
		m.Record(300*time.Millisecond, now)
	}
	if !m.Overloaded(now) {
		t.Error("expected overloaded due to high p95 latency")
	}
}

func TestLoadMonitor_HighThroughputTriggersOverload(t *testing.T) {
	m := NewLoadMonitor()
	now := time.Now()
	for i := 0; i < 200; i++ {
		m.Record(5*time.Millisecond, now)
	}
	if !m.Overloaded(now) {
		t.Error("expected overloaded due to >150 req/s")
	}
}

func TestLoadMonitor_NormalLoadNotOverloaded(t *testing.T) {
	m := NewLoadMonitor()
	now := time.Now()
	for i := 0; i < 30; i++ {
		m.Record(5*time.Millisecond, now.Add(-time.Duration(i)*time.Second))
	}
	if m.Overloaded(now) {
		t.Error("expected not overloaded under normal light load")
	}
}
