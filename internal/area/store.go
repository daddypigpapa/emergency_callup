package area

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	KindCircle  = "circle"
	KindPolygon = "polygon"
)

var (
	ErrNameTaken   = errors.New("area: an active area already has this name")
	ErrNotFound    = errors.New("area: not found")
	ErrNavOutside  = errors.New("area: nav point must be inside the polygon; provide one explicitly")
	ErrNavNotKorea = errors.New("area: nav point is outside Korea's bounding box")
)

// Row mirrors the `area` table (SPEC §4).
type Row struct {
	ID        int64
	Name      string
	Kind      string
	Lat       float64 // circle only
	Lng       float64 // circle only
	RadiusM   int     // circle only
	Polygon   []Point // polygon only
	NavLat    float64
	NavLng    float64
	BBox      [4]float64 // south, west, north, east
	Active    bool
	CreatedAt int64
}

// Store manages the `area` table.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// resolveNav applies SPEC §6.2's default-nav rule: if nav is nil, default to
// the circle center or polygon vertex-centroid; if that default falls
// outside a polygon, storage is refused and an explicit nav is required.
func resolveNav(kind string, center Point, poly []Point, nav *Point) (Point, error) {
	if nav != nil {
		if !InKorea(*nav) {
			return Point{}, ErrNavNotKorea
		}
		return *nav, nil
	}
	if kind == KindCircle {
		return center, nil
	}
	c := Centroid(poly)
	if !PointInPolygon(c, poly) {
		return Point{}, ErrNavOutside
	}
	return c, nil
}

// CreateCircle inserts a new circular area.
func (s *Store) CreateCircle(ctx context.Context, name string, center Point, radiusM int, nav *Point, now time.Time) (*Row, error) {
	if err := ValidateCircle(center, radiusM); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindCircle, center, nil, nav)
	if err != nil {
		return nil, err
	}
	bbox := CircleBBox(center, radiusM)
	return s.insert(ctx, name, KindCircle, center.Lat, center.Lng, radiusM, nil, navPt, bbox, now)
}

// CreatePolygon inserts a new polygon area.
func (s *Store) CreatePolygon(ctx context.Context, name string, poly []Point, nav *Point, now time.Time) (*Row, error) {
	if err := ValidatePolygon(poly); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindPolygon, Point{}, poly, nav)
	if err != nil {
		return nil, err
	}
	bbox := PolygonBBox(poly)
	return s.insert(ctx, name, KindPolygon, 0, 0, 0, poly, navPt, bbox, now)
}

func (s *Store) insert(ctx context.Context, name, kind string, lat, lng float64, radiusM int, poly []Point, nav Point, bbox [4]float64, now time.Time) (*Row, error) {
	var polyJSON, latVal, lngVal, radiusVal any
	if kind == KindPolygon {
		b, err := json.Marshal(poly)
		if err != nil {
			return nil, err
		}
		polyJSON = string(b)
		latVal, lngVal, radiusVal = nil, nil, nil
	} else {
		latVal, lngVal, radiusVal = lat, lng, radiusM
	}
	bboxJSON, err := json.Marshal(bbox)
	if err != nil {
		return nil, err
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO area(name, kind, lat, lng, radius_m, polygon, nav_lat, nav_lng, bbox, active, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		name, kind, latVal, lngVal, radiusVal, polyJSON, nav.Lat, nav.Lng, string(bboxJSON), now.UnixMilli())
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for i := 0; i+len("UNIQUE constraint failed") <= len(msg); i++ {
		if msg[i:i+len("UNIQUE constraint failed")] == "UNIQUE constraint failed" {
			return true
		}
	}
	return false
}

func scanRow(row interface{ Scan(...any) error }) (*Row, error) {
	var r Row
	var lat, lng sql.NullFloat64
	var radius sql.NullInt64
	var polyStr sql.NullString
	var bboxStr string
	var active int
	if err := row.Scan(&r.ID, &r.Name, &r.Kind, &lat, &lng, &radius, &polyStr, &r.NavLat, &r.NavLng, &bboxStr, &active, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.Lat, r.Lng = lat.Float64, lng.Float64
	r.RadiusM = int(radius.Int64)
	r.Active = active != 0
	if polyStr.Valid && polyStr.String != "" {
		if err := json.Unmarshal([]byte(polyStr.String), &r.Polygon); err != nil {
			return nil, fmt.Errorf("area: decode polygon: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(bboxStr), &r.BBox); err != nil {
		return nil, fmt.Errorf("area: decode bbox: %w", err)
	}
	return &r, nil
}

const selectCols = `id, name, kind, lat, lng, radius_m, polygon, nav_lat, nav_lng, bbox, active, created_at`

// GetByID returns an area by primary key, active or not.
func (s *Store) GetByID(ctx context.Context, id int64) (*Row, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM area WHERE id = ?`, id)
	r, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// GetActiveByName returns the currently-active area with this exact name,
// used by roster xlsx import to resolve the "임무지역" column (SPEC §4.2).
func (s *Store) GetActiveByName(ctx context.Context, name string) (*Row, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM area WHERE name = ? AND active = 1`, name)
	r, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// List returns areas, optionally restricted to active ones, ordered by name.
func (s *Store) List(ctx context.Context, activeOnly bool) ([]Row, error) {
	q := `SELECT ` + selectCols + ` FROM area`
	if activeOnly {
		q += ` WHERE active = 1`
	}
	q += ` ORDER BY name`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CopyCircle clones an existing area as a new active circle (possibly with a
// new name/geometry) and deactivates the source row. Areas are never
// mutated in place once created (SPEC §4: "사건에 한 번이라도 쓰인 지역은
// 수정하지 않는다... 새 행으로 복제하고 원본은 active=0").
func (s *Store) CopyCircle(ctx context.Context, srcID int64, name string, center Point, radiusM int, nav *Point, now time.Time) (*Row, error) {
	if _, err := s.GetByID(ctx, srcID); err != nil {
		return nil, err
	}
	newRow, err := s.CreateCircle(ctx, name, center, radiusM, nav, now)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE area SET active = 0 WHERE id = ?`, srcID); err != nil {
		return nil, err
	}
	return newRow, nil
}

// CopyPolygon is CopyCircle's polygon counterpart.
func (s *Store) CopyPolygon(ctx context.Context, srcID int64, name string, poly []Point, nav *Point, now time.Time) (*Row, error) {
	if _, err := s.GetByID(ctx, srcID); err != nil {
		return nil, err
	}
	newRow, err := s.CreatePolygon(ctx, name, poly, nav, now)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE area SET active = 0 WHERE id = ?`, srcID); err != nil {
		return nil, err
	}
	return newRow, nil
}

// SignedDistance computes SPEC §6.2's sd for point p against this area.
func (r *Row) SignedDistance(p Point) float64 {
	if r.Kind == KindCircle {
		return SignedDistanceCircle(Point{Lat: r.Lat, Lng: r.Lng}, r.RadiusM, p)
	}
	return SignedDistancePolygon(r.Polygon, p)
}

// REq returns the "equivalent radius" used by SPEC §6.1's n table.
func (r *Row) REq() float64 {
	if r.Kind == KindCircle {
		return float64(r.RadiusM)
	}
	return REqPolygon(r.Polygon)
}
