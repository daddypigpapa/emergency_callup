package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"emergencycallup/internal/area"
	"emergencycallup/internal/audit"
	"emergencycallup/internal/auth"
	"emergencycallup/internal/roster"
)

// handleMembersList implements GET /a/members.
func (s *Server) handleMembersList(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	list, err := s.Members.List(r.Context(), false)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	out := make([]any, 0, len(list))
	for _, m := range list {
		out = append(out, map[string]any{
			"id": m.ID, "loginId": m.LoginID, "dept": m.Dept, "name": m.Name,
			"officeTel": m.OfficeTel, "mobile": maskMobile(m.Mobile), "teamNo": m.TeamNo,
			"mission": m.Mission, "areaId": m.AreaID, "note": m.Note, "active": m.Active,
		})
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "members": out})
}

type memberCreateRequest struct {
	LoginID   string `json:"loginId"`
	Dept      string `json:"dept"`
	Name      string `json:"name"`
	OfficeTel string `json:"officeTel"`
	Mobile    string `json:"mobile"`
	TeamNo    int64  `json:"teamNo"`
	Mission   string `json:"mission"`
	AreaID    int64  `json:"areaId"`
	Note      string `json:"note"`
}

// handleMemberCreate implements POST /a/members.
func (s *Server) handleMemberCreate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	var req memberCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	mobile, _, err := roster.NormalizeMobile(req.Mobile)
	if err != nil {
		writeError(w, now, "invalid", "휴대전화 형식이 올바르지 않습니다.")
		return
	}
	officeTel, err := roster.NormalizeOfficeTel(req.OfficeTel)
	if err != nil {
		writeError(w, now, "invalid", "행정전화 형식이 올바르지 않습니다.")
		return
	}
	m, pw, err := s.Members.Create(r.Context(), roster.NewInput{
		LoginID: req.LoginID, Dept: req.Dept, Name: req.Name, OfficeTel: officeTel,
		Mobile: mobile, TeamNo: req.TeamNo, Mission: req.Mission, AreaID: req.AreaID, Note: req.Note,
	}, now)
	if err != nil {
		switch {
		case errors.Is(err, roster.ErrLoginIDTaken):
			writeError(w, now, "conflict", "이미 사용 중인 로그인ID입니다.")
		case errors.Is(err, roster.ErrMobileTaken):
			writeError(w, now, "conflict", "이미 등록된 휴대전화번호입니다.")
		default:
			writeError(w, now, "server", "서버 오류가 발생했습니다.")
		}
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "member_import", Actor: audit.ActorAdmin(adminFromContext(r.Context()).LoginID), MemberID: &m.ID})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": m.ID, "initialPassword": pw})
}

type memberUpdateRequest struct {
	Dept      *string `json:"dept"`
	Name      *string `json:"name"`
	OfficeTel *string `json:"officeTel"`
	Mobile    *string `json:"mobile"`
	TeamNo    *int64  `json:"teamNo"`
	Mission   *string `json:"mission"`
	AreaID    *int64  `json:"areaId"`
	Note      *string `json:"note"`
	Active    *bool   `json:"active"`
}

// handleMemberUpdate implements PUT /a/members/{id}.
func (s *Server) handleMemberUpdate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "인원 ID가 올바르지 않습니다.")
		return
	}
	var req memberUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	if req.Active != nil {
		if err := s.Members.SetActive(r.Context(), id, *req.Active, now); err != nil {
			writeError(w, now, "server", "서버 오류가 발생했습니다.")
			return
		}
		if !*req.Active {
			if err := s.Sessions.RevokeAllForSubject(r.Context(), auth.KindMember, id); err != nil {
				writeError(w, now, "server", "서버 오류가 발생했습니다.")
				return
			}
		}
	}
	upd := roster.UpdateInput{Dept: req.Dept, Name: req.Name, OfficeTel: req.OfficeTel, Mobile: req.Mobile,
		TeamNo: req.TeamNo, Mission: req.Mission, AreaID: req.AreaID, Note: req.Note}
	if err := s.Members.Update(r.Context(), id, upd, now); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "member_update", Actor: audit.ActorAdmin(admin.LoginID), MemberID: &id})
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

// handleMemberPasswordReset implements POST /a/members/{id}/password-reset.
func (s *Server) handleMemberPasswordReset(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "인원 ID가 올바르지 않습니다.")
		return
	}
	pw, err := s.Members.PasswordReset(r.Context(), id)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	if err := s.Sessions.RevokeAllForSubject(r.Context(), auth.KindMember, id); err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "password_reset", Actor: audit.ActorAdmin(admin.LoginID), MemberID: &id})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "password": pw})
}

const maxImportFileSize = 5 << 20 // 5MB (SPEC §11.3)

// handleMemberImport implements POST /a/members/import?mode=preview|commit.
func (s *Server) handleMemberImport(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	mode := r.URL.Query().Get("mode")

	if mode == "commit" {
		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
			writeError(w, now, "invalid", "미리보기 토큰이 필요합니다.")
			return
		}
		res, err := s.Importer.Commit(r.Context(), req.Token, now)
		if err != nil {
			writeError(w, now, "invalid", "미리보기가 만료되었거나 존재하지 않습니다.")
			return
		}
		accounts := make([]any, 0, len(res.NewAccounts))
		for _, a := range res.NewAccounts {
			accounts = append(accounts, map[string]any{"loginId": a.LoginID, "password": a.Password})
		}
		writeJSON(w, map[string]any{"t": now.UnixMilli(), "created": res.Created, "updated": res.Updated, "unchanged": res.Unchanged, "newAccounts": accounts})
		return
	}

	if mode != "preview" {
		writeError(w, now, "invalid", "mode는 preview 또는 commit이어야 합니다.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImportFileSize)
	if err := r.ParseMultipartForm(maxImportFileSize); err != nil {
		writeError(w, now, "invalid", "파일이 너무 크거나 형식이 올바르지 않습니다.")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, now, "invalid", "file 필드가 필요합니다.")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, now, "invalid", "파일을 읽을 수 없습니다.")
		return
	}

	preview, err := s.Importer.Preview(r.Context(), data, now)
	if err != nil {
		writeError(w, now, "invalid", err.Error())
		return
	}
	rows := make([]any, 0, len(preview.Rows))
	for _, row := range preview.Rows {
		rows = append(rows, map[string]any{
			"rowNum": row.RowNum, "loginId": row.LoginID, "dept": row.Dept, "name": row.Name,
			"mobile": row.Mobile, "teamNo": row.TeamNo, "mission": row.Mission, "areaName": row.AreaName,
			"note": row.Note, "status": row.Status, "errors": row.Errors, "warnings": row.Warnings,
		})
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "token": preview.Token, "rows": rows})
}

// --- Areas ---

func (s *Server) handleAreasList(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	list, err := s.Areas.List(r.Context(), true)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	out := make([]any, 0, len(list))
	for i := range list {
		a := &list[i]
		size, cells := gridFieldsJSON(a)
		out = append(out, map[string]any{
			"id": a.ID, "name": a.Name, "kind": a.Kind, "lat": a.Lat, "lng": a.Lng, "r": a.RadiusM,
			"polygon": polygonJSON(a), "size": size, "cells": cells,
			"nav": [2]float64{a.NavLat, a.NavLng}, "bbox": a.BBox,
		})
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "areas": out})
}

type areaCreateRequest struct {
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Lat     float64      `json:"lat"`
	Lng     float64      `json:"lng"`
	RadiusM int          `json:"r"`
	Polygon [][2]float64 `json:"polygon"`
	Size    int          `json:"size"`
	Cells   [][2]int64   `json:"cells"`
	Nav     *[2]float64  `json:"nav"`
}

func areaFromRequest(req areaCreateRequest) (poly []area.Point, cells []area.Cell, nav *area.Point) {
	poly = make([]area.Point, len(req.Polygon))
	for i, p := range req.Polygon {
		poly[i] = area.Point{Lat: p[0], Lng: p[1]}
	}
	cells = make([]area.Cell, len(req.Cells))
	for i, c := range req.Cells {
		cells[i] = area.Cell{I: c[0], J: c[1]}
	}
	if req.Nav != nil {
		nav = &area.Point{Lat: req.Nav[0], Lng: req.Nav[1]}
	}
	return
}

func (s *Server) handleAreaCreate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	var req areaCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Name == "" {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	poly, cells, nav := areaFromRequest(req)
	var row *area.Row
	var err error
	switch req.Kind {
	case area.KindCircle:
		row, err = s.Areas.CreateCircle(r.Context(), req.Name, area.Point{Lat: req.Lat, Lng: req.Lng}, req.RadiusM, nav, now)
	case area.KindPolygon:
		row, err = s.Areas.CreatePolygon(r.Context(), req.Name, poly, nav, now)
	case area.KindGrid:
		row, err = s.Areas.CreateGrid(r.Context(), req.Name, req.Size, cells, nav, now)
	default:
		writeError(w, now, "invalid", "kind는 circle, polygon, grid 중 하나여야 합니다.")
		return
	}
	if err != nil {
		writeError(w, now, "invalid", err.Error())
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "area_create", Actor: audit.ActorAdmin(admin.LoginID)})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": row.ID})
}

// handleAreaUpdate implements PUT /a/areas/{id} (docs/SPEC_AREA_EDITOR.md
// §4.1) — edits an area in place, unlike handleAreaCopy which always
// leaves the source row untouched. Refused with 409 while the area is in
// use by the active incident (area.ErrAreaInUse).
func (s *Server) handleAreaUpdate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "지역 ID가 올바르지 않습니다.")
		return
	}
	var req areaCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Name == "" {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	poly, cells, nav := areaFromRequest(req)
	var row *area.Row
	switch req.Kind {
	case area.KindCircle:
		row, err = s.Areas.UpdateCircle(r.Context(), id, req.Name, area.Point{Lat: req.Lat, Lng: req.Lng}, req.RadiusM, nav, now)
	case area.KindPolygon:
		row, err = s.Areas.UpdatePolygon(r.Context(), id, req.Name, poly, nav, now)
	case area.KindGrid:
		row, err = s.Areas.UpdateGrid(r.Context(), id, req.Name, req.Size, cells, nav, now)
	default:
		writeError(w, now, "invalid", "kind는 circle, polygon, grid 중 하나여야 합니다.")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, area.ErrAreaInUse):
			writeError(w, now, "conflict", "현재 활성 사건에서 사용 중인 지역은 수정할 수 없습니다.")
		case errors.Is(err, area.ErrNotFound):
			writeError(w, now, "invalid", "지역을 찾을 수 없습니다.")
		default:
			writeError(w, now, "invalid", err.Error())
		}
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "area_update", Actor: audit.ActorAdmin(admin.LoginID)})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": row.ID})
}

// handleAreaDelete implements DELETE /a/areas/{id} — a soft delete
// (active=0), refused with 409 while in use by the active incident or
// still referenced by a member's default area (docs/SPEC_AREA_EDITOR.md §4.1).
func (s *Server) handleAreaDelete(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "지역 ID가 올바르지 않습니다.")
		return
	}
	if err := s.Areas.Deactivate(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, area.ErrAreaInUse):
			writeError(w, now, "conflict", "현재 활성 사건에서 사용 중인 지역은 삭제할 수 없습니다.")
		case errors.Is(err, area.ErrAreaReferenced):
			writeError(w, now, "conflict", err.Error())
		case errors.Is(err, area.ErrNotFound):
			writeError(w, now, "invalid", "지역을 찾을 수 없습니다.")
		default:
			writeError(w, now, "server", "서버 오류가 발생했습니다.")
		}
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "area_delete", Actor: audit.ActorAdmin(admin.LoginID)})
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

// handleGeoCell implements GET /a/geo/cell?size=&lat=&lng= — lets the
// editor's JS cross-check its own cell math against the server's
// (docs/SPEC_AREA_EDITOR.md §4.1).
func (s *Server) handleGeoCell(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	size, errSize := strconv.Atoi(r.URL.Query().Get("size"))
	lat, errLat := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	if errSize != nil || errLat != nil || errLng != nil {
		writeError(w, now, "invalid", "size, lat, lng 파라미터가 필요합니다.")
		return
	}
	c := area.CellOf(size, area.Point{Lat: lat, Lng: lng})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "i": c.I, "j": c.J, "bounds": area.CellBounds(size, c)})
}

func (s *Server) handleAreaCopy(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	admin := adminFromContext(r.Context())
	srcID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "지역 ID가 올바르지 않습니다.")
		return
	}
	var req areaCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Name == "" {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	var nav *area.Point
	if req.Nav != nil {
		nav = &area.Point{Lat: req.Nav[0], Lng: req.Nav[1]}
	}
	var row *area.Row
	switch req.Kind {
	case area.KindCircle:
		row, err = s.Areas.CopyCircle(r.Context(), srcID, req.Name, area.Point{Lat: req.Lat, Lng: req.Lng}, req.RadiusM, nav, now)
	case area.KindPolygon:
		poly := make([]area.Point, len(req.Polygon))
		for i, p := range req.Polygon {
			poly[i] = area.Point{Lat: p[0], Lng: p[1]}
		}
		row, err = s.Areas.CopyPolygon(r.Context(), srcID, req.Name, poly, nav, now)
	default:
		writeError(w, now, "invalid", "kind는 circle 또는 polygon이어야 합니다.")
		return
	}
	if err != nil {
		writeError(w, now, "invalid", err.Error())
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "area_copy", Actor: audit.ActorAdmin(admin.LoginID)})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": row.ID})
}

// --- Presets ---

func (s *Server) handlePresetsList(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	kind := r.URL.Query().Get("kind")
	list, err := s.Presets.List(r.Context(), kind, false)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	out := make([]any, 0, len(list))
	for _, p := range list {
		out = append(out, map[string]any{"id": p.ID, "kind": p.Kind, "text": p.Text, "sort": p.Sort, "active": p.Active})
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "presets": out})
}

type presetCreateRequest struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Sort int    `json:"sort"`
}

func (s *Server) handlePresetCreate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	var req presetCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Text == "" {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	p, err := s.Presets.Create(r.Context(), req.Kind, req.Text, req.Sort)
	if err != nil {
		writeError(w, now, "invalid", err.Error())
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": p.ID})
}

type presetUpdateRequest struct {
	Text   string `json:"text"`
	Sort   int    `json:"sort"`
	Active bool   `json:"active"`
}

func (s *Server) handlePresetUpdate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "프리셋 ID가 올바르지 않습니다.")
		return
	}
	var req presetUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	if err := s.Presets.Update(r.Context(), id, req.Text, req.Sort, req.Active); err != nil {
		writeError(w, now, "invalid", "프리셋을 찾을 수 없습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

func (s *Server) handlePresetDelete(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "프리셋 ID가 올바르지 않습니다.")
		return
	}
	if err := s.Presets.Delete(r.Context(), id); err != nil {
		writeError(w, now, "invalid", "프리셋을 찾을 수 없습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

// --- Admin accounts ---

func (s *Server) handleAdminsList(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	list, err := s.Admins.List(r.Context())
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	out := make([]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{"id": a.ID, "loginId": a.LoginID, "name": a.Name, "dept": a.Dept, "role": a.Role, "active": a.Active})
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "admins": out})
}

type adminCreateRequest struct {
	LoginID  string `json:"loginId"`
	Name     string `json:"name"`
	Dept     string `json:"dept"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	actor := adminFromContext(r.Context())
	var req adminCreateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.LoginID == "" || req.Name == "" || req.Password == "" {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	a, err := s.Admins.Create(r.Context(), req.LoginID, req.Password, req.Name, req.Dept, req.Role, now)
	if err != nil {
		writeError(w, now, "invalid", err.Error())
		return
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "admin_user_update", Actor: audit.ActorAdmin(actor.LoginID)})
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "id": a.ID})
}

type adminUpdateRequest struct {
	Active *bool   `json:"active"`
	Role   *string `json:"role"`
}

func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	actor := adminFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "관리자 ID가 올바르지 않습니다.")
		return
	}
	var req adminUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, now, "invalid", "요청 형식이 올바르지 않습니다.")
		return
	}
	if req.Role != nil {
		if err := s.Admins.UpdateRole(r.Context(), id, *req.Role); err != nil {
			writeError(w, now, "invalid", err.Error())
			return
		}
	}
	if req.Active != nil {
		if err := s.Admins.SetActive(r.Context(), id, *req.Active); err != nil {
			writeError(w, now, "invalid", err.Error())
			return
		}
		if !*req.Active {
			if err := s.Sessions.RevokeAllForSubject(r.Context(), auth.KindAdmin, id); err != nil {
				writeError(w, now, "server", "서버 오류가 발생했습니다.")
				return
			}
		}
	}
	_ = s.Audit.Record(r.Context(), now, audit.Event{Type: "admin_user_update", Actor: audit.ActorAdmin(actor.LoginID)})
	writeJSON(w, map[string]any{"t": now.UnixMilli()})
}

// --- Events (audit log) ---

func (s *Server) handleEventsList(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	incidentIDStr := r.URL.Query().Get("incident")
	incidentID, err := strconv.ParseInt(incidentIDStr, 10, 64)
	if err != nil {
		writeError(w, now, "invalid", "incident 파라미터가 필요합니다.")
		return
	}
	rows, err := s.Audit.ListByIncident(r.Context(), incidentID, 1000)
	if err != nil {
		writeError(w, now, "server", "서버 오류가 발생했습니다.")
		return
	}
	writeJSON(w, map[string]any{"t": now.UnixMilli(), "events": rows})
}
