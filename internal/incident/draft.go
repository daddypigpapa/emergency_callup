package incident

import (
	"context"
	"database/sql"
)

// TeamDraft is one team's auto-computed default for the new-incident
// preview (SPEC §5.4, extended by docs/SPEC_AREA_EDITOR.md §4.5).
type TeamDraft struct {
	TeamNo            int64
	Mission           string // "" if no usable peacetime default exists
	AreaID            int64  // 0 if no usable peacetime default exists
	MissionIncomplete bool   // true: every member's peacetime mission was empty
	AreaIncomplete    bool   // true: every member's peacetime area was empty
	MemberCount       int
	FromPlan          bool // true: Mission and/or AreaID came from the team's pre-registered plan, not the §5.4 majority vote
}

// MemberOverrideDraft is one member whose peacetime mission/area differs
// from their team's computed default and so is auto-registered as a
// personal override (SPEC §5.4 rule 2).
type MemberOverrideDraft struct {
	MemberID int64
	Name     string
	TeamNo   int64
	Mission  string
	AreaID   int64
}

// Draft is the full new-incident auto-fill preview (GET /a/incidents/draft).
type Draft struct {
	Teams     []TeamDraft
	Overrides []MemberOverrideDraft
}

type memberPeacetime struct {
	ID      int64
	Name    string
	TeamNo  int64
	Mission string
	AreaID  int64
}

// BuildDraft implements SPEC §5.4: for each team with at least one active
// member, the team default is the most common non-empty peacetime value
// (ties broken by the smallest member ID); members whose own peacetime
// value differs from that default become individual overrides.
//
// docs/SPEC_AREA_EDITOR.md §4.5 adds a priority step in front of that: if
// the team has a pre-registered plan with a non-empty mission and/or area,
// that value replaces the §5.4 majority-vote default before per-member
// overrides are computed — so a member whose peacetime value happens to
// match the (now-superseded) majority default but not the plan still gets
// flagged as an override, not silently absorbed into the new default.
// plans may be nil/empty (no plan for any team).
func BuildDraft(ctx context.Context, db *sql.DB, plans []TeamPlan) (*Draft, error) {
	planByTeam := make(map[int64]TeamPlan, len(plans))
	for _, p := range plans {
		planByTeam[p.TeamNo] = p
	}

	rows, err := db.QueryContext(ctx,
		`SELECT id, name, team_no, COALESCE(mission,''), COALESCE(area_id,0)
		 FROM member WHERE active = 1 AND team_no IS NOT NULL ORDER BY team_no, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byTeam := map[int64][]memberPeacetime{}
	var teamOrder []int64
	for rows.Next() {
		var m memberPeacetime
		if err := rows.Scan(&m.ID, &m.Name, &m.TeamNo, &m.Mission, &m.AreaID); err != nil {
			return nil, err
		}
		if _, ok := byTeam[m.TeamNo]; !ok {
			teamOrder = append(teamOrder, m.TeamNo)
		}
		byTeam[m.TeamNo] = append(byTeam[m.TeamNo], m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	d := &Draft{}
	for _, teamNo := range teamOrder {
		members := byTeam[teamNo]
		defMission, missionIncomplete := modeString(members)
		defArea, areaIncomplete := modeArea(members)

		fromPlan := false
		if plan, ok := planByTeam[teamNo]; ok {
			if plan.Mission != "" {
				defMission, missionIncomplete, fromPlan = plan.Mission, false, true
			}
			if plan.AreaID != 0 {
				defArea, areaIncomplete, fromPlan = plan.AreaID, false, true
			}
		}

		d.Teams = append(d.Teams, TeamDraft{
			TeamNo: teamNo, Mission: defMission, AreaID: defArea,
			MissionIncomplete: missionIncomplete, AreaIncomplete: areaIncomplete,
			MemberCount: len(members), FromPlan: fromPlan,
		})

		for _, m := range members {
			mission := defMission
			area := defArea
			diff := false
			if m.Mission != "" && m.Mission != defMission {
				mission = m.Mission
				diff = true
			}
			if m.AreaID != 0 && m.AreaID != defArea {
				area = m.AreaID
				diff = true
			}
			if diff {
				d.Overrides = append(d.Overrides, MemberOverrideDraft{
					MemberID: m.ID, Name: m.Name, TeamNo: teamNo, Mission: mission, AreaID: area,
				})
			}
		}
	}
	return d, nil
}

func modeString(members []memberPeacetime) (value string, incomplete bool) {
	counts := map[string]int{}
	firstID := map[string]int64{}
	any := false
	for _, m := range members {
		if m.Mission == "" {
			continue
		}
		any = true
		counts[m.Mission]++
		if _, ok := firstID[m.Mission]; !ok {
			firstID[m.Mission] = m.ID
		}
	}
	if !any {
		return "", true
	}
	best := ""
	bestCount := -1
	var bestFirstID int64
	for val, c := range counts {
		if c > bestCount || (c == bestCount && firstID[val] < bestFirstID) {
			best, bestCount, bestFirstID = val, c, firstID[val]
		}
	}
	return best, false
}

func modeArea(members []memberPeacetime) (value int64, incomplete bool) {
	counts := map[int64]int{}
	firstID := map[int64]int64{}
	any := false
	for _, m := range members {
		if m.AreaID == 0 {
			continue
		}
		any = true
		counts[m.AreaID]++
		if _, ok := firstID[m.AreaID]; !ok {
			firstID[m.AreaID] = m.ID
		}
	}
	if !any {
		return 0, true
	}
	var best int64
	bestCount := -1
	var bestFirstID int64
	for val, c := range counts {
		if c > bestCount || (c == bestCount && firstID[val] < bestFirstID) {
			best, bestCount, bestFirstID = val, c, firstID[val]
		}
	}
	return best, false
}
