package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"emergencycallup/internal/incident"
	smspkg "emergencycallup/internal/sms"
)

type smsPrepareRequest struct {
	Kind   string `json:"kind"`   // open|close|resend
	Via    string `json:"via"`    // manual|http
	Target string `json:"target"` // all|not_logged_in|failed|team:N
}

// resolveTargetMemberIDs implements SPEC §9.5's target filters against the
// given incident's assignments.
func (s *Server) resolveTargetMemberIDs(r *http.Request, incidentID int64, kind, target string) ([]int64, error) {
	assignments, err := s.Incidents.ListAssignments(r.Context(), incidentID)
	if err != nil {
		return nil, err
	}
	switch {
	case target == "all":
		ids := make([]int64, len(assignments))
		for i, a := range assignments {
			ids[i] = a.MemberID
		}
		return ids, nil
	case target == "not_logged_in":
		var ids []int64
		for _, a := range assignments {
			if a.State == incident.StateNotified {
				ids = append(ids, a.MemberID)
			}
		}
		return ids, nil
	case target == "failed":
		return s.SMS.LastFailedMemberIDs(r.Context(), incidentID, kind)
	case strings.HasPrefix(target, "team:"):
		teamNo, err := strconv.ParseInt(strings.TrimPrefix(target, "team:"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid team target")
		}
		var ids []int64
		for _, a := range assignments {
			if a.TeamNo == teamNo {
				ids = append(ids, a.MemberID)
			}
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("invalid target")
	}
}

// handleSMSPrepare implements POST /a/incidents/{id}/sms (SPEC §7.3, §9).
func (s *Server) handleSMSPrepare(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	incidentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "사건 ID가 올바르지 않습니다.")
		return
	}
	var req smsPrepareRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Kind == "" || req.Via == "" || req.Target == "" {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}

	inc, err := s.Incidents.GetByID(r.Context(), incidentID)
	if err != nil {
		writeError(w, now, "invalid", "사건을 찾을 수 없습니다.")
		return
	}
	if inc.Status == incident.IncidentClosed && req.Kind != smspkg.KindClose {
		writeError(w, now, "closed", "종료된 사건은 종료 문자만 보낼 수 있습니다.")
		return
	}

	memberIDs, err := s.resolveTargetMemberIDs(r, incidentID, req.Kind, req.Target)
	if err != nil {
		writeError(w, now, "invalid", err.Error())
		return
	}

	var text string
	switch req.Kind {
	case smspkg.KindOpen:
		text = smspkg.OpenText(s.Cfg.BaseURL, inc.TypeText, inc.Message)
	case smspkg.KindClose:
		text = smspkg.CloseText(inc.TypeText)
	case smspkg.KindResend:
		text = smspkg.ResendText(s.Cfg.BaseURL, inc.TypeText)
	default:
		writeError(w, now, "invalid", "kind는 open|close|resend 중 하나여야 합니다.")
		return
	}

	msgs := make([]smspkg.Message, 0, len(memberIDs))
	for _, id := range memberIDs {
		m, err := s.Members.GetByID(r.Context(), id)
		if err != nil {
			continue
		}
		msgs = append(msgs, smspkg.Message{MemberID: id, Mobile: m.Mobile, Text: text})
	}

	res, err := s.SMS.Prepare(r.Context(), incidentID, req.Kind, req.Via, text, msgs, "admin:"+admin.LoginID, now)
	if err != nil {
		if errors.Is(err, smspkg.ErrHTTPDisabled) {
			writeError(w, now, "invalid", "문자 API가 설정되어 있지 않습니다.")
			return
		}
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "batchId": res.BatchID, "text": res.Text, "count": res.Count, "bytes": res.Bytes, "lms": res.LMS})
}

// handleSMSRecipientsCSV implements GET .../sms/{batchId}/recipients.csv (SPEC §9.3, §8.2.5).
func (s *Server) handleSMSRecipientsCSV(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	batchID := r.PathValue("batchId")
	rows, err := s.SMS.RecipientsCSVRows(r.Context(), batchID)
	if err != nil {
		writeError(w, now, "invalid", "배치를 찾을 수 없습니다.")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=recipients.csv")
	w.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	w.Write([]byte("이름,휴대전화\r\n"))
	for _, row := range rows {
		w.Write([]byte(csvEscape(row.Name) + "," + csvEscape(row.Mobile) + "\r\n"))
	}
}

// handleSMSMarkSent implements POST .../sms/{batchId}/mark-sent.
func (s *Server) handleSMSMarkSent(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	batchID := r.PathValue("batchId")
	if err := s.SMS.MarkSent(r.Context(), batchID, "admin:"+admin.LoginID, now); err != nil {
		writeError(w, now, "invalid", "배치를 찾을 수 없습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

// handleSMSBatchResult implements GET .../sms/{batchId}.
func (s *Server) handleSMSBatchResult(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	batchID := r.PathValue("batchId")
	res, err := s.SMS.GetBatchResult(r.Context(), batchID)
	if err != nil {
		writeError(w, now, "invalid", "배치를 찾을 수 없습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "sent": res.Sent, "failed": res.Failed, "pending": res.Pending, "failedMembers": res.FailedMembers})
}

// csvEscape guards against CSV formula injection (SPEC §8.2.5, R12): a
// leading =, +, -, or @ gets a leading apostrophe.
func csvEscape(field string) string {
	if field != "" {
		switch field[0] {
		case '=', '+', '-', '@':
			field = "'" + field
		}
	}
	if strings.ContainsAny(field, ",\"\n\r") {
		field = `"` + strings.ReplaceAll(field, `"`, `""`) + `"`
	}
	return field
}
