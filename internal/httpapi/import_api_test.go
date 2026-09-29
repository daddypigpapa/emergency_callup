package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"emergencycallup/internal/area"
)

func TestTemplateDownload(t *testing.T) {
	s := testServer(t)
	cookie := adminCookie(t, s)
	for _, kind := range []string{"roster", "areas", "plans"} {
		rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/templates/"+kind, nil, cookie)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "PK") {
			t.Errorf("%s: status %d, want 200 with an xlsx body", kind, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Disposition"), "filename*=UTF-8''") {
			t.Errorf("%s: Content-Disposition = %q, want a UTF-8 filename", kind, rec.Header().Get("Content-Disposition"))
		}
	}
	if rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/templates/nope", nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("unknown template: status %d, want 404", rec.Code)
	}
	if rec := reqJSON(t, s.Handler(), http.MethodGet, "/api/v1/a/templates/areas", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: status %d, want 401", rec.Code)
	}
}

func uploadXLSX(t *testing.T, h http.Handler, path string, sheet string, rows [][]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	f := excelize.NewFile()
	_ = f.SetSheetName("Sheet1", sheet)
	for i, r := range rows {
		vals := make([]any, len(r))
		for k, v := range r {
			vals[k] = v
		}
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		_ = f.SetSheetRow(sheet, cell, &vals)
	}
	var xb bytes.Buffer
	if err := f.Write(&xb); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "areas.xlsx")
	_, _ = fw.Write(xb.Bytes())
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAreaImportAPI_PreviewThenCommit(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	cookie := adminCookie(t, s)

	rec := uploadXLSX(t, h, "/api/v1/a/areas/import?mode=preview", area.ImportSheet, [][]string{
		area.ImportHeader,
		{"가져온원", "원", "", "35.8714", "128.6014", "300", "", ""},
		{"잘못된행", "원", "", "", "", "", "", ""},
	}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeJSON(t, rec)
	rows := body["rows"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["status"] != "new" || rows[1].(map[string]any)["status"] != "error" {
		t.Fatalf("rows = %v", rows)
	}

	rec = reqJSON(t, h, http.MethodPost, "/api/v1/a/areas/import?mode=commit", map[string]any{"token": body["token"]}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status %d: %s", rec.Code, rec.Body.String())
	}
	if res := decodeJSON(t, rec); res["created"].(float64) != 1 {
		t.Errorf("commit = %v, want 1 created", res)
	}
	rec = reqJSON(t, h, http.MethodGet, "/api/v1/a/areas", nil, cookie)
	if !strings.Contains(rec.Body.String(), "가져온원") {
		t.Error("imported area missing from the area list")
	}
}
