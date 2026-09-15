package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"emergencycallup/internal/area"
	"emergencycallup/internal/audit"
	"emergencycallup/internal/incident"
	"emergencycallup/internal/tracker"
)

func stateCode(state string) int {
	switch state {
	case incident.StateNotified:
		return 0
	case incident.StateLoggedIn:
		return 1
	case incident.StateLocDenied:
		return 2
	case incident.StateMoving:
		return 3
	case incident.StateArrived:
		return 4
	case incident.StateLeft:
		return 5
	}
	return 0
}

func maskMobile(mobile string) string {
	if len(mobile) != 11 {
		return mobile
	}
	return mobile[:3] + "****" + mobile[7:]
}

// buildBoardPayload assembles the common body of GET /a/snapshot and
// GET /a/delta (SPEC §7.4). since=nil means "full snapshot".
func (s *Server) buildBoardPayload(w http.ResponseWriter, r *http.Request, since *uint64) {
	now := s.Now()
	ctx := r.Context()

	body := map[string]any{"t": now.UnixMilli(), "epoch": s.Tracker.Epoch(), "seq": s.Tracker.Seq()}

	inc, err := s.Incidents.GetActive(ctx)
	if errors.Is(err, incident.ErrNoActiveIncident) {
		body["incident"] = nil
		body["teams"] = []any{}
		body["areas"] = []any{}
		body["people"] = []any{}
		body["m"] = []any{}
		writeJSON(w, body)
		return
	}
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	body["incident"] = map[string]any{"id": inc.ID, "type": inc.TypeText, "message": inc.Message, "iv": inc.Version, "openedAt": inc.OpenedAt}

	teamTasks, err := s.Incidents.ListTeamTasks(ctx, inc.ID)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	teams := make([]any, 0, len(teamTasks))
	for _, tt := range teamTasks {
		cps := make([][3]any, len(tt.Checkpoints))
		for i, cp := range tt.Checkpoints {
			cps[i] = [3]any{cp.Seq, cp.Lat, cp.Lng}
		}
		teams = append(teams, map[string]any{
			"no": tt.TeamNo, "name": s.teamName(ctx, tt.TeamNo), "mission": tt.Mission, "areaId": tt.AreaID, "ver": tt.Version,
			"rally": [2]float64{tt.RallyLat, tt.RallyLng}, "cps": cps,
		})
	}
	body["teams"] = teams

	areaRows, err := s.Areas.List(ctx, true)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	areasJSON := make([]any, 0, len(areaRows))
	for i := range areaRows {
		a := &areaRows[i]
		size, cells := gridFieldsJSON(a)
		areasJSON = append(areasJSON, map[string]any{
			"id": a.ID, "name": a.Name, "kind": a.Kind, "lat": a.Lat, "lng": a.Lng, "r": a.RadiusM,
			"polygon": polygonJSON(a), "size": size, "cells": cells, "bbox": a.BBox,
		})
	}
	body["areas"] = areasJSON

	assignments, err := s.Incidents.ListAssignments(ctx, inc.ID)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	people := make([]any, 0, len(assignments))
	for _, a := range assignments {
		m, err := s.Members.GetByID(ctx, a.MemberID)
		if err != nil {
			continue
		}
		people = append(people, []any{
			a.MemberID, m.Name, m.Dept, a.TeamNo, nullEmpty(m.OfficeTel), maskMobile(m.Mobile), nullEmpty(m.Note),
			a.MissionOverride, a.AreaOverrideID,
		})
	}
	body["people"] = people

	var snaps []tracker.LiveSnapshot
	if since != nil {
		snaps = s.Tracker.SnapshotSince(*since)
	} else {
		snaps = s.Tracker.SnapshotAll()
	}
	mRows := make([]any, 0, len(snaps))
	for _, sn := range snaps {
		elapsed := int64(-1)
		if sn.HasLastFix {
			elapsed = (sn.LastFixAt - inc.OpenedAt) / 1000
		}
		flags := 0
		if sn.AckPending {
			flags |= 1
		}
		if sn.Suspect {
			flags |= 2
		}
		mRows = append(mRows, []any{sn.MemberID, stateCode(sn.State), sn.LastLat5, sn.LastLng5, sn.LastAcc, elapsed, flags})
	}
	body["m"] = mRows

	writeJSON(w, body)
}

// rallyJSON/checkpointsJSON render one team's pre-registered plan fields
// for the new-incident preview (docs/SPEC_AREA_EDITOR.md §4.5). rallyJSON
// returns nil when the plan leaves the rally point automatic (no lat/lng
// saved) — the actual auto-resolved point is only computed at incident
// open/team-task-update time (SPEC_AREA_EDITOR.md §3.5), not here.
func rallyJSON(p incident.TeamPlan) any {
	if p.RallyLat == nil || p.RallyLng == nil {
		return nil
	}
	return map[string]any{"lat": *p.RallyLat, "lng": *p.RallyLng, "addr": p.RallyAddr}
}

func checkpointsJSON(p incident.TeamPlan) []any {
	out := make([]any, 0, len(p.Checkpoints))
	for _, cp := range p.Checkpoints {
		out = append(out, map[string]any{"seq": cp.Seq, "name": cp.Name, "lat": cp.Lat, "lng": cp.Lng, "addr": cp.Addr, "r": cp.RadiusM})
	}
	return out
}

func polygonJSON(a *area.Row) any {
	if a.Kind != area.KindPolygon {
		return nil
	}
	poly := make([][2]float64, len(a.Polygon))
	for i, p := range a.Polygon {
		poly[i] = [2]float64{p.Lat, p.Lng}
	}
	return poly
}

// gridFieldsJSON returns (size, cells) for a grid-kind area, or (nil, nil)
// otherwise (docs/SPEC_AREA_EDITOR.md §4.1/§4.5). cells is the [[i,j],...]
// wire format shared with POST/PUT /a/areas.
func gridFieldsJSON(a *area.Row) (size any, cells any) {
	if a.Kind != area.KindGrid {
		return nil, nil
	}
	pairs := make([][2]int64, len(a.Cells))
	for i, c := range a.Cells {
		pairs[i] = [2]int64{c.I, c.J}
	}
	return a.GridSize, pairs
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// handleSnapshot implements GET /a/snapshot.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	s.buildBoardPayload(w, r, nil)
}

// handleDelta implements GET /a/delta?epoch=E&since=N.
func (s *Server) handleDelta(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	epoch := r.URL.Query().Get("epoch")
	sinceStr := r.URL.Query().Get("since")
	since, err := strconv.ParseUint(sinceStr, 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "since 파라미터가 필요합니다.")
		return
	}
	if epoch != s.Tracker.Epoch() {
		writeError(w, now, "resync", "epoch가 바뀌었습니다. 스냅샷을 다시 받으세요.")
		return
	}
	s.buildBoardPayload(w, r, &since)
}

// handleIncidentDraft implements GET /a/incidents/draft (SPEC §5.4, §7.3,
// docs/SPEC_AREA_EDITOR.md §4.5).
func (s *Server) handleIncidentDraft(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	plans, err := s.Plans.List(r.Context())
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	planByTeam := make(map[int64]incident.TeamPlan, len(plans))
	for _, p := range plans {
		planByTeam[p.TeamNo] = p
	}

	draft, err := incident.BuildDraft(r.Context(), s.DB.DB, plans)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	teams := make([]any, 0, len(draft.Teams))
	for _, t := range draft.Teams {
		plan := planByTeam[t.TeamNo]
		teams = append(teams, map[string]any{
			"no": t.TeamNo, "mission": t.Mission, "areaId": t.AreaID,
			"missionIncomplete": t.MissionIncomplete, "areaIncomplete": t.AreaIncomplete, "memberCount": t.MemberCount,
			"fromPlan": t.FromPlan, "rally": rallyJSON(plan), "checkpoints": checkpointsJSON(plan),
		})
	}
	overrides := make([]any, 0, len(draft.Overrides))
	for _, o := range draft.Overrides {
		overrides = append(overrides, map[string]any{"memberId": o.MemberID, "name": o.Name, "teamNo": o.TeamNo, "mission": o.Mission, "areaId": o.AreaID})
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "teams": teams, "overrides": overrides})
}

type incidentOpenRequest struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Teams   []struct {
		No      int64  `json:"no"`
		Mission string `json:"mission"`
		AreaID  int64  `json:"areaId"`
	} `json:"teams"`
	Members []struct {
		ID      int64   `json:"id"`
		Mission *string `json:"mission"`
		AreaID  *int64  `json:"areaId"`
	} `json:"members"`
}

// handleIncidentOpen implements POST /a/incidents.
func (s *Server) handleIncidentOpen(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	var req incidentOpenRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Type == "" || req.Message == "" {
		writeError(w, now, "invalid", "발령종류와 메시지를 입력하세요.")
		return
	}
	teams := make([]incident.TeamInput, len(req.Teams))
	for i, t := range req.Teams {
		teams[i] = incident.TeamInput{No: t.No, Mission: t.Mission, AreaID: t.AreaID}
	}
	overrides := make([]incident.MemberOverrideInput, len(req.Members))
	for i, m := range req.Members {
		overrides[i] = incident.MemberOverrideInput{MemberID: m.ID, Mission: m.Mission, AreaID: m.AreaID}
	}

	inc, err := s.Incidents.Open(r.Context(), req.Type, req.Message, teams, overrides, audit.ActorAdmin(admin.LoginID), now)
	if err != nil {
		switch {
		case errors.Is(err, incident.ErrIncidentActive):
			writeError(w, now, "conflict", "이미 진행 중인 사건이 있습니다.")
		case errors.Is(err, incident.ErrNoTeamsRequested), errors.Is(err, incident.ErrTeamMissingValues):
			writeError(w, now, "invalid", err.Error())
		default:
			writeError(w, now, "server", "서버 오류가 발생했습니다.")
		}
		return
	}
	if err := s.Tracker.LoadActive(r.Context()); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": inc.ID, "iv": inc.Version})
}

type incidentUpdateMetaRequest struct {
	Type    *string `json:"type"`
	Message *string `json:"message"`
}

// handleIncidentUpdateMeta implements PATCH /a/incidents/current.
func (s *Server) handleIncidentUpdateMeta(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	var req incidentUpdateMetaRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	inc, err := s.Incidents.UpdateMeta(r.Context(), req.Type, req.Message, audit.ActorAdmin(admin.LoginID), now)
	if err != nil {
		if errors.Is(err, incident.ErrNoActiveIncident) {
			writeError(w, now, "invalid", "진행 중인 사건이 없습니다.")
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if err := s.Tracker.LoadActive(r.Context()); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "iv": inc.Version})
}

type teamTaskUpdateRequest struct {
	Mission string `json:"mission"`
	AreaID  int64  `json:"areaId"`
}

// handleTeamTaskUpdate implements PUT /a/incidents/current/teams/{no}.
func (s *Server) handleTeamTaskUpdate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	teamNo, err := strconv.ParseInt(r.PathValue("no"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "조 번호가 올바르지 않습니다.")
		return
	}
	var req teamTaskUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Mission == "" || req.AreaID == 0 {
		writeError(w, now, "invalid", "임무와 임무지역을 입력하세요.")
		return
	}
	inc, err := s.Incidents.GetActive(r.Context())
	if err != nil {
		writeError(w, now, "invalid", "진행 중인 사건이 없습니다.")
		return
	}
	if err := s.Incidents.UpdateTeamTask(r.Context(), inc.ID, teamNo, req.Mission, req.AreaID, audit.ActorAdmin(admin.LoginID), now); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if err := s.Tracker.LoadActive(r.Context()); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

type addMemberRequest struct {
	MemberID int64   `json:"memberId"`
	TeamNo   int64   `json:"teamNo"`
	Mission  *string `json:"mission"`
	AreaID   *int64  `json:"areaId"`
}

// handleIncidentAddMember implements POST /a/incidents/current/members.
func (s *Server) handleIncidentAddMember(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	var req addMemberRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.MemberID == 0 || req.TeamNo == 0 {
		writeError(w, now, "invalid", "인원과 조를 지정하세요.")
		return
	}
	inc, err := s.Incidents.GetActive(r.Context())
	if err != nil {
		writeError(w, now, "invalid", "진행 중인 사건이 없습니다.")
		return
	}
	if err := s.Incidents.AddMember(r.Context(), inc.ID, req.MemberID, req.TeamNo, req.Mission, req.AreaID, audit.ActorAdmin(admin.LoginID), now); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if err := s.Tracker.LoadActive(r.Context()); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

type memberAssignmentUpdateRequest struct {
	Mission *string `json:"mission"`
	AreaID  *int64  `json:"areaId"`
	TeamNo  *int64  `json:"teamNo"`
}

// handleMemberAssignmentUpdate implements PUT /a/incidents/current/members/{id}.
func (s *Server) handleMemberAssignmentUpdate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	memberID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "인원 ID가 올바르지 않습니다.")
		return
	}
	var req memberAssignmentUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	inc, err := s.Incidents.GetActive(r.Context())
	if err != nil {
		writeError(w, now, "invalid", "진행 중인 사건이 없습니다.")
		return
	}
	// SPEC §7.3: teamNo move isn't part of this package's override model
	// directly — re-home via remove+add semantics is out of v1 scope beyond
	// mission/area override, so only mission/area are applied here.
	if err := s.Incidents.UpdateMemberOverride(r.Context(), inc.ID, memberID, req.Mission, req.AreaID, audit.ActorAdmin(admin.LoginID), now); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if err := s.Tracker.LoadActive(r.Context()); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

// handleMemberContact implements GET /a/incidents/current/members/{id}/contact.
func (s *Server) handleMemberContact(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	memberID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "인원 ID가 올바르지 않습니다.")
		return
	}
	m, err := s.Members.GetByID(r.Context(), memberID)
	if err != nil {
		writeError(w, now, "invalid", "인원을 찾을 수 없습니다.")
		return
	}
	inc, _ := s.Incidents.GetActive(r.Context())
	var incidentID *int64
	if inc != nil {
		incidentID = &inc.ID
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "contact_view", Actor: audit.ActorAdmin(admin.LoginID), IncidentID: incidentID, MemberID: &memberID})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "mobile": m.Mobile, "officeTel": m.OfficeTel})
}

type incidentCloseRequest struct {
	Confirm string `json:"confirm"`
}

// handleIncidentClose implements POST /a/incidents/current/close.
func (s *Server) handleIncidentClose(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	var req incidentCloseRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Confirm != "상황종료" {
		writeError(w, now, "invalid", "확인 문구가 올바르지 않습니다.")
		return
	}
	inc, err := s.Incidents.Close(r.Context(), audit.ActorAdmin(admin.LoginID), now)
	if err != nil {
		if errors.Is(err, incident.ErrNoActiveIncident) {
			writeError(w, now, "invalid", "진행 중인 사건이 없습니다.")
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	// SPEC §11.1/§16-1: member sessions must not outlive incident close + 1h.
	if err := s.Sessions.ExpireMemberSessionsBy(r.Context(), now.UnixMilli()+60*60*1000); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if err := s.Tracker.LoadActive(r.Context()); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if s.BackupHook != nil {
		go s.BackupHook(context.Background())
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": inc.ID})
}
