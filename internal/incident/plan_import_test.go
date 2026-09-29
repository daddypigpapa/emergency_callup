package incident

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"emergencycallup/internal/area"
	"emergencycallup/internal/geo"
)

type fakeGeo map[string]geo.GeocodeResult

func (f fakeGeo) Geocode(_ context.Context, q string) (*geo.GeocodeResult, error) {
	if r, ok := f[q]; ok {
		return &r, nil
	}
	return nil, geo.ErrNotFound
}

func (fakeGeo) SearchAdminDong(context.Context, string) ([]geo.AdminDong, error) { return nil, nil }

func planWorkbook(t *testing.T, plans, cps [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	_ = f.SetSheetName("Sheet1", PlanImportSheet)
	if _, err := f.NewSheet(CheckpointImportSheet); err != nil {
		t.Fatal(err)
	}
	put := func(sheet string, rows [][]string) {
		for i, r := range rows {
			vals := make([]any, len(r))
			for k, v := range r {
				vals[k] = v
			}
			cell, _ := excelize.CoordinatesToCellName(1, i+1)
			_ = f.SetSheetRow(sheet, cell, &vals)
		}
	}
	put(PlanImportSheet, plans)
	put(CheckpointImportSheet, cps)
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPlanImport_PreviewAndCommit(t *testing.T) {
	db := testDB(t)
	areas := area.NewStore(db.DB)
	plans := NewPlanStore(db.DB)
	ctx := context.Background()
	mustArea(t, areas, "시청 앞 광장", 35.8714, 128.6014)

	lookup := fakeGeo{
		"공평로 88": {Lat: 35.8716, Lng: 128.6013, Matched: "대구광역시 중구 공평로 88"},
		"공평로 70": {Lat: 35.8700, Lng: 128.6010, Matched: "대구광역시 중구 공평로 70"},
	}
	im := NewPlanImporter(plans, areas, lookup)

	data := planWorkbook(t,
		[][]string{
			PlanImportHeader,
			{"1", "수색구조", "시청 앞 광장", "", "", ""}, // auto rally
			{"2조", "교통통제", "시청 앞 광장", "공평로 88", "", ""},
			{"3", "", "없는지역", "", "", ""},
			{"4", "", "", "", "", ""},
			{"4", "", "", "", "", ""}, // duplicate team
		},
		[][]string{
			CheckpointImportHeader,
			{"1", "2", "둘째", "", "35.8725", "128.6005", "80"},
			{"1", "1", "", "공평로 70", "", "", ""},
			{"1", "", "셋째(순번없음)", "", "35.873", "128.601", ""},
			{"2", "1", "반경오류", "", "35.87", "128.60", "5"},
			{"9", "1", "없는조", "", "35.87", "128.60", ""},
		},
	)
	token, teams, err := im.Preview(ctx, data, time.Now())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	byTeam := map[int64][]PlanImportTeam{}
	for _, tm := range teams {
		byTeam[tm.TeamNo] = append(byTeam[tm.TeamNo], tm)
	}

	t1 := byTeam[1][0]
	if t1.Status != "ok" || t1.Rally != nil || len(t1.Checkpoints) != 3 {
		t.Fatalf("team 1 = %+v, want ok, auto rally, 3 checkpoints", t1)
	}
	if t1.Checkpoints[0].Name != "지점1" || t1.Checkpoints[0].Addr != "대구광역시 중구 공평로 70" ||
		t1.Checkpoints[1].Name != "둘째" || t1.Checkpoints[1].RadiusM != 80 ||
		t1.Checkpoints[2].Name != "셋째(순번없음)" || t1.Checkpoints[2].RadiusM != 50 {
		t.Errorf("team 1 checkpoints not ordered/defaulted as expected: %+v", t1.Checkpoints)
	}
	if t2 := byTeam[2][0]; t2.Status != "error" { // 반경 5m is out of range
		t.Errorf("team 2 = %+v, want error from its checkpoint", t2)
	}
	if t3 := byTeam[3][0]; t3.Status != "error" {
		t.Errorf("team 3 = %+v, want error for unknown area", t3)
	}
	if t4 := byTeam[4]; len(t4) != 2 || t4[0].Status != "error" || t4[1].Status != "error" {
		t.Errorf("duplicate team 4 rows = %+v, want both rejected", t4)
	}
	if o := byTeam[9]; len(o) != 1 || o[0].Status != "error" {
		t.Errorf("checkpoint for a team not on the plan sheet = %+v, want an error row", o)
	}

	saved, err := im.Commit(ctx, token, "admin1", time.Now())
	if err != nil || saved != 1 {
		t.Fatalf("commit saved=%d err=%v, want 1 (only team 1 is error-free)", saved, err)
	}
	p, err := plans.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mission != "수색구조" || p.AreaID == 0 || p.RallyLat != nil || len(p.Checkpoints) != 3 {
		t.Errorf("saved team 1 plan = %+v", p)
	}
}

func TestPlanImport_RallyFromAddress(t *testing.T) {
	db := testDB(t)
	areas := area.NewStore(db.DB)
	plans := NewPlanStore(db.DB)
	im := NewPlanImporter(plans, areas, fakeGeo{"공평로 88": {Lat: 35.8716, Lng: 128.6013, Matched: "대구광역시 중구 공평로 88"}})
	data := planWorkbook(t,
		[][]string{PlanImportHeader, {"5", "대기", "", "공평로 88", "", ""}},
		[][]string{CheckpointImportHeader},
	)
	token, teams, err := im.Preview(context.Background(), data, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if teams[0].Rally == nil || teams[0].Rally.Addr != "대구광역시 중구 공평로 88" {
		t.Fatalf("rally = %+v, want geocoded address", teams[0].Rally)
	}
	if _, err := im.Commit(context.Background(), token, "admin1", time.Now()); err != nil {
		t.Fatal(err)
	}
	p, _ := plans.Get(context.Background(), 5)
	if p.RallyLat == nil || *p.RallyLat != 35.8716 || p.RallyAddr != "대구광역시 중구 공평로 88" {
		t.Errorf("saved rally = %+v", p)
	}
}
