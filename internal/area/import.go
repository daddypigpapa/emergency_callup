package area

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"emergencycallup/internal/geo"
	"emergencycallup/internal/xlsxin"
)

// ImportSheet and ImportHeader define the 임무지역 spreadsheet
// (templates/임무지역_서식.xlsx must match — enforced by a test there).
const ImportSheet = "임무지역"

var ImportHeader = []string{"이름", "종류", "주소", "위도", "경도", "반경(m)", "격자크기(m)", "행정동"}

const (
	colName = iota
	colKind
	colAddr
	colLat
	colLng
	colRadius
	colGridSize
	colDong
)

// ImportRow is one previewed 임무지역 row, geometry fully resolved (any
// address geocoding / 행정동 lookup already done), ready to commit.
type ImportRow struct {
	RowNum     int
	Name       string
	Kind       string // circle | grid
	Summary    string // human-readable, e.g. "원 · 반경 300m"
	Status     string // new | update | error
	Errors     []string
	Warnings   []string
	ExistingID int64

	Center   Point
	RadiusM  int
	GridSize int
	Cells    []Cell
}

// Importer runs the two-step 임무지역 spreadsheet import. lookup may be nil,
// in which case rows that need an address or 행정동 lookup are errors.
type Importer struct {
	areas    *Store
	lookup   geo.Lookup
	previews *xlsxin.Previews[[]ImportRow]
}

func NewImporter(areas *Store, lookup geo.Lookup) *Importer {
	return &Importer{areas: areas, lookup: lookup, previews: xlsxin.NewPreviews[[]ImportRow](10 * time.Minute)}
}

// Preview parses and validates without writing anything.
func (im *Importer) Preview(ctx context.Context, data []byte, now time.Time) (string, []ImportRow, error) {
	f, err := xlsxin.Open(data)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	rows, err := xlsxin.Sheet(f, ImportSheet, ImportHeader)
	if err != nil {
		return "", nil, err
	}

	var out []ImportRow
	seen := map[string]int{}
	for i, raw := range rows[1:] {
		if xlsxin.IsBlank(raw) {
			continue
		}
		r := im.parseRow(ctx, i+2, raw)
		if prev, dup := seen[r.Name]; dup && r.Name != "" {
			r.Errors = append(r.Errors, fmt.Sprintf("이름이 %d행과 겹칩니다", prev))
		}
		seen[r.Name] = r.RowNum
		if len(r.Errors) > 0 {
			r.Status = "error"
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return "", nil, errors.New("'임무지역' 시트에 내용이 없습니다")
	}
	return im.previews.Put(out, now), out, nil
}

func (im *Importer) parseRow(ctx context.Context, rowNum int, raw []string) ImportRow {
	r := ImportRow{RowNum: rowNum, Name: xlsxin.Cell(raw, colName)}
	fail := func(msg string, args ...any) { r.Errors = append(r.Errors, fmt.Sprintf(msg, args...)) }

	if r.Name == "" {
		fail("이름: 값이 없습니다")
	} else if utf8.RuneCountInString(r.Name) > 50 {
		fail("이름: 50자 이하여야 합니다")
	}

	switch k := xlsxin.Cell(raw, colKind); k {
	case "원", KindCircle:
		r.Kind = KindCircle
	case "격자", KindGrid:
		r.Kind = KindGrid
	default:
		fail("종류: '원' 또는 '격자'여야 합니다")
		return r
	}

	center, haveCenter := im.center(ctx, raw, &r)
	radius, haveRadius := intCell(raw, colRadius, "반경(m)", &r)

	switch r.Kind {
	case KindCircle:
		if !haveCenter {
			fail("원: 주소 또는 위도·경도가 필요합니다")
		} else if !haveRadius {
			fail("원: 반경(m)이 필요합니다")
		} else if err := ValidateCircle(center, radius); err != nil {
			fail("원: 반경은 %d~%dm, 위치는 대한민국 안이어야 합니다", CircleRadiusMin, CircleRadiusMax)
		}
		r.Center, r.RadiusM = center, radius
		r.Summary = "원"
		if haveRadius {
			r.Summary = fmt.Sprintf("원 · 반경 %dm", radius)
		}

	case KindGrid:
		size, ok := intCell(raw, colGridSize, "격자크기(m)", &r)
		if !ok || !validGridSize(size) {
			fail("격자크기(m): 100, 250, 500, 1000 중 하나여야 합니다")
			break
		}
		r.GridSize = size
		var cells []Cell
		if dongs := xlsxin.Cell(raw, colDong); dongs != "" {
			cells = im.dongCells(ctx, size, dongs, &r)
		} else if haveCenter && haveRadius {
			c, err := CellsWithinRadius(size, center, radius)
			if err != nil {
				fail("격자: 반경이 너무 큽니다 — 격자크기를 키우세요")
			}
			cells = c
		} else {
			fail("격자: 행정동, 또는 주소(위도·경도)와 반경(m)이 필요합니다")
		}
		cells = DedupeSortCells(cells)
		if len(r.Errors) == 0 {
			if err := ValidateGrid(size, cells); err != nil {
				fail("격자: 칸 수는 %d~%d개여야 합니다(현재 %d개)", GridMinCells, GridMaxCells, len(cells))
			}
		}
		r.Cells = cells
		r.Summary = fmt.Sprintf("격자 %dm · %d칸", size, len(cells))
	}

	if len(r.Errors) > 0 || r.Name == "" {
		return r
	}
	existing, err := im.areas.GetActiveByName(ctx, r.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		r.Status = "new"
	case err != nil:
		fail("기존 지역 조회 오류")
	default:
		r.ExistingID = existing.ID
		r.Status = "update"
		if inUse, err := im.areas.InUse(ctx, existing.ID); err != nil {
			fail("기존 지역 조회 오류")
		} else if inUse {
			fail("같은 이름의 지역이 현재 발령에서 사용 중이라 바꿀 수 없습니다")
		}
	}
	return r
}

// center resolves the row's point from 위도·경도, falling back to geocoding
// 주소. The geocoded match is reported as a warning so the admin can check it.
func (im *Importer) center(ctx context.Context, raw []string, r *ImportRow) (Point, bool) {
	lat, okLat, errLat := xlsxin.Float(xlsxin.Cell(raw, colLat))
	lng, okLng, errLng := xlsxin.Float(xlsxin.Cell(raw, colLng))
	if errLat != nil || errLng != nil || okLat != okLng {
		r.Errors = append(r.Errors, "위도·경도: 둘 다 숫자로 적거나 둘 다 비워 두세요")
		return Point{}, false
	}
	if okLat {
		p := Point{Lat: lat, Lng: lng}
		if !InKorea(p) {
			r.Errors = append(r.Errors, "위도·경도: 대한민국 범위 밖입니다(위도·경도를 바꿔 적지 않았는지 확인)")
			return Point{}, false
		}
		return p, true
	}
	addr := xlsxin.Cell(raw, colAddr)
	if addr == "" {
		return Point{}, false
	}
	if im.lookup == nil {
		r.Errors = append(r.Errors, "주소: 주소 검색을 사용할 수 없습니다 — 위도·경도를 적으세요")
		return Point{}, false
	}
	res, err := im.lookup.Geocode(ctx, addr)
	if err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("주소 '%s'을(를) 찾지 못했습니다", addr))
		return Point{}, false
	}
	r.Warnings = append(r.Warnings, fmt.Sprintf("주소 확인: %s", res.Matched))
	return Point{Lat: res.Lat, Lng: res.Lng}, true
}

// dongCells fills the grid with every cell inside the listed 행정동
// (comma/semicolon separated). Each entry must match exactly one dong —
// "삼덕동1가" or "대구광역시 중구 삼덕동1가"; an ambiguous name is an error
// rather than a guess.
func (im *Importer) dongCells(ctx context.Context, size int, spec string, r *ImportRow) []Cell {
	if im.lookup == nil {
		r.Errors = append(r.Errors, "행정동: 행정동 검색을 사용할 수 없습니다")
		return nil
	}
	var all []Cell
	for _, want := range strings.FieldsFunc(spec, func(c rune) bool { return c == ',' || c == ';' }) {
		want = strings.Join(strings.Fields(want), " ")
		if want == "" {
			continue
		}
		parts := strings.Fields(want)
		results, err := im.lookup.SearchAdminDong(ctx, parts[len(parts)-1])
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("행정동 '%s' 조회 실패", want))
			continue
		}
		var matches []geo.AdminDong
		for _, d := range results {
			if d.Name == want || d.Full == want || strings.HasSuffix(d.Full, " "+want) {
				matches = append(matches, d)
			}
		}
		switch len(matches) {
		case 0:
			r.Errors = append(r.Errors, fmt.Sprintf("행정동 '%s'을(를) 찾지 못했습니다(예: 삼덕동1가)", want))
			continue
		case 1:
		default:
			r.Errors = append(r.Errors, fmt.Sprintf("행정동 '%s'이(가) %d곳과 일치합니다(예: %s) — 시·군·구까지 적으세요",
				want, len(matches), matches[0].Full))
			continue
		}
		cells, err := CellsInRings(size, matches[0].Rings)
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("행정동 '%s': 너무 넓습니다 — 격자크기를 키우세요", want))
			continue
		}
		r.Warnings = append(r.Warnings, fmt.Sprintf("행정동 확인: %s (%d칸)", matches[0].Full, len(cells)))
		all = append(all, cells...)
	}
	return all
}

func intCell(raw []string, col int, label string, r *ImportRow) (int, bool) {
	v, ok, err := xlsxin.Float(xlsxin.Cell(raw, col))
	if err != nil {
		r.Errors = append(r.Errors, label+": 숫자여야 합니다")
		return 0, false
	}
	if !ok {
		return 0, false
	}
	return int(v), true
}

// CommitResult reports what Commit wrote; Failed lists rows that could not
// be saved (e.g. the area became in-use between preview and commit).
type CommitResult struct {
	Created, Updated int
	Failed           []string
}

func (im *Importer) Commit(ctx context.Context, token string, now time.Time) (*CommitResult, error) {
	rows, err := im.previews.Take(token, now)
	if err != nil {
		return nil, err
	}
	res := &CommitResult{}
	for _, r := range rows {
		if r.Status == "error" {
			continue
		}
		var err error
		switch {
		case r.Status == "new" && r.Kind == KindCircle:
			_, err = im.areas.CreateCircle(ctx, r.Name, r.Center, r.RadiusM, nil, now)
		case r.Status == "new":
			_, err = im.areas.CreateGrid(ctx, r.Name, r.GridSize, r.Cells, nil, now)
		case r.Kind == KindCircle:
			_, err = im.areas.UpdateCircle(ctx, r.ExistingID, r.Name, r.Center, r.RadiusM, nil, now)
		default:
			_, err = im.areas.UpdateGrid(ctx, r.ExistingID, r.Name, r.GridSize, r.Cells, nil, now)
		}
		if err != nil {
			res.Failed = append(res.Failed, strconv.Itoa(r.RowNum)+"행 "+r.Name+": "+err.Error())
			continue
		}
		if r.Status == "new" {
			res.Created++
		} else {
			res.Updated++
		}
	}
	return res, nil
}
