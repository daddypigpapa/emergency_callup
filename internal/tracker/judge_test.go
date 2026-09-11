package tracker

import (
	"testing"

	"emergencycallup/internal/area"
	"emergencycallup/internal/incident"
)

func circleArea(id int64) *area.Row {
	return &area.Row{ID: id, Kind: area.KindCircle, Lat: 35.8714, Lng: 128.6014, RadiusM: 100,
		BBox: area.CircleBBox(area.Point{Lat: 35.8714, Lng: 128.6014}, 100)}
}

func freshLive(state string) *Live {
	return &Live{IncidentID: 1, MemberID: 1, TeamNo: 1, EffAreaID: 1, State: state}
}

const baseTS = int64(1_700_000_000_000)

func TestApplyFix_InvalidFixOnlyUpdatesLast(t *testing.T) {
	l := freshLive(incident.StateMoving)
	f := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 500, TS: baseTS} // acc too poor
	transitioned, events := l.ApplyFix(f, circleArea(1), baseTS)
	if transitioned || len(events) != 0 {
		t.Fatalf("invalid fix should not transition: transitioned=%v events=%v", transitioned, events)
	}
	if l.State != incident.StateMoving {
		t.Errorf("state changed on invalid fix: %s", l.State)
	}
	if !l.HasLastFix || l.LastAcc != 500 {
		t.Error("last_* should still be updated on an invalid fix")
	}
}

func TestApplyFix_FirstValidFixMovesToMoving(t *testing.T) {
	l := freshLive(incident.StateNotified)
	f := Fix{Lat: 35.90, Lng: 128.62, Acc: 10, TS: baseTS} // far from area, still valid
	transitioned, _ := l.ApplyFix(f, circleArea(1), baseTS)
	if !transitioned || l.State != incident.StateMoving {
		t.Fatalf("expected transition to MOVING, got state=%s transitioned=%v", l.State, transitioned)
	}
}

// SPEC §6.2: arrival needs 2 consecutive sd<=0 fixes with gap >= 10s.
func TestApplyFix_ArrivalRequiresTwoConsecutiveFixesWithGap(t *testing.T) {
	l := freshLive(incident.StateMoving)
	ar := circleArea(1)
	inside := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS}

	transitioned, _ := l.ApplyFix(inside, ar, baseTS)
	if transitioned {
		t.Fatal("single inside fix should not confirm arrival")
	}

	tooSoon := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS + 5000}
	transitioned, _ = l.ApplyFix(tooSoon, ar, baseTS+5000)
	if transitioned {
		t.Fatal("second fix within 10s gap should not confirm arrival yet")
	}
	if l.State != incident.StateMoving {
		t.Errorf("state should still be MOVING, got %s", l.State)
	}

	enoughGap := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS + 11000}
	transitioned, events := l.ApplyFix(enoughGap, ar, baseTS+11000)
	if !transitioned || l.State != incident.StateArrived {
		t.Fatalf("expected ARRIVED after gap>=10s, got state=%s transitioned=%v", l.State, transitioned)
	}
	if len(events) != 1 || events[0] != EventArrived {
		t.Errorf("expected arrived event, got %v", events)
	}
	if l.ArrivedAt != baseTS {
		t.Errorf("arrived_at should be the FIRST qualifying fix's ts (%d), got %d", baseTS, l.ArrivedAt)
	}
}

// A breaking fix (sd>0) in the middle resets the streak (SPEC: "중간에 조건이 끊기면 연속 계수 초기화").
func TestApplyFix_ArrivalStreakResetsOnBreak(t *testing.T) {
	l := freshLive(incident.StateMoving)
	ar := circleArea(1)
	inside := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS}
	l.ApplyFix(inside, ar, baseTS)

	outside := Fix{Lat: 35.95, Lng: 128.6014, Acc: 10, TS: baseTS + 5000}
	l.ApplyFix(outside, ar, baseTS+5000)

	backInside := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS + 20000}
	transitioned, _ := l.ApplyFix(backInside, ar, baseTS+20000)
	if transitioned {
		t.Fatal("streak should have reset; a single inside fix after a break must not confirm arrival")
	}
}

// SPEC §6.2: departure needs 2 consecutive sd > max(30,acc) fixes, gap >= 30s.
func TestApplyFix_DepartureRequiresTwoFixesWithGap(t *testing.T) {
	l := freshLive(incident.StateArrived)
	l.ArrivedAt = baseTS
	ar := circleArea(1)

	// ~150m from center (100m-radius circle) -> sd ~ 50m > max(30, acc=10).
	far := Fix{Lat: 35.8714 + 150.0/111320.0, Lng: 128.6014, Acc: 10, TS: baseTS}
	transitioned, _ := l.ApplyFix(far, ar, baseTS)
	if transitioned {
		t.Fatal("single departure fix should not confirm")
	}

	tooSoon := Fix{Lat: far.Lat, Lng: far.Lng, Acc: 10, TS: baseTS + 10000}
	transitioned, _ = l.ApplyFix(tooSoon, ar, baseTS+10000)
	if transitioned {
		t.Fatal("second fix within 30s should not confirm departure")
	}

	enoughGap := Fix{Lat: far.Lat, Lng: far.Lng, Acc: 10, TS: baseTS + 31000}
	transitioned, events := l.ApplyFix(enoughGap, ar, baseTS+31000)
	if !transitioned || l.State != incident.StateLeft {
		t.Fatalf("expected LEFT after gap>=30s, got state=%s", l.State)
	}
	if len(events) != 1 || events[0] != EventLeft {
		t.Errorf("expected left event, got %v", events)
	}
	if l.LeftCount != 1 {
		t.Errorf("left_count = %d, want 1", l.LeftCount)
	}
}

func TestApplyFix_ReturnedFromLeft(t *testing.T) {
	l := freshLive(incident.StateLeft)
	ar := circleArea(1)
	inside1 := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS}
	l.ApplyFix(inside1, ar, baseTS)
	inside2 := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS + 11000}
	transitioned, events := l.ApplyFix(inside2, ar, baseTS+11000)
	if !transitioned || l.State != incident.StateArrived {
		t.Fatalf("expected re-ARRIVED, got %s", l.State)
	}
	if len(events) != 1 || events[0] != EventReturned {
		t.Errorf("expected returned event, got %v", events)
	}
}

// SPEC §6.4: speed > 55 m/s marks suspect and excludes the fix from judgement.
func TestApplyFix_SuspectSpeedExcludedFromJudgement(t *testing.T) {
	l := freshLive(incident.StateMoving)
	ar := circleArea(1)
	// Establish a "good" baseline far from the area.
	l.ApplyFix(Fix{Lat: 35.95, Lng: 128.62, Acc: 10, TS: baseTS}, ar, baseTS)

	// 1 second later, ~10km away -> ~10000 m/s, absurd.
	jump := Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS + 1000}
	transitioned, events := l.ApplyFix(jump, ar, baseTS+1000)
	if transitioned {
		t.Fatal("suspect jump must not drive a state transition")
	}
	if !l.Suspect {
		t.Fatal("expected suspect=true after implausible jump")
	}
	if len(events) != 1 || events[0] != EventSuspect {
		t.Errorf("expected suspect event, got %v", events)
	}
}

func TestApplyFix_SuspectClearsAfterTwoGoodFixes(t *testing.T) {
	l := freshLive(incident.StateMoving)
	ar := circleArea(1)
	l.ApplyFix(Fix{Lat: 35.95, Lng: 128.62, Acc: 10, TS: baseTS}, ar, baseTS)
	l.ApplyFix(Fix{Lat: 35.8714, Lng: 128.6014, Acc: 10, TS: baseTS + 1000}, ar, baseTS+1000) // suspect
	if !l.Suspect {
		t.Fatal("expected suspect after jump")
	}

	// Two consecutive plausible fixes near the suspect position.
	l.ApplyFix(Fix{Lat: 35.8714, Lng: 128.6015, Acc: 10, TS: baseTS + 16000}, ar, baseTS+16000)
	if !l.Suspect {
		t.Fatal("should still be suspect after only 1 good fix")
	}
	l.ApplyFix(Fix{Lat: 35.8714, Lng: 128.6015, Acc: 10, TS: baseTS + 31000}, ar, baseTS+31000)
	if l.Suspect {
		t.Fatal("should clear suspect after 2 consecutive good fixes")
	}
}

func TestApplyFix_OutOfKoreaIsSuspect(t *testing.T) {
	l := freshLive(incident.StateMoving)
	ar := circleArea(1)
	f := Fix{Lat: 10.0, Lng: 128.0, Acc: 10, TS: baseTS}
	transitioned, events := l.ApplyFix(f, ar, baseTS)
	if transitioned {
		t.Fatal("out-of-Korea fix must not drive a transition")
	}
	if !l.Suspect || len(events) != 1 || events[0] != EventSuspect {
		t.Errorf("expected suspect flag+event, got suspect=%v events=%v", l.Suspect, events)
	}
}

func TestApplyMissionChange_AreaChangeRevertsArrivedToMoving(t *testing.T) {
	l := freshLive(incident.StateArrived)
	l.ArrivedAt = baseTS
	l.ApplyMissionChange("새임무", 99, 2)
	if l.State != incident.StateMoving {
		t.Errorf("state = %s, want MOVING after area change", l.State)
	}
	if l.ArrivedAt != 0 {
		t.Errorf("arrived_at should be cleared, got %d", l.ArrivedAt)
	}
}

func TestApplyMissionChange_MissionOnlyKeepsState(t *testing.T) {
	l := freshLive(incident.StateArrived)
	l.EffAreaID = 5
	l.ArrivedAt = baseTS
	l.ApplyMissionChange("새임무", 5, 2)
	if l.State != incident.StateArrived {
		t.Errorf("state = %s, want ARRIVED unchanged (mission-only change)", l.State)
	}
	if l.ArrivedAt != baseTS {
		t.Error("arrived_at should be preserved on a mission-only change")
	}
}
