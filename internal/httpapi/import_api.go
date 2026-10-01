package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"emergencycallup/internal/audit"
	"emergencycallup/templates"
)

// readUpload reads the multipart "file" field (≤ maxImportFileSize), writing
// the error response itself and returning ok=false on failure.
func (s *Server) readUpload(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	now := s.Now()
	r.Body = http.MaxBytesReader(w, r.Body, maxImportFileSize)
	if err := r.ParseMultipartForm(maxImportFileSize); err != nil {
		writeError(w, now, "invalid", "파일이 너무 크거나 형식이 올바르지 않습니다.")
		return nil, false
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, now, "invalid", "file 필드가 필요합니다.")
		return nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, now, "invalid", "파일을 읽을 수 없습니다.")
		return nil, false
	}
	return data, true
}

func readCommitToken(w http.ResponseWriter, r *http.Request, s *Server) (string, bool) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		writeError(w, s.Now(), "invalid", "미리보기 토큰이 필요합니다.")
		return "", false
	}
	return req.Token, true
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// handleAreaImport implements POST /a/areas/import?mode=preview|commit —
// the 임무지역 counterpart of the roster import (same two-step flow).
func (s *Server) handleAreaImport(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	switch r.URL.Query().Get("mode") {
	case "preview":
		data, ok := s.readUpload(w, r)
		if !ok {
			return
		}
		token, rows, err := s.AreaImporter.Preview(r.Context(), data, now)
		if err != nil {
			writeError(w, now, "invalid", err.Error())
			return
		}
		out := make([]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"rowNum": row.RowNum, "name": row.Name, "summary": row.Summary, "status": row.Status,
				"errors": nonNil(row.Errors), "warnings": nonNil(row.Warnings),
			})
		}
		writeJSON(w, map[string]any{"t": now.UnixMilli(), "token": token, "rows": out})
	case "commit":
		token, ok := readCommitToken(w, r, s)
		if !ok {
			return
		}
		res, err := s.AreaImporter.Commit(r.Context(), token, now)
		if err != nil {
			writeError(w, now, "invalid", err.Error())
			return
		}
		admin := adminFromContext(r.Context())
		_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "area_import", Actor: audit.ActorAdmin(admin.LoginID),
			Data: map[string]any{"created": res.Created, "updated": res.Updated, "failed": len(res.Failed)}})
		writeJSON(w, map[string]any{"t": now.UnixMilli(), "created": res.Created, "updated": res.Updated, "failed": nonNil(res.Failed)})
	default:
		writeError(w, now, "invalid", "mode는 preview 또는 commit이어야 합니다.")
	}
}

// handlePlanImport implements POST /a/team-plans/import?mode=preview|commit.
func (s *Server) handlePlanImport(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	switch r.URL.Query().Get("mode") {
	case "preview":
		data, ok := s.readUpload(w, r)
		if !ok {
			return
		}
		token, teams, err := s.PlanImporter.Preview(r.Context(), data, now)
		if err != nil {
			writeError(w, now, "invalid", err.Error())
			return
		}
		out := make([]any, 0, len(teams))
		for _, t := range teams {
			rally := "자동(임무지역 중심)"
			if t.Rally != nil {
				rally = t.Rally.Addr
			}
			out = append(out, map[string]any{
				"rowNum": t.RowNum, "teamNo": t.TeamNo, "mission": t.Mission, "areaName": t.AreaName,
				"rally": rally, "cpCount": len(t.Checkpoints), "status": t.Status,
				"errors": nonNil(t.Errors), "warnings": nonNil(t.Warnings),
			})
		}
		writeJSON(w, map[string]any{"t": now.UnixMilli(), "token": token, "teams": out})
	case "commit":
		token, ok := readCommitToken(w, r, s)
		if !ok {
			return
		}
		admin := adminFromContext(r.Context())
		saved, err := s.PlanImporter.Commit(r.Context(), token, admin.LoginID, now)
		if err != nil {
			writeError(w, now, "invalid", err.Error())
			return
		}
		_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "team_plan_import", Actor: audit.ActorAdmin(admin.LoginID),
			Data: map[string]any{"saved": saved}})
		// Same as saving on screen: a running incident picks up the plans.
		plans, err := s.Plans.List(r.Context())
		if err != nil {
			writeError(w, now, "server", "서버 오류가 발생했습니다.")
			return
		}
		teams := make([]int64, 0, len(plans))
		for _, p := range plans {
			teams = append(teams, p.TeamNo)
		}
		applied, err := s.refreshActivePlans(r.Context(), teams, audit.ActorAdmin(admin.LoginID), now)
		if err != nil {
			writeError(w, now, "server", "계획은 저장했지만 진행 중인 발령에 반영하지 못했습니다.")
			return
		}
		body := map[string]any{"t": now.UnixMilli(), "saved": saved}
		if len(applied) > 0 {
			body["applied"] = applied
		}
		writeJSON(w, body)
	default:
		writeError(w, now, "invalid", "mode는 preview 또는 commit이어야 합니다.")
	}
}

// handleTemplateDownload implements GET /a/templates/{kind} — the standard
// import spreadsheets from templates/ (roster, areas, plans).
func (s *Server) handleTemplateDownload(w http.ResponseWriter, r *http.Request) {
	name, ok := templates.ByKind[r.PathValue("kind")]
	if !ok {
		writeError(w, s.Now(), "not_found", "없는 서식입니다.")
		return
	}
	data, err := templates.Files.ReadFile(name)
	if err != nil {
		writeError(w, s.Now(), "server", "서식 파일을 읽을 수 없습니다.")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\"template.xlsx\"; filename*=UTF-8''"+url.PathEscape(name))
	_, _ = w.Write(data)
}
