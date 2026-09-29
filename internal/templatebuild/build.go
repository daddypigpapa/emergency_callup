// Package templatebuild generates the standard spreadsheet templates in
// templates/ (run `make templates`). Headers come straight from each
// importer so a template can't drift from what its importer accepts.
package templatebuild

import (
	"bytes"
	"fmt"

	"github.com/xuri/excelize/v2"

	"emergencycallup/internal/area"
	"emergencycallup/internal/incident"
	"emergencycallup/internal/roster"
)

// Template is one file to write into templates/.
type Template struct {
	FileName string
	Build    func() ([]byte, error)
}

var All = []Template{
	{"명단_서식.xlsx", buildRoster},
	{"임무지역_서식.xlsx", buildAreas},
	{"조별계획_서식.xlsx", buildPlans},
}

type dataSheet struct {
	name   string
	header []string
	widths []float64
}

type guideRow struct{ item, text string }

type example struct {
	sheet string
	rows  [][]string
}

func build(sheets []dataSheet, title string, guide []guideRow, examples []example) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:   &excelize.Font{Bold: true},
		Border: []excelize.Border{{Type: "bottom", Color: "000000", Style: 2}},
	})
	if err != nil {
		return nil, err
	}
	wrap, err := f.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"}})
	if err != nil {
		return nil, err
	}

	for i, s := range sheets {
		if i == 0 {
			if err := f.SetSheetName("Sheet1", s.name); err != nil {
				return nil, err
			}
		} else if _, err := f.NewSheet(s.name); err != nil {
			return nil, err
		}
		if err := writeHeader(f, s.name, s.header, headerStyle); err != nil {
			return nil, err
		}
		for c, w := range s.widths {
			col, _ := excelize.ColumnNumberToName(c + 1)
			_ = f.SetColWidth(s.name, col, col, w)
		}
		_ = f.SetPanes(s.name, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	}

	const g = "작성방법"
	if _, err := f.NewSheet(g); err != nil {
		return nil, err
	}
	_ = f.SetColWidth(g, "A", "A", 16)
	_ = f.SetColWidth(g, "B", "B", 90)
	row := 1
	set := func(a, b string) {
		_ = f.SetCellValue(g, fmt.Sprintf("A%d", row), a)
		_ = f.SetCellValue(g, fmt.Sprintf("B%d", row), b)
		_ = f.SetCellStyle(g, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), wrap)
		row++
	}
	_ = f.SetCellValue(g, "A1", title)
	_ = f.SetCellStyle(g, "A1", "A1", headerStyle)
	row = 3
	for _, gr := range guide {
		set(gr.item, gr.text)
	}
	for _, ex := range examples {
		row++
		_ = f.SetCellValue(g, fmt.Sprintf("A%d", row), fmt.Sprintf("예시 ('%s' 시트에 이렇게 적습니다)", ex.sheet))
		row++
		var header []string
		for _, s := range sheets {
			if s.name == ex.sheet {
				header = s.header
			}
		}
		cell := fmt.Sprintf("A%d", row)
		if err := writeHeaderAt(f, g, cell, header, headerStyle); err != nil {
			return nil, err
		}
		row++
		for _, r := range ex.rows {
			vals := make([]any, len(r))
			for k, v := range r {
				vals[k] = v
			}
			_ = f.SetSheetRow(g, fmt.Sprintf("A%d", row), &vals)
			row++
		}
	}

	f.SetActiveSheet(0)
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeHeader(f *excelize.File, sheet string, header []string, style int) error {
	return writeHeaderAt(f, sheet, "A1", header, style)
}

func writeHeaderAt(f *excelize.File, sheet, cell string, header []string, style int) error {
	vals := make([]any, len(header))
	for i, h := range header {
		vals[i] = h
	}
	if err := f.SetSheetRow(sheet, cell, &vals); err != nil {
		return err
	}
	col, row, err := excelize.CellNameToCoordinates(cell)
	if err != nil {
		return err
	}
	end, _ := excelize.CoordinatesToCellName(col+len(header)-1, row)
	return f.SetCellStyle(sheet, cell, end, style)
}

func buildRoster() ([]byte, error) {
	return build(
		[]dataSheet{{"명단", roster.ImportHeader, []float64{14, 10, 10, 16, 8, 22, 18, 20, 14}}},
		"명단 가져오기 서식 — '명단' 시트에 한 줄에 한 명씩 적습니다. 첫 줄(제목)은 고치지 마세요.",
		[]guideRow{
			{"소속", "필수. 부서 이름."},
			{"이름", "필수."},
			{"행정전화", "선택. 내선 4자리(예: 1234)."},
			{"휴대전화", "필수. 010-1234-5678, 01012345678 등 어떤 형식이든 됩니다. 같은 휴대전화번호는 같은 사람으로 보고 내용을 갱신합니다."},
			{"편성조", "필수. 1~10 (\"3\" 또는 \"3조\")."},
			{"임무내용", "선택. 평소 맡는 임무. 발령할 때 조별 임무를 자동으로 채우는 근거가 됩니다."},
			{"임무지역", "선택. 설정 › 임무지역에 등록된 이름과 똑같이 적어야 합니다."},
			{"비고", "선택."},
			{"로그인ID", "필수. 현장에서 로그인할 아이디(예: 사번)."},
			{"반영 후", "새로 등록된 사람의 첫 비밀번호는 [반영]을 누른 직후 한 번만 보여 줍니다. 그때 옮겨 적어 두세요."},
		},
		[]example{{"명단", [][]string{
			{"안전정책과", "김도현", "1234", "010-1111-2222", "1", "차단선 구축", "시청 앞 광장", "", "k1001"},
			{"재난안전과", "이서연", "", "01033334444", "2조", "", "", "야간 대기", "k1002"},
		}}},
	)
}

func buildAreas() ([]byte, error) {
	return build(
		[]dataSheet{{area.ImportSheet, area.ImportHeader, []float64{18, 8, 26, 12, 12, 10, 12, 36}}},
		"임무지역 가져오기 서식 — '임무지역' 시트에 한 줄에 한 지역씩 적습니다. 첫 줄(제목)은 고치지 마세요.",
		[]guideRow{
			{"이름", "필수. 이미 있는 이름이면 그 지역을 이 내용으로 바꿉니다(단, 지금 발령에서 쓰고 있는 지역은 바꿀 수 없습니다)."},
			{"종류", "필수. 원 또는 격자."},
			{"주소", "도로명+기초번호(예: 공평로 88). 위도·경도를 모를 때 대신 적습니다. 가져오기 미리보기에 찾은 주소가 표시되니 확인하세요."},
			{"위도, 경도", "소수로(예: 35.8714, 128.6014). 적으면 주소보다 우선합니다."},
			{"반경(m)", "원: 필수, 50~5000. 격자: 주소(또는 위도·경도)에서 이 반경 안의 칸을 채웁니다."},
			{"격자크기(m)", "격자: 필수. 100, 250, 500, 1000 중 하나."},
			{"행정동", "격자: 이 동 안의 칸을 모두 채웁니다. 여러 개는 쉼표로(예: 삼덕동1가, 삼덕동2가). 같은 이름의 동이 여러 곳이면 '대구광역시 중구 삼덕동1가'처럼 시·군·구까지 적습니다. 행정동을 적으면 주소·반경보다 우선합니다."},
			{"집결지", "지역의 길안내 지점은 지역 중심으로 자동 설정됩니다. 조별 집결지는 '조별계획' 서식에서 정합니다."},
		},
		[]example{{area.ImportSheet, [][]string{
			{"시청 앞 광장", "원", "공평로 88", "", "", "300", "", ""},
			{"동인동 일대", "격자", "", "35.8714", "128.6014", "500", "250", ""},
			{"삼덕동 전역", "격자", "", "", "", "", "250", "삼덕동1가, 삼덕동2가, 삼덕동3가"},
		}}},
	)
}

func buildPlans() ([]byte, error) {
	return build(
		[]dataSheet{
			{incident.PlanImportSheet, incident.PlanImportHeader, []float64{6, 22, 18, 26, 12, 12}},
			{incident.CheckpointImportSheet, incident.CheckpointImportHeader, []float64{6, 6, 16, 26, 12, 12, 9}},
		},
		"조별계획 가져오기 서식 — '조별계획' 시트에 조마다 한 줄, '체크포인트' 시트에 체크포인트마다 한 줄씩 적습니다. 첫 줄(제목)은 고치지 마세요.",
		[]guideRow{
			{"조", "필수. 1~10 (\"3\" 또는 \"3조\"). 파일에 적은 조는 저장된 계획(체크포인트 포함)을 통째로 바꿉니다. 파일에 없는 조는 그대로 둡니다."},
			{"임무", "선택. 발령할 때 이 조의 임무로 자동으로 채워집니다."},
			{"임무지역", "선택. 설정 › 임무지역에 등록된 이름과 똑같이 적어야 합니다."},
			{"집결지", "주소(도로명+기초번호) 또는 위도·경도. 비우면 임무지역 중심이 집결지가 됩니다."},
			{"체크포인트 조", "'조별계획' 시트에 있는 조여야 합니다."},
			{"체크포인트 순번", "지나는 순서(1, 2, 3…). 비우면 적은 순서대로."},
			{"체크포인트 이름", "선택. 비우면 '지점1', '지점2'…"},
			{"체크포인트 위치", "주소 또는 위도·경도 중 하나는 필수."},
			{"체크포인트 반경(m)", "선택. 20~500, 비우면 50."},
			{"개수", "체크포인트는 조당 최대 10개."},
		},
		[]example{
			{incident.PlanImportSheet, [][]string{
				{"1", "수색구조", "시청 앞 광장", "", "", ""},
				{"2조", "교통통제", "동인동 일대", "공평로 88", "", ""},
			}},
			{incident.CheckpointImportSheet, [][]string{
				{"1", "1", "진입로", "공평로 70", "", "", ""},
				{"1", "2", "", "", "35.8725", "128.6005", "80"},
			}},
		},
	)
}
