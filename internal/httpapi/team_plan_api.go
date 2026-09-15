package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"emergencycallup/internal/area"
	"emergencycallup/internal/audit"
	"emergencycallup/internal/incident"
)

// handleTeamPlansList implements GET /a/team-plans (docs/SPEC_AREA_EDITOR.md
// §4.4). Always returns all 10 teams; a team with no saved rally point gets
// its area's own nav point back as the display default ("auto":true), or
// null if the team has no area at all.
func (s *Server) handleTeamPlansList(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	ctx := r.Context()
	plans, err := s.Plans.List(ctx)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	teams := make([]any, 0, len(plans))
	for _, p := range plans {
		teams = append(teams, map[string]any{
			"no": p.TeamNo, "name": s.teamName(ctx, p.TeamNo), "mission": p.Mission, "areaId": p.AreaID,
			"rally":       s.teamPlanRallyJSON(ctx, p),
			"checkpoints": checkpointsJSON(p),
		})
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "teams": teams})
}

func (s *Server) teamPlanRallyJSON(ctx context.Context, p incident.TeamPlan) any {
	if p.RallyLat != nil && p.RallyLng != nil {
		return map[string]any{"lat": *p.RallyLat, "lng": *p.RallyLng, "addr": p.RallyAddr, "auto": false}
	}
	if p.AreaID == 0 {
		return nil
	}
	ar, err := s.Areas.GetByID(ctx, p.AreaID)
	if err != nil {
		return nil
	}
	return map[string]any{"lat": ar.NavLat, "lng": ar.NavLng, "addr": "", "auto": true}
}

type teamPlanSaveRequest struct {
	Teams []teamPlanSaveTeam `json:"teams"`
}

type teamPlanSaveTeam struct {
	No      int64  `json:"no"`
	Mission string `json:"mission"`
	AreaID  int64  `json:"areaId"`
	Rally   *struct {
		Lat  float64 `json:"lat"`
		Lng  float64 `json:"lng"`
		Addr string  `json:"addr"`
	} `json:"rally"`
	Checkpoints []struct {
		Name string  `json:"name"`
		Lat  float64 `json:"lat"`
		Lng  float64 `json:"lng"`
		Addr string  `json:"addr"`
		R    int     `json:"r"`
	} `json:"checkpoints"`
}

// handleTeamPlansSave implements PUT /a/team-plans (docs/SPEC_AREA_EDITOR.md
// §4.4): saves the given teams' plans in one batch; teams not included are
// left untouched. areaId must name an active area. A rally point outside
// its team's area is allowed (a parking lot can legitimately sit outside
// the mission area boundary) but reported back as a warning.
func (s *Server) handleTeamPlansSave(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	var req teamPlanSaveRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}

	inputs := make([]incident.TeamPlanInput, 0, len(req.Teams))
	var warnings []string
	changedTeams := make([]int64, 0, len(req.Teams))
	for _, t := range req.Teams {
		var ar *area.Row
		if t.AreaID != 0 {
			var err error
			ar, err = s.Areas.GetByID(r.Context(), t.AreaID)
			if err != nil || !ar.Active {
				writeError(w, now, "invalid", "존재하지 않거나 비활성화된 지역입니다.")
				return
			}
		}

		in := incident.TeamPlanInput{No: t.No, Mission: t.Mission, AreaID: t.AreaID}
		if t.Rally != nil {
			in.RallyLat, in.RallyLng, in.RallyAddr = &t.Rally.Lat, &t.Rally.Lng, t.Rally.Addr
			if ar != nil && ar.SignedDistance(area.Point{Lat: t.Rally.Lat, Lng: t.Rally.Lng}) > 0 {
				warnings = append(warnings, itoa(int(t.No))+"조 집결지가 임무지역 밖입니다")
			}
		}
		for _, cp := range t.Checkpoints {
			in.Checkpoints = append(in.Checkpoints, incident.Checkpoint{
				Name: cp.Name, Lat: cp.Lat, Lng: cp.Lng, Addr: cp.Addr, RadiusM: cp.R,
			})
		}
		inputs = append(inputs, in)
		changedTeams = append(changedTeams, t.No)
	}

	if err := s.Plans.SaveBatch(r.Context(), inputs, admin.LoginID, now.UnixMilli()); err != nil {
		writeError(w, now, "invalid", err.Error())
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "team_plan_update", Actor: audit.ActorAdmin(admin.LoginID),
		Data: map[string]any{"teams": changedTeams}})

	body := map[string]any{"t": now.UnixMilli()}
	if len(warnings) > 0 {
		body["warnings"] = warnings
	}
	writeJSON(w, body)
}
