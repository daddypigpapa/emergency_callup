package area

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"emergencycallup/internal/geo"
)

type fakeLookup struct {
	addrs map[string]geo.GeocodeResult
	dongs []geo.AdminDong
}

func (f fakeLookup) Geocode(_ context.Context, q string) (*geo.GeocodeResult, error) {
	if r, ok := f.addrs[q]; ok {
		return &r, nil
	}
	return nil, geo.ErrNotFound
}

func (f fakeLookup) SearchAdminDong(_ context.Context, q string) ([]geo.AdminDong, error) {
	var out []geo.AdminDong
	for _, d := range f.dongs {
		if strings.Contains(d.Name, q) {
			out = append(out, d)
		}
	}
	return out, nil
}

func workbook(t *testing.T, sheets map[string][][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	first := true
	for name, rows := range sheets {
		if first {
			_ = f.SetSheetName("Sheet1", name)
			first = false
		} else if _, err := f.NewSheet(name); err != nil {
			t.Fatal(err)
		}
		for i, r := range rows {
			vals := make([]any, len(r))
			for k, v := range r {
				vals[k] = v
			}
			cell, _ := excelize.CoordinatesToCellName(1, i+1)
			_ = f.SetSheetRow(name, cell, &vals)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A square dong ring ~550m on a side around the test point.
func squareRing(lat, lng, d float64) [][2]float64 {
	return [][2]float64{{lat - d, lng - d}, {lat - d, lng + d}, {lat + d, lng + d}, {lat + d, lng - d}, {lat - d, lng - d}}
}

func TestAreaImport_PreviewAndCommit(t *testing.T) {
	db := testDB(t)
	s := NewStore(db.DB)
	ctx := context.Background()
	if _, err := s.CreateCircle(ctx, "기존지역", Point{Lat: 35.87, Lng: 128.60}, 100, nil, time.Now()); err != nil {
		t.Fatal(err)
	}

	lookup := fakeLookup{
		addrs: map[string]geo.GeocodeResult{"공평로 88": {Lat: 35.8716, Lng: 128.6013, Matched: "대구광역시 중구 공평로 88"}},
		dongs: []geo.AdminDong{
			{Name: "삼덕동1가", Full: "대구광역시 중구 삼덕동1가", Rings: [][][2]float64{squareRing(35.866, 128.603, 0.005)}},
			{Name: "신천동", Full: "대구광역시 동구 신천동", Rings: [][][2]float64{squareRing(35.87, 128.62, 0.003)}},
			{Name: "신천동", Full: "서울특별시 송파구 신천동", Rings: [][][2]float64{squareRing(37.51, 127.10, 0.003)}},
		},
	}
	im := NewImporter(s, lookup)

	data := workbook(t, map[string][][]string{ImportSheet: {
		ImportHeader,
		{"원(좌표)", "원", "", "35.8714", "128.6014", "300", "", ""},
		{"원(주소)", "circle", "공평로 88", "", "", "1,000", "", ""},
		{"격자(반경)", "격자", "", "35.8714", "128.6014", "500", "250", ""},
		{"격자(동)", "grid", "", "", "", "", "250", "삼덕동1가"},
		{"기존지역", "원", "", "35.88", "128.61", "200", "", ""},
		{"애매한동", "격자", "", "", "", "", "250", "신천동"},
		{"종류오류", "사각형", "", "35.87", "128.60", "100", "", ""},
		{"원(좌표)", "원", "", "35.87", "128.60", "100", "", ""},
		{},
		{"뒤바뀐좌표", "원", "", "128.6", "35.87", "100", "", ""},
	}})
	token, rows, err := im.Preview(ctx, data, time.Now())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	byName := map[string][]ImportRow{}
	for _, r := range rows {
		byName[r.Name] = append(byName[r.Name], r)
	}

	if r := byName["원(주소)"][0]; r.Status != "new" || r.RadiusM != 1000 || len(r.Warnings) == 0 {
		t.Errorf("원(주소) = %+v, want new, radius 1000 (thousands separator), geocode warning", r)
	}
	if r := byName["격자(반경)"][0]; r.Status != "new" || len(r.Cells) < 9 {
		t.Errorf("격자(반경) = %+v, want new with a filled disc of cells", r)
	}
	if r := byName["격자(동)"][0]; r.Status != "new" || len(r.Cells) < 4 {
		t.Errorf("격자(동) = %+v, want new with the dong's cells", r)
	}
	if r := byName["기존지역"][0]; r.Status != "update" || r.ExistingID == 0 {
		t.Errorf("기존지역 = %+v, want update of the existing area", r)
	}
	for _, name := range []string{"애매한동", "종류오류", "뒤바뀐좌표"} {
		if r := byName[name][0]; r.Status != "error" {
			t.Errorf("%s = %+v, want error", name, r)
		}
	}
	if dups := byName["원(좌표)"]; len(dups) != 2 || dups[1].Status != "error" {
		t.Errorf("duplicate name rows = %+v, want the second flagged", dups)
	}

	res, err := im.Commit(ctx, token, time.Now())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.Created != 4 || res.Updated != 1 || len(res.Failed) != 0 {
		t.Errorf("commit = %+v, want 4 created (원(좌표) #1, 원(주소), 격자×2), 1 updated", res)
	}
	got, err := s.GetActiveByName(ctx, "기존지역")
	if err != nil || got.RadiusM != 200 {
		t.Errorf("기존지역 after commit = %+v (%v), want radius 200", got, err)
	}
	if _, err := im.Commit(ctx, token, time.Now()); err == nil {
		t.Error("a preview token must be single-use")
	}
}

func TestAreaImport_RejectsWrongHeaderAndNonXLSX(t *testing.T) {
	db := testDB(t)
	im := NewImporter(NewStore(db.DB), nil)
	if _, _, err := im.Preview(context.Background(), []byte("not a zip"), time.Now()); err == nil {
		t.Error("non-xlsx input should be rejected")
	}
	data := workbook(t, map[string][][]string{ImportSheet: {{"이름", "종류"}, {"a", "원"}}})
	if _, _, err := im.Preview(context.Background(), data, time.Now()); err == nil {
		t.Error("wrong header should be rejected")
	}
}

func TestAreaImport_NoLookupMeansAddressRowsError(t *testing.T) {
	db := testDB(t)
	im := NewImporter(NewStore(db.DB), nil)
	data := workbook(t, map[string][][]string{ImportSheet: {ImportHeader, {"주소만", "원", "공평로 88", "", "", "300", "", ""}}})
	_, rows, err := im.Preview(context.Background(), data, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != "error" {
		t.Errorf("row = %+v, want error when no address lookup is available", rows[0])
	}
}
