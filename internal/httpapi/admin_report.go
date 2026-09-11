package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

var seoulLocation = mustLoadSeoul()

func mustLoadSeoul() *time.Location {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.FixedZone("Asia/Seoul", 9*60*60) // fallback if tzdata is missing
	}
	return loc
}

func formatSeoul(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).In(seoulLocation).Format("2006-01-02 15:04:05")
}

// handleReportCSV implements GET /a/incidents/{id}/report.csv (SPEC §8.2.5,
// R10, R12): 조, 이름, 소속, 최초 접속, 응소 시각, 이탈 횟수, 마지막 상태,
// all times in Asia/Seoul regardless of server TZ, UTF-8 BOM, CSV-injection
// safe.
func (s *Server) handleReportCSV(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	incidentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "사건 ID가 올바르지 않습니다.")
		return
	}
	assignments, err := s.Incidents.ListAssignments(r.Context(), incidentID)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=report.csv")
	w.Write([]byte{0xEF, 0xBB, 0xBF})
	w.Write([]byte("조,이름,소속,최초접속,응소시각,이탈횟수,마지막상태\r\n"))

	for _, a := range assignments {
		m, err := s.Members.GetByID(r.Context(), a.MemberID)
		if err != nil {
			continue
		}
		firstLogin, arrived := "", ""
		if a.FirstLoginAt != nil {
			firstLogin = formatSeoul(*a.FirstLoginAt)
		}
		if a.ArrivedAt != nil {
			arrived = formatSeoul(*a.ArrivedAt)
		}
		row := []string{
			s.teamName(r.Context(), a.TeamNo), m.Name, m.Dept, firstLogin, arrived,
			strconv.Itoa(a.LeftCount), a.State,
		}
		for i, c := range row {
			if i > 0 {
				w.Write([]byte(","))
			}
			w.Write([]byte(csvEscape(c)))
		}
		w.Write([]byte("\r\n"))
	}
}
