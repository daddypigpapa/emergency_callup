package area

import (
	"math"
	"testing"
)

func almostEqual(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestHaversine_KnownDistance(t *testing.T) {
	// Roughly 1 degree of latitude ≈ 111.32 km.
	a := Point{Lat: 35.0, Lng: 128.0}
	b := Point{Lat: 36.0, Lng: 128.0}
	d := Haversine(a, b)
	if !almostEqual(d, 111320, 500) {
		t.Errorf("distance = %f, want ~111320", d)
	}
}

func TestValidateCircle(t *testing.T) {
	center := Point{Lat: 35.8714, Lng: 128.6014}
	if err := ValidateCircle(center, 150); err != nil {
		t.Errorf("valid circle rejected: %v", err)
	}
	if err := ValidateCircle(center, 49); err == nil {
		t.Error("radius below minimum should be rejected")
	}
	if err := ValidateCircle(center, 5001); err == nil {
		t.Error("radius above maximum should be rejected")
	}
	if err := ValidateCircle(Point{Lat: 10, Lng: 128}, 150); err == nil {
		t.Error("center outside Korea should be rejected")
	}
}

func TestSignedDistanceCircle_InsideNegativeOutsidePositive(t *testing.T) {
	center := Point{Lat: 35.8714, Lng: 128.6014}
	radius := 100
	inside := center // distance 0 from center, deep inside
	if sd := SignedDistanceCircle(center, radius, inside); sd >= 0 {
		t.Errorf("center point sd = %f, want negative", sd)
	}

	// A point ~200m north should be outside a 100m circle.
	far := Point{Lat: center.Lat + 200.0/111320.0, Lng: center.Lng}
	if sd := SignedDistanceCircle(center, radius, far); sd <= 0 {
		t.Errorf("far point sd = %f, want positive", sd)
	}
}

func TestCircleBBox_ContainsCenter(t *testing.T) {
	center := Point{Lat: 35.8714, Lng: 128.6014}
	bbox := CircleBBox(center, 150)
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	if !(south < center.Lat && center.Lat < north) {
		t.Errorf("bbox lat range %v does not contain center lat %v", bbox, center.Lat)
	}
	if !(west < center.Lng && center.Lng < east) {
		t.Errorf("bbox lng range %v does not contain center lng %v", bbox, center.Lng)
	}
}

func square(cx, cy, halfSide float64) []Point {
	return []Point{
		{Lat: cx - halfSide, Lng: cy - halfSide},
		{Lat: cx - halfSide, Lng: cy + halfSide},
		{Lat: cx + halfSide, Lng: cy + halfSide},
		{Lat: cx + halfSide, Lng: cy - halfSide},
	}
}

func TestValidatePolygon_SimpleSquareOK(t *testing.T) {
	poly := square(35.87, 128.60, 0.01) // ~2.2km x 2.2km square
	if err := ValidatePolygon(poly); err != nil {
		t.Errorf("simple square rejected: %v", err)
	}
}

// R9: a bowtie-ordered quadrilateral (edges cross) must be rejected.
func TestValidatePolygon_BowtieRejected(t *testing.T) {
	// Swap two vertices of a valid square to create a self-intersecting bowtie.
	poly := []Point{
		{Lat: 35.860, Lng: 128.590},
		{Lat: 35.880, Lng: 128.610}, // swapped
		{Lat: 35.860, Lng: 128.610},
		{Lat: 35.880, Lng: 128.590}, // swapped
	}
	if err := ValidatePolygon(poly); err != ErrPolygonSelfCross {
		t.Errorf("expected ErrPolygonSelfCross, got %v", err)
	}
}

func TestValidatePolygon_PointCount(t *testing.T) {
	if err := ValidatePolygon([]Point{{Lat: 35, Lng: 128}, {Lat: 35.01, Lng: 128}}); err != ErrPolygonPointCount {
		t.Errorf("2 points: expected ErrPolygonPointCount, got %v", err)
	}
	many := make([]Point, 21)
	for i := range many {
		many[i] = Point{Lat: 35 + float64(i)*0.0001, Lng: 128}
	}
	if err := ValidatePolygon(many); err != ErrPolygonPointCount {
		t.Errorf("21 points: expected ErrPolygonPointCount, got %v", err)
	}
}

func TestValidatePolygon_OutOfKorea(t *testing.T) {
	poly := square(10.0, 128.0, 0.01)
	if err := ValidatePolygon(poly); err != ErrPolygonOutOfKorea {
		t.Errorf("expected ErrPolygonOutOfKorea, got %v", err)
	}
}

func TestValidatePolygon_TooSmall(t *testing.T) {
	poly := square(35.87, 128.60, 0.0001) // tiny square, well under 1000 m²
	if err := ValidatePolygon(poly); err != ErrPolygonTooSmall {
		t.Errorf("expected ErrPolygonTooSmall, got %v", err)
	}
}

func TestPointInPolygon(t *testing.T) {
	poly := square(35.87, 128.60, 0.01)
	inside := Point{Lat: 35.87, Lng: 128.60}
	outside := Point{Lat: 35.90, Lng: 128.60}
	if !PointInPolygon(inside, poly) {
		t.Error("center should be inside")
	}
	if PointInPolygon(outside, poly) {
		t.Error("far point should be outside")
	}
}

func TestSignedDistancePolygon_Sign(t *testing.T) {
	poly := square(35.87, 128.60, 0.01)
	inside := Point{Lat: 35.87, Lng: 128.60}
	outside := Point{Lat: 35.95, Lng: 128.60}
	if sd := SignedDistancePolygon(poly, inside); sd >= 0 {
		t.Errorf("inside point sd = %f, want negative", sd)
	}
	if sd := SignedDistancePolygon(poly, outside); sd <= 0 {
		t.Errorf("outside point sd = %f, want positive", sd)
	}
}

func TestCentroid_OfSquareIsCenter(t *testing.T) {
	poly := square(35.87, 128.60, 0.01)
	c := Centroid(poly)
	if !almostEqual(c.Lat, 35.87, 1e-9) || !almostEqual(c.Lng, 128.60, 1e-9) {
		t.Errorf("centroid = %+v, want (35.87, 128.60)", c)
	}
}

func TestREqPolygon_Positive(t *testing.T) {
	poly := square(35.87, 128.60, 0.01)
	r := REqPolygon(poly)
	if r <= 0 {
		t.Errorf("REqPolygon = %f, want positive", r)
	}
}
