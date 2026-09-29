package incident

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"emergencycallup/internal/area"
	"emergencycallup/internal/geo"
	"emergencycallup/internal/xlsxin"
)

// The 조별계획 spreadsheet (templates/조별계획_서식.xlsx must match —
// enforced by a test there): one row per team, plus one row per checkpoint
// on a second sheet.
const (
	PlanImportSheet       = "조별계획"
	CheckpointImportSheet = "체크포인트"
)

var (
	PlanImportHeader       = []string{"조", "임무", "임무지역", "집결지 주소", "집결지 위도", "집결지 경도"}
	CheckpointImportHeader = []string{"조", "순번", "이름", "주소", "위도", "경도", "반경(m)"}
)

// PlanImportTeam is one previewed team, locations already resolved.
type PlanImportTeam struct {
	RowNum      int
	TeamNo      int64
	Mission     string
	AreaName    string
	AreaID      int64
	Rally       *Checkpoint // nil = automatic (area center); only Lat/Lng/Addr used
	Checkpoints []Checkpoint
	Status      string // ok | error
	Errors      []string
	Warnings    []string
}

// PlanImporter runs the two-step 조별계획 spreadsheet import. Teams in the
// file replace their saved plan entirely (mission, area, rally, all
// checkpoints); teams not in the file are left as they are.
type PlanImporter struct {
	plans    *PlanStore
	areas    *area.Store
	lookup   geo.Lookup
	previews *xlsxin.Previews[[]PlanImportTeam]
}

func NewPlanImporter(plans *PlanStore, areas *area.Store, lookup geo.Lookup) *PlanImporter {
	return &PlanImporter{plans: plans, areas: areas, lookup: lookup,
		previews: xlsxin.NewPreviews[[]PlanImportTeam](10 * time.Minute)}
}

func (im *PlanImporter) Preview(ctx context.Context, data []byte, now time.Time) (string, []PlanImportTeam, error) {
	f, err := xlsxin.Open(data)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	planRows, err := xlsxin.Sheet(f, PlanImportSheet, PlanImportHeader)
	if err != nil {
		return "", nil, err
	}
	cpRows, err := xlsxin.Sheet(f, CheckpointImportSheet, CheckpointImportHeader)
	if err != nil {
		return "", nil, err
	}

	var teams []PlanImportTeam
	byNo := map[int64]int{} // team no -> index into teams
	for i, raw := range planRows[1:] {
		if xlsxin.IsBlank(raw) {
			continue
		}
		t := im.parseTeam(ctx, i+2, raw)
		if prev, dup := byNo[t.TeamNo]; dup && t.TeamNo != 0 {
			// Both rows are rejected: which one the admin meant is a guess.
			t.Errors = append(t.Errors, fmt.Sprintf("%d조가 %d행에도 있습니다", t.TeamNo, teams[prev].RowNum))
			teams[prev].Errors = append(teams[prev].Errors, fmt.Sprintf("%d조가 %d행에도 있습니다", t.TeamNo, t.RowNum))
		}
		teams = append(teams, t)
		if t.TeamNo != 0 {
			byNo[t.TeamNo] = len(teams) - 1
		}
	}
	var orphans []PlanImportTeam // checkpoint rows that can't attach to a team

	type seqCP struct {
		seq, row int
		cp       Checkpoint
	}
	cps := map[int64][]seqCP{}
	for i, raw := range cpRows[1:] {
		if xlsxin.IsBlank(raw) {
			continue
		}
		rowNum := i + 2
		no, err := parseTeamNo(xlsxin.Cell(raw, 0))
		if err != nil {
			orphans = append(orphans, PlanImportTeam{RowNum: rowNum,
				Errors: []string{fmt.Sprintf("체크포인트 %d행 조: 숫자(예: 3 또는 3조)여야 합니다", rowNum)}})
			continue
		}
		idx, ok := byNo[no]
		if !ok {
			orphans = append(orphans, PlanImportTeam{RowNum: rowNum, TeamNo: no,
				Errors: []string{fmt.Sprintf("체크포인트 %d행: %d조가 '조별계획' 시트에 없습니다", rowNum, no)}})
			continue
		}
		t := &teams[idx]
		seq := 1 << 30 // blank 순번 sorts after numbered ones, in row order
		if s := xlsxin.Cell(raw, 1); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				t.Errors = append(t.Errors, fmt.Sprintf("체크포인트 %d행 순번: 숫자여야 합니다", rowNum))
				continue
			}
			seq = v
		}
		cp, ok := im.parseCheckpoint(ctx, rowNum, raw, t)
		if ok {
			cps[no] = append(cps[no], seqCP{seq: seq, row: rowNum, cp: cp})
		}
	}
	for no, list := range cps {
		sort.SliceStable(list, func(a, b int) bool { return list[a].seq < list[b].seq })
		t := &teams[byNo[no]]
		for k, s := range list {
			if s.cp.Name == "" {
				s.cp.Name = fmt.Sprintf("지점%d", k+1)
			}
			t.Checkpoints = append(t.Checkpoints, s.cp)
		}
		if len(t.Checkpoints) > MaxCheckpoints {
			t.Errors = append(t.Errors, fmt.Sprintf("체크포인트는 조당 %d개까지입니다(현재 %d개)", MaxCheckpoints, len(t.Checkpoints)))
		}
	}

	teams = append(teams, orphans...)
	for i := range teams {
		if len(teams[i].Errors) > 0 {
			teams[i].Status = "error"
		} else {
			teams[i].Status = "ok"
		}
	}
	if len(teams) == 0 {
		return "", nil, errors.New("'조별계획' 시트에 내용이 없습니다")
	}
	return im.previews.Put(teams, now), teams, nil
}

func (im *PlanImporter) parseTeam(ctx context.Context, rowNum int, raw []string) PlanImportTeam {
	t := PlanImportTeam{RowNum: rowNum}
	no, err := parseTeamNo(xlsxin.Cell(raw, 0))
	if err != nil {
		t.Errors = append(t.Errors, "조: 숫자(예: 3 또는 3조)여야 합니다")
	} else {
		var one int
		qerr := im.plans.db.QueryRowContext(ctx, `SELECT 1 FROM team WHERE no = ?`, no).Scan(&one)
		if errors.Is(qerr, sql.ErrNoRows) {
			t.Errors = append(t.Errors, fmt.Sprintf("%d조는 없는 조입니다", no))
		} else if qerr != nil {
			t.Errors = append(t.Errors, "조 조회 오류")
		}
		t.TeamNo = no
	}

	t.Mission = xlsxin.Cell(raw, 1)
	if utf8.RuneCountInString(t.Mission) > 100 {
		t.Errors = append(t.Errors, "임무: 100자 이하여야 합니다")
	}

	t.AreaName = xlsxin.Cell(raw, 2)
	if t.AreaName != "" {
		a, err := im.areas.GetActiveByName(ctx, t.AreaName)
		if err != nil {
			t.Errors = append(t.Errors, fmt.Sprintf("임무지역 '%s'이(가) 없습니다 — 임무지역을 먼저 등록하세요", t.AreaName))
		} else {
			t.AreaID = a.ID
		}
	}

	if lat, lng, addr, ok := im.location(ctx, raw, 3, 4, 5, "집결지", &t); ok {
		t.Rally = &Checkpoint{Lat: lat, Lng: lng, Addr: addr}
	}
	return t
}

func (im *PlanImporter) parseCheckpoint(ctx context.Context, rowNum int, raw []string, t *PlanImportTeam) (Checkpoint, bool) {
	label := fmt.Sprintf("체크포인트 %d행", rowNum)
	nErr := len(t.Errors)
	cp := Checkpoint{Name: xlsxin.Cell(raw, 2), RadiusM: 50}
	if utf8.RuneCountInString(cp.Name) > 50 {
		t.Errors = append(t.Errors, label+" 이름: 50자 이하여야 합니다")
	}
	if v, ok, err := xlsxin.Float(xlsxin.Cell(raw, 6)); err != nil || (ok && (v < 20 || v > 500)) {
		t.Errors = append(t.Errors, label+" 반경(m): 20~500 사이 숫자여야 합니다")
	} else if ok {
		cp.RadiusM = int(v)
	}
	lat, lng, addr, ok := im.location(ctx, raw, 3, 4, 5, label, t)
	if !ok && len(t.Errors) == nErr {
		t.Errors = append(t.Errors, label+": 주소 또는 위도·경도가 필요합니다")
	}
	cp.Lat, cp.Lng, cp.Addr = lat, lng, addr
	return cp, ok && len(t.Errors) == nErr
}

// location reads a point from (lat, lng) columns, or geocodes the address
// column when those are blank. ok=false with no error added means "nothing
// given" (an automatic rally point, for the team sheet).
func (im *PlanImporter) location(ctx context.Context, raw []string, addrCol, latCol, lngCol int, label string, t *PlanImportTeam) (lat, lng float64, addr string, ok bool) {
	addr = xlsxin.Cell(raw, addrCol)
	lat, okLat, errLat := xlsxin.Float(xlsxin.Cell(raw, latCol))
	lng, okLng, errLng := xlsxin.Float(xlsxin.Cell(raw, lngCol))
	if errLat != nil || errLng != nil || okLat != okLng {
		t.Errors = append(t.Errors, label+" 위도·경도: 둘 다 숫자로 적거나 둘 다 비워 두세요")
		return 0, 0, "", false
	}
	if okLat {
		if !area.InKorea(area.Point{Lat: lat, Lng: lng}) {
			t.Errors = append(t.Errors, label+" 위도·경도: 대한민국 범위 밖입니다(위도·경도를 바꿔 적지 않았는지 확인)")
			return 0, 0, "", false
		}
		if addr == "" {
			addr = "좌표 지정"
		}
		return lat, lng, addr, true
	}
	if addr == "" {
		return 0, 0, "", false
	}
	if im.lookup == nil {
		t.Errors = append(t.Errors, label+" 주소: 주소 검색을 사용할 수 없습니다 — 위도·경도를 적으세요")
		return 0, 0, "", false
	}
	res, err := im.lookup.Geocode(ctx, addr)
	if err != nil {
		t.Errors = append(t.Errors, fmt.Sprintf("%s 주소 '%s'을(를) 찾지 못했습니다", label, addr))
		return 0, 0, "", false
	}
	t.Warnings = append(t.Warnings, fmt.Sprintf("%s 주소 확인: %s", label, res.Matched))
	return res.Lat, res.Lng, res.Matched, true
}

func parseTeamNo(s string) (int64, error) {
	v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(s), "조"))
	if err != nil || v < 1 || v > 99 {
		return 0, errors.New("bad team number")
	}
	return int64(v), nil
}

// Commit saves every team without errors in one SaveBatch and returns how
// many were saved.
func (im *PlanImporter) Commit(ctx context.Context, token, actor string, now time.Time) (int, error) {
	teams, err := im.previews.Take(token, now)
	if err != nil {
		return 0, err
	}
	var inputs []TeamPlanInput
	for _, t := range teams {
		if t.Status != "ok" {
			continue
		}
		in := TeamPlanInput{No: t.TeamNo, Mission: t.Mission, AreaID: t.AreaID, Checkpoints: t.Checkpoints}
		if t.Rally != nil {
			lat, lng := t.Rally.Lat, t.Rally.Lng
			in.RallyLat, in.RallyLng, in.RallyAddr = &lat, &lng, t.Rally.Addr
		}
		inputs = append(inputs, in)
	}
	if len(inputs) == 0 {
		return 0, nil
	}
	if err := im.plans.SaveBatch(ctx, inputs, actor, now.UnixMilli()); err != nil {
		return 0, err
	}
	return len(inputs), nil
}
