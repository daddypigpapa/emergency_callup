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
	KindGrid    = "grid"
)

var (
	ErrNameTaken      = errors.New("area: an active area already has this name")
	ErrNotFound       = errors.New("area: not found")
	ErrNavOutside     = errors.New("area: nav point must be inside the polygon; provide one explicitly")
	ErrNavNotKorea    = errors.New("area: nav point is outside Korea's bounding box")
	ErrAreaInUse      = errors.New("area: in use by the active incident")
	ErrAreaReferenced = errors.New("area: still referenced by member roster entries")
)

// Row mirrors the `area` table (SPEC §4, docs/SPEC_AREA_EDITOR.md §3.2).
type Row struct {
	ID        int64
	Name      string
	Kind      string
	Lat       float64 // circle only
	Lng       float64 // circle only
	RadiusM   int     // circle only
	Polygon   []Point // polygon only
	GridSize  int     // grid only, meters (one of GridSizes)
	Cells     []Cell  // grid only
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
// the circle center, polygon vertex-centroid, or grid center
// (docs/SPEC_AREA_EDITOR.md §3.4); if a polygon's centroid default falls
// outside the polygon, storage is refused and an explicit nav is required
// (grid's default is always inside the selection by construction, so it has
// no equivalent failure mode).
func resolveNav(kind string, center Point, poly []Point, gridSize int, gridCells []Cell, nav *Point) (Point, error) {
	if nav != nil {
		if !InKorea(*nav) {
			return Point{}, ErrNavNotKorea
		}
		return *nav, nil
	}
	switch kind {
	case KindCircle:
		return center, nil
	case KindGrid:
		return GridCenter(gridSize, gridCells), nil
	default: // polygon
		c := Centroid(poly)
		if !PointInPolygon(c, poly) {
			return Point{}, ErrNavOutside
		}
		return c, nil
	}
}

// CreateCircle inserts a new circular area.
func (s *Store) CreateCircle(ctx context.Context, name string, center Point, radiusM int, nav *Point, now time.Time) (*Row, error) {
	if err := ValidateCircle(center, radiusM); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindCircle, center, nil, 0, nil, nav)
	if err != nil {
		return nil, err
	}
	bbox := CircleBBox(center, radiusM)
	return s.upsert(ctx, 0, name, KindCircle, center.Lat, center.Lng, radiusM, nil, 0, nil, navPt, bbox, now)
}

// CreatePolygon inserts a new polygon area.
func (s *Store) CreatePolygon(ctx context.Context, name string, poly []Point, nav *Point, now time.Time) (*Row, error) {
	if err := ValidatePolygon(poly); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindPolygon, Point{}, poly, 0, nil, nav)
	if err != nil {
		return nil, err
	}
	bbox := PolygonBBox(poly)
	return s.upsert(ctx, 0, name, KindPolygon, 0, 0, 0, poly, 0, nil, navPt, bbox, now)
}

// CreateGrid inserts a new grid-cell area (docs/SPEC_AREA_EDITOR.md §3).
// cells need not be pre-sorted/deduplicated — CreateGrid does that.
func (s *Store) CreateGrid(ctx context.Context, name string, size int, cells []Cell, nav *Point, now time.Time) (*Row, error) {
	cells = DedupeSortCells(cells)
	if err := ValidateGrid(size, cells); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindGrid, Point{}, nil, size, cells, nav)
	if err != nil {
		return nil, err
	}
	bbox := GridBBox(size, cells)
	return s.upsert(ctx, 0, name, KindGrid, 0, 0, 0, nil, size, cells, navPt, bbox, now)
}

// UpdateCircle replaces an existing area's geometry/name in place, refusing
// if the area is in use by the active incident (docs/SPEC_AREA_EDITOR.md
// §3.3) — unlike Copy*, which always leaves the original row untouched and
// creates a new one; Update* is for fixing a mistake in a not-yet-used
// (or no-longer-active) area during pre-registration.
func (s *Store) UpdateCircle(ctx context.Context, id int64, name string, center Point, radiusM int, nav *Point, now time.Time) (*Row, error) {
	if inUse, err := s.inUseByActiveIncident(ctx, id); err != nil {
		return nil, err
	} else if inUse {
		return nil, ErrAreaInUse
	}
	if err := ValidateCircle(center, radiusM); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindCircle, center, nil, 0, nil, nav)
	if err != nil {
		return nil, err
	}
	bbox := CircleBBox(center, radiusM)
	return s.upsert(ctx, id, name, KindCircle, center.Lat, center.Lng, radiusM, nil, 0, nil, navPt, bbox, now)
}

// UpdatePolygon is UpdateCircle's polygon counterpart.
func (s *Store) UpdatePolygon(ctx context.Context, id int64, name string, poly []Point, nav *Point, now time.Time) (*Row, error) {
	if inUse, err := s.inUseByActiveIncident(ctx, id); err != nil {
		return nil, err
	} else if inUse {
		return nil, ErrAreaInUse
	}
	if err := ValidatePolygon(poly); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindPolygon, Point{}, poly, 0, nil, nav)
	if err != nil {
		return nil, err
	}
	bbox := PolygonBBox(poly)
	return s.upsert(ctx, id, name, KindPolygon, 0, 0, 0, poly, 0, nil, navPt, bbox, now)
}

// UpdateGrid is UpdateCircle's grid counterpart. It also allows changing an
// existing area's kind entirely (e.g. circle -> grid) since the row is
// addressed by id, not by its current kind.
func (s *Store) UpdateGrid(ctx context.Context, id int64, name string, size int, cells []Cell, nav *Point, now time.Time) (*Row, error) {
	if inUse, err := s.inUseByActiveIncident(ctx, id); err != nil {
		return nil, err
	} else if inUse {
		return nil, ErrAreaInUse
	}
	cells = DedupeSortCells(cells)
	if err := ValidateGrid(size, cells); err != nil {
		return nil, err
	}
	navPt, err := resolveNav(KindGrid, Point{}, nil, size, cells, nav)
	if err != nil {
		return nil, err
	}
	bbox := GridBBox(size, cells)
	return s.upsert(ctx, id, name, KindGrid, 0, 0, 0, nil, size, cells, navPt, bbox, now)
}

// Deactivate sets an area inactive (soft delete), refusing if it's in use
// by the active incident or still referenced by a roster entry's default
// area (docs/SPEC_AREA_EDITOR.md §4.1).
func (s *Store) Deactivate(ctx context.Context, id int64) error {
	if inUse, err := s.inUseByActiveIncident(ctx, id); err != nil {
		return err
	} else if inUse {
		return ErrAreaInUse
	}
	n, err := s.referencedByMembers(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d명", ErrAreaReferenced, n)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE area SET active = 0 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) inUseByActiveIncident(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM (
			SELECT 1 FROM team_task tt JOIN incident i ON i.id = tt.incident_id
			WHERE i.status = 'active' AND tt.area_id = ?
			UNION ALL
			SELECT 1 FROM assignment a JOIN incident i ON i.id = a.incident_id
			WHERE i.status = 'active' AND a.eff_area_id = ?
		)`, id, id).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) referencedByMembers(ctx context.Context, id int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM member WHERE area_id = ?`, id).Scan(&n)
	return n, err
}

// cellPairs/cellsFromPairs convert between Cell and the [[i,j],...] JSON
// wire format used for area.cells (docs/SPEC_AREA_EDITOR.md §3.1) and the
// admin/field API payloads.
func cellPairs(cells []Cell) [][2]int64 {
	out := make([][2]int64, len(cells))
	for i, c := range cells {
		out[i] = [2]int64{c.I, c.J}
	}
	return out
}

func cellsFromPairs(pairs [][2]int64) []Cell {
	out := make([]Cell, len(pairs))
	for i, p := range pairs {
		out[i] = Cell{I: p[0], J: p[1]}
	}
	return out
}

// upsert inserts a new area (id == 0) or replaces an existing one's columns
// in place (id != 0). Only the columns relevant to kind are set; the others
// are stored NULL.
func (s *Store) upsert(ctx context.Context, id int64, name, kind string, lat, lng float64, radiusM int, poly []Point, gridSize int, cells []Cell, nav Point, bbox [4]float64, now time.Time) (*Row, error) {
	var polyJSON, latVal, lngVal, radiusVal, gridSizeVal, cellsJSON any
	switch kind {
	case KindPolygon:
		b, err := json.Marshal(poly)
		if err != nil {
			return nil, err
		}
		polyJSON = string(b)
	case KindGrid:
		gridSizeVal = gridSize
		b, err := json.Marshal(cellPairs(cells))
		if err != nil {
			return nil, err
		}
		cellsJSON = string(b)
	default: // circle
		latVal, lngVal, radiusVal = lat, lng, radiusM
	}
	bboxJSON, err := json.Marshal(bbox)
	if err != nil {
		return nil, err
	}

	if id == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO area(name, kind, lat, lng, radius_m, polygon, grid_size, cells, nav_lat, nav_lng, bbox, active, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
			name, kind, latVal, lngVal, radiusVal, polyJSON, gridSizeVal, cellsJSON, nav.Lat, nav.Lng, string(bboxJSON), now.UnixMilli())
		if err != nil {
			if isUniqueConstraint(err) {
				return nil, ErrNameTaken
			}
			return nil, err
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		return s.GetByID(ctx, newID)
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE area SET name=?, kind=?, lat=?, lng=?, radius_m=?, polygon=?, grid_size=?, cells=?, nav_lat=?, nav_lng=?, bbox=?
		 WHERE id=?`,
		name, kind, latVal, lngVal, radiusVal, polyJSON, gridSizeVal, cellsJSON, nav.Lat, nav.Lng, string(bboxJSON), id); err != nil {
		if isUniqueConstraint(err) {
			return nil, ErrNameTaken
		}
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
	var radius, gridSize sql.NullInt64
	var polyStr, cellsStr sql.NullString
	var bboxStr string
	var active int
	if err := row.Scan(&r.ID, &r.Name, &r.Kind, &lat, &lng, &radius, &polyStr, &gridSize, &cellsStr,
		&r.NavLat, &r.NavLng, &bboxStr, &active, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.Lat, r.Lng = lat.Float64, lng.Float64
	r.RadiusM = int(radius.Int64)
	r.GridSize = int(gridSize.Int64)
	r.Active = active != 0
	if polyStr.Valid && polyStr.String != "" {
		if err := json.Unmarshal([]byte(polyStr.String), &r.Polygon); err != nil {
			return nil, fmt.Errorf("area: decode polygon: %w", err)
		}
	}
	if cellsStr.Valid && cellsStr.String != "" {
		var pairs [][2]int64
		if err := json.Unmarshal([]byte(cellsStr.String), &pairs); err != nil {
			return nil, fmt.Errorf("area: decode cells: %w", err)
		}
		r.Cells = cellsFromPairs(pairs)
	}
	if err := json.Unmarshal([]byte(bboxStr), &r.BBox); err != nil {
		return nil, fmt.Errorf("area: decode bbox: %w", err)
	}
	return &r, nil
}

const selectCols = `id, name, kind, lat, lng, radius_m, polygon, grid_size, cells, nav_lat, nav_lng, bbox, active, created_at`

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
	switch r.Kind {
	case KindCircle:
		return SignedDistanceCircle(Point{Lat: r.Lat, Lng: r.Lng}, r.RadiusM, p)
	case KindGrid:
		return SignedDistanceGrid(r.GridSize, r.Cells, p)
	default:
		return SignedDistancePolygon(r.Polygon, p)
	}
}

// REq returns the "equivalent radius" used by SPEC §6.1's n table.
func (r *Row) REq() float64 {
	switch r.Kind {
	case KindCircle:
		return float64(r.RadiusM)
	case KindGrid:
		return REqGrid(r.GridSize, r.Cells)
	default:
		return REqPolygon(r.Polygon)
	}
}
