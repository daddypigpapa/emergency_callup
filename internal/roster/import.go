package roster

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"

	"emergencycallup/internal/area"
)

// RowStatus classifies one parsed xlsx row for the preview screen
// (SPEC §7.3, §8.2.6: "신규/변경/오류").
type RowStatus string

const (
	StatusNew       RowStatus = "new"
	StatusChanged   RowStatus = "changed"
	StatusUnchanged RowStatus = "unchanged"
	StatusError     RowStatus = "error"
)

// ImportRow is one parsed and validated roster row.
type ImportRow struct {
	RowNum     int // 1-based, matching the spreadsheet's data row order
	LoginID    string
	Dept       string
	Name       string
	OfficeTel  string
	Mobile     string
	TeamNo     int64
	Mission    string
	AreaName   string
	AreaID     int64
	Note       string
	Status     RowStatus
	Errors     []string
	Warnings   []string
	ExistingID int64 // 0 if new
}

// Preview holds one previewed-but-not-committed import batch.
type Preview struct {
	Token     string
	CreatedAt time.Time
	Rows      []ImportRow
}

// expectedHeader is the exact column order required by SPEC §8.2.6:
// "소속, 이름, 행정전화, 휴대전화, 편성조, 임무내용, 임무지역, 비고, 로그인ID".
var expectedHeader = []string{"소속", "이름", "행정전화", "휴대전화", "편성조", "임무내용", "임무지역", "비고", "로그인ID"}

const previewTTL = 10 * time.Minute

// Importer runs the two-phase (preview/commit) xlsx roster import.
type Importer struct {
	db      *sql.DB
	areas   *area.Store
	members *Store

	mu       sync.Mutex
	previews map[string]*Preview
}

func NewImporter(db *sql.DB, areas *area.Store, members *Store) *Importer {
	return &Importer{db: db, areas: areas, members: members, previews: make(map[string]*Preview)}
}

var (
	ErrBadSignature   = errors.New("roster: file is not a valid xlsx (zip signature check failed)")
	ErrBadHeader      = errors.New("roster: header row does not match the required column order")
	ErrEmptySheet     = errors.New("roster: sheet has no data rows")
	ErrPreviewExpired = errors.New("roster: preview token not found or expired")
)

// xlsxSignature is the ZIP local-file-header magic (SPEC §11.3: "확장자와
// 파일 시그니처(ZIP PK\x03\x04) 모두 확인").
var xlsxSignature = []byte{'P', 'K', 0x03, 0x04}

// HasXLSXSignature checks the ZIP magic bytes, independent of parsing.
func HasXLSXSignature(data []byte) bool {
	return bytes.HasPrefix(data, xlsxSignature)
}

// Preview parses and validates xlsxData without writing to the database,
// then stores the result under a fresh token (SPEC §7.3 "저장 안 함").
func (im *Importer) Preview(ctx context.Context, xlsxData []byte, now time.Time) (*Preview, error) {
	if !HasXLSXSignature(xlsxData) {
		return nil, ErrBadSignature
	}
	f, err := excelize.OpenReader(bytes.NewReader(xlsxData))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sheet := f.GetSheetName(0)
	rawRows, err := f.GetRows(sheet)
	if err != nil {
		return nil, err
	}
	if len(rawRows) == 0 {
		return nil, ErrEmptySheet
	}
	header := rawRows[0]
	if len(header) < len(expectedHeader) {
		return nil, ErrBadHeader
	}
	for i, want := range expectedHeader {
		if header[i] != want {
			return nil, ErrBadHeader
		}
	}
	if len(rawRows) < 2 {
		return nil, ErrEmptySheet
	}

	var rows []ImportRow
	for i, raw := range rawRows[1:] {
		if isBlankRow(raw) {
			continue
		}
		rows = append(rows, im.parseRow(ctx, i+1, raw))
	}

	im.sweepExpired(now)
	token := newToken()
	p := &Preview{Token: token, CreatedAt: now, Rows: rows}
	im.mu.Lock()
	im.previews[token] = p
	im.mu.Unlock()
	return p, nil
}

func isBlankRow(cells []string) bool {
	for _, c := range cells {
		if trimmed := stripControlChars(c); len(trimmed) > 0 {
			return false
		}
	}
	return true
}

func cell(cells []string, i int) string {
	if i < len(cells) {
		return cells[i]
	}
	return ""
}

// parseRow validates one row's fields and, if all pass, determines whether
// it represents a new member or an update to an existing one.
func (im *Importer) parseRow(ctx context.Context, rowNum int, raw []string) ImportRow {
	r := ImportRow{RowNum: rowNum}
	var errs []string
	var warns []string

	dept, err := NormalizeText(cell(raw, 0), MaxDeptLen, "소속")
	if err != nil || dept == "" {
		errs = append(errs, "소속: 값이 없거나 너무 깁니다")
	}
	r.Dept = dept

	name, err := NormalizeText(cell(raw, 1), MaxNameLen, "이름")
	if err != nil || name == "" {
		errs = append(errs, "이름: 값이 없거나 너무 깁니다")
	}
	r.Name = name

	officeTel, err := NormalizeOfficeTel(cell(raw, 2))
	if err != nil {
		errs = append(errs, "행정전화: 4자리 숫자여야 합니다")
	}
	r.OfficeTel = officeTel

	mobile, warned, err := NormalizeMobile(cell(raw, 3))
	if err != nil {
		errs = append(errs, "휴대전화: 01로 시작하는 숫자 11자리여야 합니다")
	}
	r.Mobile = mobile
	if warned {
		warns = append(warns, "휴대전화 앞자리 0이 없어 보정했습니다. 원본을 확인하세요.")
	}

	teamNo, err := NormalizeTeamNo(cell(raw, 4))
	if err != nil {
		errs = append(errs, "편성조: 값을 해석할 수 없습니다")
	} else {
		var exists int
		qerr := im.db.QueryRowContext(ctx, `SELECT 1 FROM team WHERE no = ?`, teamNo).Scan(&exists)
		if errors.Is(qerr, sql.ErrNoRows) {
			errs = append(errs, "편성조: 존재하지 않는 조입니다")
		} else if qerr != nil {
			errs = append(errs, "편성조: 조회 오류")
		} else {
			r.TeamNo = int64(teamNo)
		}
	}

	mission, err := NormalizeText(cell(raw, 5), MaxMissionLen, "임무내용")
	if err != nil {
		errs = append(errs, "임무내용: 너무 깁니다")
	}
	r.Mission = mission

	areaName, err := NormalizeText(cell(raw, 6), MaxNameLen, "임무지역")
	if err != nil {
		errs = append(errs, "임무지역: 너무 깁니다")
	}
	r.AreaName = areaName
	if areaName != "" {
		a, aerr := im.areas.GetActiveByName(ctx, areaName)
		if aerr != nil {
			errs = append(errs, "임무지역: 일치하는 활성 지역이 없습니다")
		} else {
			r.AreaID = a.ID
		}
	}

	note, err := NormalizeText(cell(raw, 7), MaxNoteLen, "비고")
	if err != nil {
		errs = append(errs, "비고: 너무 깁니다")
	}
	r.Note = note

	loginID, err := NormalizeText(cell(raw, 8), 50, "로그인ID")
	if err != nil || loginID == "" {
		errs = append(errs, "로그인ID: 값이 없거나 너무 깁니다")
	}
	r.LoginID = loginID

	r.Errors = errs
	r.Warnings = warns
	if len(errs) > 0 {
		r.Status = StatusError
		return r
	}

	// Identity match key across re-imports is the normalized mobile number
	// (SPEC R4: same person recognized across formatting variants).
	existing, err := im.members.GetByMobile(ctx, mobile)
	switch {
	case errors.Is(err, ErrNotFound):
		r.Status = StatusNew
	case err == nil:
		r.ExistingID = existing.ID
		if rowEqualsMember(r, existing) {
			r.Status = StatusUnchanged
		} else {
			r.Status = StatusChanged
		}
	default:
		r.Errors = append(r.Errors, "기존 인원 조회 오류")
		r.Status = StatusError
	}
	return r
}

func rowEqualsMember(r ImportRow, m *Member) bool {
	return r.LoginID == m.LoginID && r.Dept == m.Dept && r.Name == m.Name &&
		r.OfficeTel == m.OfficeTel && r.TeamNo == m.TeamNo && r.Mission == m.Mission &&
		r.AreaID == m.AreaID && r.Note == m.Note
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (im *Importer) sweepExpired(now time.Time) {
	im.mu.Lock()
	defer im.mu.Unlock()
	for k, p := range im.previews {
		if now.Sub(p.CreatedAt) > previewTTL {
			delete(im.previews, k)
		}
	}
}

// CommitResult summarizes what Commit did, including the one-time plaintext
// passwords for newly created accounts (SPEC §7.3: "신규 인원 초기 비밀번호
// 목록은 이 응답에서만 1회 제공").
type CommitResult struct {
	Created     int
	Updated     int
	Unchanged   int
	NewAccounts []struct {
		LoginID  string
		Password string
	}
}

// Commit persists a previously previewed batch. Rows with errors are never
// written (they shouldn't be present if the caller re-validated, but this
// is defense in depth). The preview token is consumed (single use).
func (im *Importer) Commit(ctx context.Context, token string, now time.Time) (*CommitResult, error) {
	im.mu.Lock()
	p, ok := im.previews[token]
	if ok {
		delete(im.previews, token)
	}
	im.mu.Unlock()
	if !ok {
		return nil, ErrPreviewExpired
	}

	res := &CommitResult{}
	for _, row := range p.Rows {
		switch row.Status {
		case StatusNew:
			m, pw, err := im.members.Create(ctx, NewInput{
				LoginID: row.LoginID, Dept: row.Dept, Name: row.Name, OfficeTel: row.OfficeTel,
				Mobile: row.Mobile, TeamNo: row.TeamNo, Mission: row.Mission, AreaID: row.AreaID, Note: row.Note,
			}, now)
			if err != nil {
				return nil, err
			}
			res.Created++
			res.NewAccounts = append(res.NewAccounts, struct {
				LoginID  string
				Password string
			}{LoginID: m.LoginID, Password: pw})
		case StatusChanged:
			dept, name, officeTel, mission, note := row.Dept, row.Name, row.OfficeTel, row.Mission, row.Note
			teamNo, areaID, loginID := row.TeamNo, row.AreaID, row.LoginID
			if err := im.members.Update(ctx, row.ExistingID, UpdateInput{
				Dept: &dept, Name: &name, OfficeTel: &officeTel, TeamNo: &teamNo,
				Mission: &mission, AreaID: &areaID, Note: &note,
			}, now); err != nil {
				return nil, err
			}
			if loginID != "" {
				if _, err := im.db.ExecContext(ctx, `UPDATE member SET login_id = ? WHERE id = ?`, loginID, row.ExistingID); err != nil {
					return nil, mapConstraintErr(err)
				}
			}
			res.Updated++
		case StatusUnchanged:
			res.Unchanged++
		}
	}
	return res, nil
}
