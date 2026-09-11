package roster

import (
	"context"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"emergencycallup/internal/area"
	"emergencycallup/internal/auth"
)

func buildXLSX(t *testing.T, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := f.GetSheetName(0)
	for r, row := range append([][]string{expectedHeader}, rows...) {
		for c, v := range row {
			cellRef, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellStr(sheet, cellRef, v); err != nil {
				t.Fatalf("set cell: %v", err)
			}
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

func newImporter(t *testing.T) (*Importer, context.Context) {
	t.Helper()
	db := testDB(t)
	h := auth.NewHasher()
	areas := area.NewStore(db.DB)
	members := NewStore(db.DB, h)
	im := NewImporter(db.DB, areas, members)
	return im, context.Background()
}

func sampleXLSXRow() []string {
	return []string{"안전정책과", "김도현", "1234", "010-1111-2222", "3조", "차단선 구축", "", "야간 대기조", "k1234"}
}

func TestImporter_PreviewNewRow(t *testing.T) {
	im, ctx := newImporter(t)
	data := buildXLSX(t, [][]string{sampleXLSXRow()})
	p, err := im.Preview(ctx, data, time.Now())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(p.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(p.Rows))
	}
	r := p.Rows[0]
	if r.Status != StatusNew {
		t.Errorf("status = %s, want new; errors=%v", r.Status, r.Errors)
	}
	if r.Mobile != "01011112222" {
		t.Errorf("mobile = %q", r.Mobile)
	}
}

func TestImporter_BadHeaderRejected(t *testing.T) {
	im, ctx := newImporter(t)
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	f.SetCellStr(sheet, "A1", "wrong")
	buf, _ := f.WriteToBuffer()
	f.Close()
	_, err := im.Preview(ctx, buf.Bytes(), time.Now())
	if err != ErrBadHeader {
		t.Fatalf("expected ErrBadHeader, got %v", err)
	}
}

func TestImporter_BadSignatureRejected(t *testing.T) {
	im, ctx := newImporter(t)
	_, err := im.Preview(ctx, []byte("not a real xlsx file"), time.Now())
	if err != ErrBadSignature {
		t.Fatalf("expected ErrBadSignature, got %v", err)
	}
}

func TestImporter_UnknownTeamRejected(t *testing.T) {
	im, ctx := newImporter(t)
	row := sampleXLSXRow()
	row[4] = "99조" // team 99 does not exist by default (only 1-10 seeded)
	data := buildXLSX(t, [][]string{row})
	p, err := im.Preview(ctx, data, time.Now())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if p.Rows[0].Status != StatusError {
		t.Fatalf("status = %s, want error", p.Rows[0].Status)
	}
}

func TestImporter_UnknownAreaRejected(t *testing.T) {
	im, ctx := newImporter(t)
	row := sampleXLSXRow()
	row[6] = "없는지역"
	data := buildXLSX(t, [][]string{row})
	p, err := im.Preview(ctx, data, time.Now())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if p.Rows[0].Status != StatusError {
		t.Fatalf("status = %s, want error", p.Rows[0].Status)
	}
}

func TestImporter_CommitCreatesAccountWithOneTimePassword(t *testing.T) {
	im, ctx := newImporter(t)
	data := buildXLSX(t, [][]string{sampleXLSXRow()})
	p, err := im.Preview(ctx, data, time.Now())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	res, err := im.Commit(ctx, p.Token, time.Now())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.Created != 1 {
		t.Fatalf("created = %d, want 1", res.Created)
	}
	if len(res.NewAccounts) != 1 || res.NewAccounts[0].LoginID != "k1234" {
		t.Fatalf("unexpected new accounts: %+v", res.NewAccounts)
	}
	if len(res.NewAccounts[0].Password) != auth.RandomPasswordLength {
		t.Errorf("password length wrong: %q", res.NewAccounts[0].Password)
	}

	got, err := im.members.GetByLoginID(ctx, "k1234")
	if err != nil {
		t.Fatalf("member should exist: %v", err)
	}
	if got.Mobile != "01011112222" {
		t.Errorf("mobile = %q", got.Mobile)
	}
}

// Commit consumes the token; reusing it must fail.
func TestImporter_CommitTokenIsSingleUse(t *testing.T) {
	im, ctx := newImporter(t)
	data := buildXLSX(t, [][]string{sampleXLSXRow()})
	p, err := im.Preview(ctx, data, time.Now())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := im.Commit(ctx, p.Token, time.Now()); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if _, err := im.Commit(ctx, p.Token, time.Now()); err != ErrPreviewExpired {
		t.Fatalf("second commit: expected ErrPreviewExpired, got %v", err)
	}
}

// R4: re-importing the same person with a different mobile-number format
// (but otherwise identical data) must resolve to the same existing member
// (status "unchanged"), not create a duplicate.
func TestImporter_R4_SamePersonAcrossMobileFormats(t *testing.T) {
	im, ctx := newImporter(t)

	// First import: dashed format.
	data1 := buildXLSX(t, [][]string{sampleXLSXRow()})
	p1, err := im.Preview(ctx, data1, time.Now())
	if err != nil {
		t.Fatalf("preview 1: %v", err)
	}
	if _, err := im.Commit(ctx, p1.Token, time.Now()); err != nil {
		t.Fatalf("commit 1: %v", err)
	}

	// Second import: plain 11-digit format, identical otherwise -> unchanged.
	row2 := sampleXLSXRow()
	row2[3] = "01011112222"
	data2 := buildXLSX(t, [][]string{row2})
	p2, err := im.Preview(ctx, data2, time.Now())
	if err != nil {
		t.Fatalf("preview 2: %v", err)
	}
	if p2.Rows[0].Status != StatusUnchanged {
		t.Fatalf("status = %s, want unchanged; errors=%v", p2.Rows[0].Status, p2.Rows[0].Errors)
	}

	// Third import: numeric-cell-corrupted format missing leading zero,
	// but with a changed mission -> should match the same person and report
	// "changed" (not "new"), with a warning about the mobile correction.
	row3 := sampleXLSXRow()
	row3[3] = "1011112222"
	row3[5] = "구조 지원" // mission changed
	data3 := buildXLSX(t, [][]string{row3})
	p3, err := im.Preview(ctx, data3, time.Now())
	if err != nil {
		t.Fatalf("preview 3: %v", err)
	}
	r3 := p3.Rows[0]
	if r3.Status != StatusChanged {
		t.Fatalf("status = %s, want changed; errors=%v", r3.Status, r3.Errors)
	}
	if len(r3.Warnings) == 0 {
		t.Error("expected a warning about the corrected mobile number")
	}

	// Only one member should exist in total.
	all, err := im.members.List(ctx, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("member count = %d, want 1", len(all))
	}
}
