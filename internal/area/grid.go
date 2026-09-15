// Grid-based mission areas (docs/SPEC_AREA_EDITOR.md §3.1, §3.3): a fixed
// nationwide grid of square cells, identified by integer (size, i, j)
// indices, used as an alternative to circle/polygon areas.
//
// The JS counterpart (web/shared/grid.js) must compute cell indices and
// bounds with the exact same constants and formulas — the client draws
// cells and lets an admin pick them, but the server is the source of truth
// for which cell a coordinate belongs to.
package area

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// GridSizes are the allowed grid cell sizes in meters.
var GridSizes = []int{100, 250, 500, 1000}

const (
	GridMinCells = 1
	GridMaxCells = 2500
)

// gridRefLat is a single fixed reference latitude used to derive one
// nationwide longitude-degree width per grid size, so every client and the
// server compute identical cell boundaries regardless of where in Korea a
// cell sits. This trades roughly ±5% east-west cell width error at Korea's
// latitude extremes (33~38.6°) for a grid that never needs per-latitude
// reprojection — acceptable against the 30m departure margin and normal
// GPS error (docs/SPEC_AREA_EDITOR.md §3.1).
const gridRefLat = 36.0
const metersPerDegLat = 111320.0

// Cell identifies one grid square by its (size, i, j) integer indices. The
// size itself is not part of the struct — callers track it alongside a
// []Cell, matching how it's stored (area.grid_size + area.cells).
type Cell struct {
	I, J int64
}

func gridDLat(size int) float64 { return float64(size) / metersPerDegLat }

func gridDLng(size int) float64 {
	return float64(size) / (metersPerDegLat * math.Cos(gridRefLat*math.Pi/180))
}

// CellOf returns the cell containing p, for the given grid size.
func CellOf(size int, p Point) Cell {
	dLat, dLng := gridDLat(size), gridDLng(size)
	return Cell{I: int64(math.Floor(p.Lng / dLng)), J: int64(math.Floor(p.Lat / dLat))}
}

// CellBounds returns [south, west, north, east] for cell c at the given size.
func CellBounds(size int, c Cell) [4]float64 {
	dLat, dLng := gridDLat(size), gridDLng(size)
	south := float64(c.J) * dLat
	west := float64(c.I) * dLng
	return [4]float64{south, west, south + dLat, west + dLng}
}

// CellCenter returns the center point of cell c at the given size.
func CellCenter(size int, c Cell) Point {
	dLat, dLng := gridDLat(size), gridDLng(size)
	return Point{Lat: (float64(c.J) + 0.5) * dLat, Lng: (float64(c.I) + 0.5) * dLng}
}

func validGridSize(size int) bool {
	for _, s := range GridSizes {
		if s == size {
			return true
		}
	}
	return false
}

var (
	ErrGridSize       = fmt.Errorf("area: grid size must be one of %v", GridSizes)
	ErrGridCellCount  = fmt.Errorf("area: grid must have between %d and %d cells", GridMinCells, GridMaxCells)
	ErrGridOutOfKorea = errors.New("area: a grid cell is outside Korea's bounding box")
)

// DedupeSortCells removes duplicate cells and sorts the remainder into a
// stable order (by J then I), so storage is canonical regardless of the
// order a client selected them in. Callers should run this before
// ValidateGrid / insert.
func DedupeSortCells(cells []Cell) []Cell {
	seen := make(map[Cell]bool, len(cells))
	out := make([]Cell, 0, len(cells))
	for _, c := range cells {
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].J != out[j].J {
			return out[i].J < out[j].J
		}
		return out[i].I < out[j].I
	})
	return out
}

// ValidateGrid checks the grid size, cell count, and that every cell's
// center falls within Korea. Callers should DedupeSortCells first (a
// duplicate-inflated count would otherwise pass the max-cells check
// incorrectly).
func ValidateGrid(size int, cells []Cell) error {
	if !validGridSize(size) {
		return ErrGridSize
	}
	if len(cells) < GridMinCells || len(cells) > GridMaxCells {
		return ErrGridCellCount
	}
	for _, c := range cells {
		if !InKorea(CellCenter(size, c)) {
			return ErrGridOutOfKorea
		}
	}
	return nil
}

// GridBBox returns [south, west, north, east] covering every cell.
func GridBBox(size int, cells []Cell) [4]float64 {
	b := CellBounds(size, cells[0])
	south, west, north, east := b[0], b[1], b[2], b[3]
	for _, c := range cells[1:] {
		cb := CellBounds(size, c)
		south = math.Min(south, cb[0])
		west = math.Min(west, cb[1])
		north = math.Max(north, cb[2])
		east = math.Max(east, cb[3])
	}
	return [4]float64{south, west, north, east}
}

// GridCenter returns the center of the cell whose center is nearest to the
// mean of every cell's center (docs/SPEC_AREA_EDITOR.md §3.4) —
// guaranteeing the result always falls inside the grid, even for L-shaped
// or otherwise non-convex selections where a plain mean could land in a gap.
func GridCenter(size int, cells []Cell) Point {
	var sumLat, sumLng float64
	for _, c := range cells {
		p := CellCenter(size, c)
		sumLat += p.Lat
		sumLng += p.Lng
	}
	n := float64(len(cells))
	mean := Point{Lat: sumLat / n, Lng: sumLng / n}

	best := cells[0]
	bestD := math.Inf(1)
	for _, c := range cells {
		d := Haversine(mean, CellCenter(size, c))
		if d < bestD {
			bestD = d
			best = c
		}
	}
	return CellCenter(size, best)
}

func cellSet(cells []Cell) map[Cell]bool {
	m := make(map[Cell]bool, len(cells))
	for _, c := range cells {
		m[c] = true
	}
	return m
}

// SignedDistanceGrid returns the signed distance from p to the grid
// boundary: negative when p is inside (docs/SPEC_AREA_EDITOR.md §3.3).
//
// Inside: distance to the nearest cell edge that borders a cell NOT in the
// set (an outer boundary edge). If all four neighbors are also in the set,
// p is deep inside a solid block — return a fixed -(size/2) rather than
// searching further, since callers only need the sign and a margin of a
// few tens of meters (the 30m departure threshold), not an exact value.
//
// Outside: the shortest distance to any cell's rectangle.
func SignedDistanceGrid(size int, cells []Cell, p Point) float64 {
	set := cellSet(cells)
	c := CellOf(size, p)
	px, py := projectMeters(gridRefLat, p)

	if set[c] {
		b := CellBounds(size, c)
		minDist := math.Inf(1)
		if !set[(Cell{I: c.I, J: c.J - 1})] {
			_, sy := projectMeters(gridRefLat, Point{Lat: b[0], Lng: p.Lng})
			minDist = math.Min(minDist, py-sy)
		}
		if !set[(Cell{I: c.I, J: c.J + 1})] {
			_, ny := projectMeters(gridRefLat, Point{Lat: b[2], Lng: p.Lng})
			minDist = math.Min(minDist, ny-py)
		}
		if !set[(Cell{I: c.I - 1, J: c.J})] {
			wx, _ := projectMeters(gridRefLat, Point{Lat: p.Lat, Lng: b[1]})
			minDist = math.Min(minDist, px-wx)
		}
		if !set[(Cell{I: c.I + 1, J: c.J})] {
			ex, _ := projectMeters(gridRefLat, Point{Lat: p.Lat, Lng: b[3]})
			minDist = math.Min(minDist, ex-px)
		}
		if math.IsInf(minDist, 1) {
			return -float64(size) / 2
		}
		return -minDist
	}

	minDist := math.Inf(1)
	for _, cc := range cells {
		b := CellBounds(size, cc)
		x0, y0 := projectMeters(gridRefLat, Point{Lat: b[0], Lng: b[1]})
		x1, y1 := projectMeters(gridRefLat, Point{Lat: b[2], Lng: b[3]})
		if d := distPointToRect(px, py, x0, y0, x1, y1); d < minDist {
			minDist = d
		}
	}
	return minDist
}

func distPointToRect(px, py, x0, y0, x1, y1 float64) float64 {
	dx := math.Max(x0-px, math.Max(0, px-x1))
	dy := math.Max(y0-py, math.Max(0, py-y1))
	return math.Hypot(dx, dy)
}

// REqGrid returns the "equivalent radius" used by SPEC §6.1's transmit
// interval table: the radius of a circle with the same total area as the
// grid selection.
func REqGrid(size int, cells []Cell) float64 {
	n := float64(len(cells))
	return math.Sqrt(n * float64(size) * float64(size) / math.Pi)
}
