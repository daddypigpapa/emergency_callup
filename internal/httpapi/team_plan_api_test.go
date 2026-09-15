package httpapi

import (
	"context"
	"net/http"
	"testing"

	"emergencycallup/internal/area"
	"emergencycallup/internal/auth"
	"emergencycallup/internal/incident"
	"emergencycallup/internal/roster"
)

// R24-adjacent (API layer): PUT then GET round-trips a team plan, and all
// 10 teams always come back even though only one was ever saved.
func TestTeamPlansAPI_SaveAndList(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := reqJSON(t, h, http.MethodPost, "/api/v1/a/areas",
		map[string]any{"name": "지역A", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 150}, cookie)
	areaID := int64(decodeJSON(t, rec)["id"].(float64))

	body := map[string]any{"teams": []map[string]any{
		{"no": 1, "mission": "계획임무", "areaId": areaID,
			"rally":       map[string]any{"lat": 35.871, "lng": 128.601, "addr": "공평로 88"},
			"checkpoints": []map[string]any{{"name": "cp1", "lat": 35.87, "lng": 128.60, "addr": "", "r": 50}},
		},
	}}
	rec = reqJSON(t, h, http.MethodPut, "/api/v1/a/team-plans", body, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = reqJSON(t, h, http.MethodGet, "/api/v1/a/team-plans", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rec.Code, rec.Body.String())
	}
	teams := decodeJSON(t, rec)["teams"].([]any)
	if len(teams) != 10 {
		t.Fatalf("teams = %d, want 10", len(teams))
	}
	t1 := teams[0].(map[string]any)
	if t1["mission"] != "계획임무" {
		t.Errorf("team 1 mission = %v, want 계획임무", t1["mission"])
	}
	rally := t1["rally"].(map[string]any)
	if rally["auto"] != false || rally["addr"] != "공평로 88" {
		t.Errorf("team 1 rally = %+v", rally)
	}
	cps := t1["checkpoints"].([]any)
	if len(cps) != 1 {
		t.Fatalf("team 1 checkpoints = %v, want 1", cps)
	}

	t2 := teams[1].(map[string]any)
	if t2["mission"] != "" || t2["areaId"].(float64) != 0 {
		t.Errorf("untouched team 2 = %+v, want zero value", t2)
	}
}

func TestTeamPlansAPI_Save_RejectsInactiveArea(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := reqJSON(t, h, http.MethodPut, "/api/v1/a/team-plans",
		map[string]any{"teams": []map[string]any{{"no": 1, "areaId": 999999}}}, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for nonexistent area", rec.Code)
	}
}

func TestTeamPlansAPI_Save_WarnsWhenRallyOutsideArea(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := reqJSON(t, h, http.MethodPost, "/api/v1/a/areas",
		map[string]any{"name": "작은지역", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 50}, cookie)
	areaID := int64(decodeJSON(t, rec)["id"].(float64))

	// A rally point ~5km away is well outside a 50m circle.
	rec = reqJSON(t, h, http.MethodPut, "/api/v1/a/team-plans", map[string]any{
		"teams": []map[string]any{{"no": 1, "areaId": areaID,
			"rally": map[string]any{"lat": 35.92, "lng": 128.65, "addr": "멀리"}}},
	}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	warnings, ok := body["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Errorf("warnings = %v, want 1 entry about the rally point being outside the area", body["warnings"])
	}
}

// R31: a member's personal area override means their /f/me nav stays the
// override area's own nav point, not the team's rally point.
func TestHandleMemberMe_PersonalAreaOverride_KeepsOwnAreaNav(t *testing.T) {
	s := testServer(t)
	ctx := context.Background()

	areaTeam, err := s.Areas.CreateCircle(ctx, "팀지역", area.Point{Lat: 35.8714, Lng: 128.6014}, 150, nil, s.Now())
	if err != nil {
		t.Fatalf("create team area: %v", err)
	}
	areaPersonal, err := s.Areas.CreateCircle(ctx, "개인지역", area.Point{Lat: 36.0, Lng: 129.0}, 150, nil, s.Now())
	if err != nil {
		t.Fatalf("create personal area: %v", err)
	}

	hasher := auth.NewHasher()
	rosterStore := roster.NewStore(s.DB.DB, hasher)
	m, _, err := rosterStore.Create(ctx, roster.NewInput{
		LoginID: "field1", Dept: "부서", Name: "이름", Mobile: "01011112222", TeamNo: 1,
	}, s.Now())
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	plans := incident.NewPlanStore(s.DB.DB)
	rallyLat, rallyLng := 35.9, 128.9
	if err := plans.SaveBatch(ctx, []incident.TeamPlanInput{
		{No: 1, Mission: "팀임무", AreaID: areaTeam.ID, RallyLat: &rallyLat, RallyLng: &rallyLng},
	}, "admin1", s.Now().UnixMilli()); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	overrideArea := areaPersonal.ID
	got, err := s.Incidents.Open(ctx, "유형", "메시지",
		[]incident.TeamInput{{No: 1, Mission: "발령임무", AreaID: areaTeam.ID}},
		[]incident.MemberOverrideInput{{MemberID: m.ID, AreaID: &overrideArea}},
		"admin1", s.Now())
	if err != nil {
		t.Fatalf("open incident: %v", err)
	}
	_ = got

	raw, err := s.Sessions.Create(ctx, auth.KindMember, m.ID, s.Now())
	if err != nil {
		t.Fatalf("create member session: %v", err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: raw}

	rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/f/me", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	taskArea := body["task"].(map[string]any)["area"].(map[string]any)
	nav := taskArea["nav"].([]any)
	navLat, navLng := nav[0].(float64), nav[1].(float64)
	if navLat != areaPersonal.NavLat || navLng != areaPersonal.NavLng {
		t.Errorf("nav = (%f,%f), want personal override area's own nav (%f,%f), not the team rally (%f,%f)",
			navLat, navLng, areaPersonal.NavLat, areaPersonal.NavLng, rallyLat, rallyLng)
	}
}
