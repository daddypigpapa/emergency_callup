package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"emergencycallup/internal/area"
	"emergencycallup/internal/audit"
	"emergencycallup/internal/incident"
	"emergencycallup/internal/tracker"
)

func areaJSON(a *area.Row) map[string]any {
	var polygon any
	if a.Kind == "polygon" {
		poly := make([][2]float64, len(a.Polygon))
		for i, p := range a.Polygon {
			poly[i] = [2]float64{p.Lat, p.Lng}
		}
		polygon = poly
	}
	var r any
	if a.Kind == "circle" {
		r = a.RadiusM
	}
	size, cells := gridFieldsJSON(a)
	return map[string]any{
		"name": a.Name, "kind": a.Kind, "lat": a.Lat, "lng": a.Lng, "r": r,
		"polygon": polygon, "size": size, "cells": cells,
		"nav": [2]float64{a.NavLat, a.NavLng}, "bbox": a.BBox,
	}
}

func (s *Server) teamName(ctx context.Context, teamNo int64) string {
	var name string
	_ = s.DB.QueryRowContext(ctx, `SELECT name FROM team WHERE no = ?`, teamNo).Scan(&name)
	return name
}

// handleMemberMe implements GET /f/me (SPEC §7.2).
func (s *Server) handleMemberMe(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	m := memberFromContext(r.Context())
	ctx := r.Context()

	inc, err := s.Incidents.GetActive(ctx)
	if errors.Is(err, incident.ErrNoActiveIncident) {
		st := incident.StatusIdle
		if closedInc, cerr := s.Incidents.LatestClosedIncidentForMember(ctx, m.ID); cerr == nil &&
			now.UnixMilli()-closedInc.ClosedAt <= 12*60*60*1000 {
			st = incident.StatusClosed
		}
		writeJSON(w, map[string]any{"t": now.UnixMilli(), "incident": nil, "member": nil, "task": nil, "st": st, "n": 60})
		return
	}
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}

	asg, err := s.Incidents.GetAssignment(ctx, inc.ID, m.ID)
	if errors.Is(err, incident.ErrAssignmentMissing) {
		writeJSON(w, map[string]any{"t": now.UnixMilli(), "incident": nil, "member": nil, "task": nil, "st": incident.StatusNone, "n": 60})
		return
	}
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}

	// First authenticated request of this incident -> LOGGED_IN (SPEC §5.1, R17).
	live, ok := s.Tracker.GetLive(m.ID)
	if ok && live.State == incident.StateNotified {
		if err := s.Tracker.MarkLoggedIn(ctx, m.ID, now); err != nil {
			writeError(w, now, "server", "서버 오류가 발생했습니다.")
			return
		}
		live, _ = s.Tracker.GetLive(m.ID)
	}

	teamTask, err := s.Incidents.GetTeamTask(ctx, inc.ID, asg.TeamNo)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	ar, err := s.Areas.GetByID(ctx, asg.EffAreaID)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}

	st := live.State
	if !ok {
		st = asg.State
	}
	n := tracker.NextInterval(st, ar.REq()*10+1000, ar.REq(), false)

	// The team's pre-registered rally point (docs/SPEC_AREA_EDITOR.md §3.5)
	// replaces the area's own nav as the field screen's "가는길" target —
	// but not for a member with a personal area override, since the rally
	// point was snapshotted for the *team's* area, not theirs.
	areaBody := areaJSON(ar)
	if asg.AreaOverrideID == nil {
		areaBody["nav"] = [2]float64{teamTask.RallyLat, teamTask.RallyLng}
	}
	cps := make([][4]any, len(teamTask.Checkpoints))
	for i, cp := range teamTask.Checkpoints {
		cps[i] = [4]any{cp.Seq, cp.Name, cp.Lat, cp.Lng}
	}

	writeJSON(w, map[string]any{
		"t": now.UnixMilli(),
		"incident": map[string]any{
			"id": inc.ID, "type": inc.TypeText, "message": inc.Message, "iv": inc.Version,
			"status": inc.Status, "openedAt": inc.OpenedAt,
		},
		"member": map[string]any{
			"name": m.Name, "dept": m.Dept, "teamNo": asg.TeamNo, "teamName": s.teamName(ctx, asg.TeamNo),
		},
		"task": map[string]any{
			"mission": asg.EffMission, "teamMission": teamTask.Mission,
			"mv": asg.MissionVer, "ackVer": asg.AckVer, "area": areaBody, "cps": cps,
		},
		"st": st,
		"n":  n,
	})
}

type consentRequest struct {
	Granted bool   `json:"granted"`
	TextVer string `json:"textVer"`
}

// handleMemberConsent implements POST /f/consent.
func (s *Server) handleMemberConsent(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	m := memberFromContext(r.Context())
	var req consentRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	eventType := "loc_consent"
	if !req.Granted {
		eventType = "loc_denied"
		_ = s.Tracker.ConsentDenied(r.Context(), m.ID, now)
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: eventType, Actor: audit.ActorMember(m.ID), MemberID: &m.ID,
		Data: map[string]any{"granted": req.Granted, "textVer": req.TextVer}})

	live, _ := s.Tracker.GetLive(m.ID)
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "st": live.State})
}

type fixRequest struct {
	La float64 `json:"la"`
	Lo float64 `json:"lo"`
	Ac int     `json:"ac"`
	Ts int64   `json:"ts"`
}

// handleMemberFix implements POST /f/fix (SPEC §6.3, §7.2).
func (s *Server) handleMemberFix(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	m := memberFromContext(r.Context())
	var req fixRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	if req.La < -90 || req.La > 90 || req.Lo < -180 || req.Lo > 180 || req.Ac < 0 || req.Ac > 100000 {
		writeError(w, now, "invalid", "좌표 값이 올바르지 않습니다.")
		return
	}

	res, err := s.Tracker.ProcessFix(r.Context(), m.ID, tracker.Fix{Lat: req.La, Lng: req.Lo, Acc: req.Ac, TS: req.Ts}, now)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeFixResult(w, now, res)
}

// handleMemberSync implements GET /f/sync.
func (s *Server) handleMemberSync(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	m := memberFromContext(r.Context())
	res, err := s.Tracker.Sync(r.Context(), m.ID, now)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeFixResult(w, now, res)
}

func writeFixResult(w http.ResponseWriter, now time.Time, res *tracker.FixResult) {
	body := map[string]any{"t": now.UnixMilli(), "st": res.Status, "iv": res.IV, "mv": res.MV, "d": res.D, "n": res.N}
	if res.Stop {
		body["stop"] = 1
	}
	writeJSON(w, body)
}

type ackRequest struct {
	MV int `json:"mv"`
}

// handleMemberAck implements POST /f/ack.
func (s *Server) handleMemberAck(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	m := memberFromContext(r.Context())
	var req ackRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	if err := s.Tracker.Ack(r.Context(), m.ID, req.MV, now); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "ack", Actor: audit.ActorMember(m.ID), MemberID: &m.ID, Data: map[string]any{"mv": req.MV}})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "mv": req.MV})
}
