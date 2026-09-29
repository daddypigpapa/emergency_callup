package templates

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"

	"emergencycallup/internal/area"
	"emergencycallup/internal/incident"
	"emergencycallup/internal/roster"
	"emergencycallup/internal/xlsxin"
)

// The committed templates must carry exactly the headers their importers
// accept. If this fails after changing an importer's columns, run
// `make templates` and commit the regenerated files.
func TestTemplatesMatchImporters(t *testing.T) {
	cases := []struct {
		kind   string
		sheets map[string][]string
	}{
		{"roster", map[string][]string{"명단": roster.ImportHeader}},
		{"areas", map[string][]string{area.ImportSheet: area.ImportHeader}},
		{"plans", map[string][]string{
			incident.PlanImportSheet:       incident.PlanImportHeader,
			incident.CheckpointImportSheet: incident.CheckpointImportHeader,
		}},
	}
	for _, c := range cases {
		data, err := Files.ReadFile(ByKind[c.kind])
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		f, err := excelize.OpenReader(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: open: %v", c.kind, err)
		}
		for sheet, want := range c.sheets {
			rows, err := f.GetRows(sheet)
			if err != nil || len(rows) == 0 || !xlsxin.HeaderMatches(rows[0], want) {
				t.Errorf("%s: sheet %q header stale — run `make templates`", c.kind, sheet)
			}
			if len(rows) > 1 {
				t.Errorf("%s: sheet %q must be header-only (examples belong on 작성방법), has %d rows", c.kind, sheet, len(rows))
			}
		}
		f.Close()
	}
}
