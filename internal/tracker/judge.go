// Package tracker implements the location-processing pipeline: fix
// validation, suspect detection, the arrival/departure state machine, the
// server-directed report interval, and the in-memory + batched-DB state
// described in docs/SPEC.md §6.
package tracker

import (
	"math"

	"emergencycallup/internal/area"
	"emergencycallup/internal/incident"
)

// Fix is one raw location report from a field device.
type Fix struct {
	Lat float64
	Lng float64
	Acc int   // meters
	TS  int64 // client-reported UTC ms
}

// Validity thresholds (SPEC §6.2).
const (
	MaxValidAccM   = 100
	MaxClockPastMs = 120_000
	MaxClockSkewMs = 30_000
)

// IsValidFix reports SPEC §6.2's "유효 위치" check, independent of suspect
// status: accuracy and timestamp only.
func IsValidFix(f Fix, serverNowMs int64) bool {
	if f.Acc > MaxValidAccM {
		return false
	}
	if f.TS < serverNowMs-MaxClockPastMs || f.TS > serverNowMs+MaxClockSkewMs {
		return false
	}
	return true
}

// Suspect thresholds (SPEC §6.4).
const maxSpeedMS = 55.0 // ~200 km/h

// Live is one member's in-memory tracking state for the active incident. It
// mirrors the live-relevant `assignment` columns plus judgement-only
// counters that are never persisted (SPEC §6.5: only state transitions and
// dirty positions get written to SQLite; these counters live purely in
// memory and are safely lost/rebuilt across a restart since the DB state
// they lead to has already been flushed at each transition).
type Live struct {
	IncidentID int64
	MemberID   int64
	TeamNo     int64
	EffAreaID  int64
	EffMission string
	MissionVer int
	AckVer     int

	State        string
	FirstLoginAt int64 // 0 = unset
	ArrivedAt    int64 // 0 = unset
	LastLeftAt   int64 // 0 = unset
	LeftCount    int

	LastLat5  int64 // lat * 1e5, 0 if never set
	LastLng5  int64
	LastAcc   int64
	LastFixAt int64 // 0 = never
	Suspect   bool

	HasLastFix bool // distinguishes "never reported" from lat5=0

	// Judgement-only counters (not persisted).
	arriveCount   int
	arriveFirstTS int64
	leaveCount    int
	leaveFirstTS  int64
	suspectClear  int
	hasLastValid  bool
	lastValidLat  float64
	lastValidLng  float64
	lastValidTS   int64

	Dirty bool // needs a batched (non-transition) DB flush

	// seqAt is the global Tracker seq value at which this row last changed
	// in a way the admin delta feed must see. Owned by Tracker, always
	// mutated under Tracker.mu.
	seqAt uint64
}

// Event names appended to the `event` table by the caller.
const (
	EventArrived  = "arrived"
	EventLeft     = "left"
	EventReturned = "returned"
	EventSuspect  = "suspect_fix"
)

// NewLive builds a Live from a freshly (re)loaded assignment row.
func NewLive(a *incident.Assignment) *Live {
	l := &Live{
		IncidentID: a.IncidentID, MemberID: a.MemberID, TeamNo: a.TeamNo,
		EffAreaID: a.EffAreaID, EffMission: a.EffMission,
		MissionVer: a.MissionVer, AckVer: a.AckVer, State: a.State,
		LeftCount: a.LeftCount, Suspect: a.Suspect,
	}
	if a.FirstLoginAt != nil {
		l.FirstLoginAt = *a.FirstLoginAt
	}
	if a.ArrivedAt != nil {
		l.ArrivedAt = *a.ArrivedAt
	}
	if a.LastLeftAt != nil {
		l.LastLeftAt = *a.LastLeftAt
	}
	if a.LastLat5 != nil {
		l.LastLat5 = *a.LastLat5
		l.HasLastFix = true
	}
	if a.LastLng5 != nil {
		l.LastLng5 = *a.LastLng5
	}
	if a.LastAcc != nil {
		l.LastAcc = *a.LastAcc
	}
	if a.LastFixAt != nil {
		l.LastFixAt = *a.LastFixAt
	}
	return l
}

// MarkLoggedIn implements SPEC §5.1: NOTIFIED -> LOGGED_IN on the first
// authenticated request of this incident, regardless of location.
func (l *Live) MarkLoggedIn(nowMs int64) {
	if l.State == incident.StateNotified {
		l.State = incident.StateLoggedIn
		l.FirstLoginAt = nowMs
		l.Dirty = true
	} else if l.FirstLoginAt == 0 {
		l.FirstLoginAt = nowMs
		l.Dirty = true
	}
}

// MarkLocDenied records a location-permission denial (POST /f/consent
// {granted:false}).
func (l *Live) MarkLocDenied() {
	if l.State == incident.StateNotified || l.State == incident.StateLoggedIn {
		l.State = incident.StateLocDenied
		l.Dirty = true
	}
}

func toLat5(v float64) int64 { return int64(math.Round(v * 1e5)) }

// ApplyFix runs one incoming fix through validation, suspect detection, and
// the §6.2 state machine. It always updates LastLat5/Lng5/Acc/FixAt (per
// SPEC: "유효하지 않은 위치는 last_*만 갱신"). It returns the events to
// audit-log and whether a transition happened (meaning: flush now,
// synchronously, per SPEC §6.3 step 5).
func (l *Live) ApplyFix(f Fix, ar *area.Row, serverNowMs int64) (transitioned bool, events []string) {
	l.LastLat5 = toLat5(f.Lat)
	l.LastLng5 = toLat5(f.Lng)
	l.LastAcc = int64(f.Acc)
	l.LastFixAt = serverNowMs
	l.HasLastFix = true
	l.Dirty = true

	if !IsValidFix(f, serverNowMs) {
		return false, nil
	}

	pt := area.Point{Lat: f.Lat, Lng: f.Lng}

	// Suspect detection (SPEC §6.4): speed is measured against the
	// immediately preceding *valid* fix, chain-wise — a flagged fix still
	// becomes the reference point for the next one. This matches "직전
	// 유효 위치와의 이동 속도": only the anomalous fix itself is excluded
	// from judgement below; once the chain settles, later fixes are judged
	// normally even before the suspect flag has fully cleared.
	suspectNow := false
	if !area.InKorea(pt) {
		suspectNow = true
	} else if l.hasLastValid {
		dtSec := float64(f.TS-l.lastValidTS) / 1000.0
		if dtSec > 0 {
			dist := area.Haversine(area.Point{Lat: l.lastValidLat, Lng: l.lastValidLng}, pt)
			speed := dist / dtSec
			if speed > maxSpeedMS {
				suspectNow = true
			}
		}
	}
	l.lastValidLat, l.lastValidLng, l.lastValidTS = f.Lat, f.Lng, f.TS
	l.hasLastValid = true

	if suspectNow {
		wasSuspect := l.Suspect
		l.Suspect = true
		l.suspectClear = 0
		if !wasSuspect {
			events = append(events, EventSuspect)
		}
		return false, events
	}

	// Not suspect itself: count toward clearing a standing suspect flag
	// (SPEC §6.4: "다음 정상 위치 2건이 연속으로 오면 suspect=0"), but still
	// use it for state-machine judgement below — it is not the anomalous
	// reading.
	if l.Suspect {
		l.suspectClear++
		if l.suspectClear >= 2 {
			l.Suspect = false
			l.suspectClear = 0
		}
	}

	// SPEC §5.1: any valid position moves NOTIFIED/LOGGED_IN/LOC_DENIED to MOVING.
	if l.State == incident.StateNotified || l.State == incident.StateLoggedIn || l.State == incident.StateLocDenied {
		l.State = incident.StateMoving
		transitioned = true
	}

	sd := ar.SignedDistance(pt)

	switch l.State {
	case incident.StateMoving, incident.StateLeft:
		if sd <= 0 {
			if l.arriveCount == 0 {
				l.arriveCount = 1
				l.arriveFirstTS = f.TS
			} else {
				if f.TS-l.arriveFirstTS >= 10_000 {
					wasLeft := l.State == incident.StateLeft
					l.State = incident.StateArrived
					l.ArrivedAt = l.arriveFirstTS
					l.arriveCount = 0
					l.leaveCount = 0
					transitioned = true
					if wasLeft {
						events = append(events, EventReturned)
					} else {
						events = append(events, EventArrived)
					}
				} else {
					l.arriveCount++
				}
			}
		} else {
			l.arriveCount = 0
		}
	case incident.StateArrived:
		threshold := math.Max(30, float64(f.Acc))
		if sd > threshold {
			if l.leaveCount == 0 {
				l.leaveCount = 1
				l.leaveFirstTS = f.TS
			} else {
				if f.TS-l.leaveFirstTS >= 30_000 {
					l.State = incident.StateLeft
					l.LastLeftAt = f.TS
					l.LeftCount++
					l.leaveCount = 0
					l.arriveCount = 0
					transitioned = true
					events = append(events, EventLeft)
				} else {
					l.leaveCount++
				}
			}
		} else {
			l.leaveCount = 0
		}
	}

	return transitioned, events
}

// ApplyMissionChange resets judgement counters when an admin change alters
// this member's effective area (SPEC §5.3 rule 3): ARRIVED/LEFT revert to
// MOVING and arrived_at clears. Mission-only changes leave state untouched.
func (l *Live) ApplyMissionChange(effMission string, effAreaID int64, missionVer int) {
	areaChanged := effAreaID != l.EffAreaID
	l.EffMission = effMission
	l.EffAreaID = effAreaID
	l.MissionVer = missionVer
	l.Dirty = true
	if areaChanged && (l.State == incident.StateArrived || l.State == incident.StateLeft) {
		l.State = incident.StateMoving
		l.ArrivedAt = 0
		l.arriveCount = 0
		l.leaveCount = 0
	}
}
