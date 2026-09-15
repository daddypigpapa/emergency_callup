package area

import "testing"

// R19: CellOf/CellBounds/CellCenter round-trip.
func TestGrid_CellRoundTrip(t *testing.T) {
	p := Point{Lat: 35.8714, Lng: 128.6014}
	size := 250
	c := CellOf(size, p)
	b := CellBounds(size, c)
	if !(b[0] <= p.Lat && p.Lat <= b[2] && b[1] <= p.Lng && p.Lng <= b[3]) {
		t.Fatalf("point %v not within its own cell bounds %v", p, b)
	}
	center := CellCenter(size, c)
	if !(b[0] <= center.Lat && center.Lat <= b[2] && b[1] <= center.Lng && center.Lng <= b[3]) {
		t.Fatalf("cell center %v not within cell bounds %v", center, b)
	}
	// Center recomputed from bounds should match CellCenter closely.
	midLat := (b[0] + b[2]) / 2
	midLng := (b[1] + b[3]) / 2
	if !almostEqual(midLat, center.Lat, 1e-9) || !almostEqual(midLng, center.Lng, 1e-9) {
		t.Errorf("bounds midpoint (%f,%f) != CellCenter (%f,%f)", midLat, midLng, center.Lat, center.Lng)
	}
}

// R20: SignedDistanceGrid sign and rough magnitude.
func TestGrid_SignedDistance(t *testing.T) {
	size := 250
	center := Point{Lat: 35.8714, Lng: 128.6014}
	c := CellOf(size, center)

	single := []Cell{c}
	sd := SignedDistanceGrid(size, single, CellCenter(size, c))
	if sd >= 0 {
		t.Errorf("cell center sd = %f, want negative", sd)
	}
	if !almostEqual(sd, -float64(size)/2, float64(size)*0.05) {
		t.Errorf("single-cell center sd = %f, want ~%f", sd, -float64(size)/2)
	}

	// A point ~100m outside the cell should read a positive distance close
	// to 100 (within a few percent, given the fixed-reference-latitude
	// approximation).
	dLat := 100.0 / metersPerDegLat
	far := Point{Lat: CellBounds(size, c)[2] + dLat, Lng: center.Lng}
	if sd := SignedDistanceGrid(size, single, far); sd <= 0 {
		t.Errorf("far point sd = %f, want positive", sd)
	} else if !almostEqual(sd, 100, 10) {
		t.Errorf("far point sd = %f, want ~100 (±10)", sd)
	}

	// A 3x3 block: the center cell's center should be deep inside, reading
	// close to -(size/2) since all four neighbors are also in the set.
	var block []Cell
	for di := int64(-1); di <= 1; di++ {
		for dj := int64(-1); dj <= 1; dj++ {
			block = append(block, Cell{I: c.I + di, J: c.J + dj})
		}
	}
	if sd := SignedDistanceGrid(size, block, CellCenter(size, c)); !almostEqual(sd, -float64(size)/2, float64(size)*0.03) {
		t.Errorf("3x3 block center sd = %f, want ~%f", sd, -float64(size)/2)
	}
}

// R21: ValidateGrid rejects bad sizes/counts; DedupeSortCells removes dupes.
func TestGrid_Validate(t *testing.T) {
	center := Point{Lat: 35.8714, Lng: 128.6014}
	c := CellOf(250, center)

	if err := ValidateGrid(300, []Cell{c}); err == nil {
		t.Error("size 300 should be rejected (not in GridSizes)")
	}
	if err := ValidateGrid(250, []Cell{c}); err != nil {
		t.Errorf("valid single-cell grid rejected: %v", err)
	}

	tooMany := make([]Cell, GridMaxCells+1)
	for i := range tooMany {
		tooMany[i] = Cell{I: int64(i), J: 0}
	}
	if err := ValidateGrid(250, tooMany); err == nil {
		t.Error("2501 cells should be rejected")
	}

	dup := []Cell{{I: 1, J: 1}, {I: 1, J: 1}, {I: 2, J: 1}}
	deduped := DedupeSortCells(dup)
	if len(deduped) != 2 {
		t.Fatalf("DedupeSortCells: got %d cells, want 2: %v", len(deduped), deduped)
	}
	if deduped[0] != (Cell{I: 1, J: 1}) || deduped[1] != (Cell{I: 2, J: 1}) {
		t.Errorf("DedupeSortCells: got %v, want sorted [{1 1} {2 1}]", deduped)
	}

	outside := Cell{I: 0, J: 0} // near lat/lng (0,0): far outside Korea
	if err := ValidateGrid(250, []Cell{outside}); err == nil {
		t.Error("cell outside Korea should be rejected")
	}
}

// R22: GridCenter always lands on a cell center within the selection, even
// for an L-shaped (non-convex) set.
func TestGrid_CenterAlwaysInSelection(t *testing.T) {
	size := 250
	origin := CellOf(size, Point{Lat: 35.8714, Lng: 128.6014})
	// L shape: 3 cells along I, plus 2 more extending south along J from
	// the last one.
	l := []Cell{
		{I: origin.I, J: origin.J},
		{I: origin.I + 1, J: origin.J},
		{I: origin.I + 2, J: origin.J},
		{I: origin.I + 2, J: origin.J - 1},
		{I: origin.I + 2, J: origin.J - 2},
	}
	center := GridCenter(size, l)
	found := false
	for _, c := range l {
		if CellCenter(size, c) == center {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GridCenter %v does not match any cell's center in %v", center, l)
	}
}
