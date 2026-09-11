// Package area implements mission-area geometry: validation, bbox
// computation, and the signed-distance-to-boundary math used by the
// tracker's arrival/departure judgement (SPEC §4, §6.2).
package area

import (
	"errors"
	"fmt"
	"math"
)

// Point is a WGS84 coordinate (degrees).
type Point struct {
	Lat float64
	Lng float64
}

const earthRadiusM = 6371000.0

// Haversine returns the great-circle distance between two points, in meters.
func Haversine(a, b Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dLat := (b.Lat - a.Lat) * math.Pi / 180
	dLng := (b.Lng - a.Lng) * math.Pi / 180
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
	return earthRadiusM * c
}

// Korea's bounding lat/lng range (SPEC §6.2: "좌표가 대한민국 범위(위도 33~39,
// 경도 124~132) 안").
const (
	KoreaLatMin = 33.0
	KoreaLatMax = 39.0
	KoreaLngMin = 124.0
	KoreaLngMax = 132.0
)

// InKorea reports whether p falls within the configured Korea bounding box.
func InKorea(p Point) bool {
	return p.Lat >= KoreaLatMin && p.Lat <= KoreaLatMax &&
		p.Lng >= KoreaLngMin && p.Lng <= KoreaLngMax
}

// projectMeters converts a point to a local equirectangular projection (in
// meters) around a reference latitude, per SPEC §6.2: "중심 위도 기준
// 등장방형 투영(m) 후 점–변 최단거리". Only relative distances within the
// small area are meaningful; this is not a general-purpose projection.
func projectMeters(refLat float64, p Point) (x, y float64) {
	latRad := refLat * math.Pi / 180
	x = p.Lng * math.Pi / 180 * earthRadiusM * math.Cos(latRad)
	y = p.Lat * math.Pi / 180 * earthRadiusM
	return
}

// --- Circle ---

const (
	CircleRadiusMin = 50
	CircleRadiusMax = 5000
)

// ValidateCircle checks radius bounds and that the center is within Korea.
func ValidateCircle(center Point, radiusM int) error {
	if !InKorea(center) {
		return errors.New("area: center is outside Korea's bounding box")
	}
	if radiusM < CircleRadiusMin || radiusM > CircleRadiusMax {
		return fmt.Errorf("area: radius must be between %d and %d meters", CircleRadiusMin, CircleRadiusMax)
	}
	return nil
}

// CircleBBox returns [south, west, north, east] for a circle, using a flat
// degrees-per-meter approximation adequate for areas of a few km (SPEC
// §6.2 accepts this approximation).
func CircleBBox(center Point, radiusM int) [4]float64 {
	dLat := float64(radiusM) / 111320.0
	latRad := center.Lat * math.Pi / 180
	cosLat := math.Cos(latRad)
	if cosLat < 0.01 {
		cosLat = 0.01
	}
	dLng := float64(radiusM) / (111320.0 * cosLat)
	return [4]float64{center.Lat - dLat, center.Lng - dLng, center.Lat + dLat, center.Lng + dLng}
}

// SignedDistanceCircle returns the signed distance from p to the circle
// boundary: negative when p is inside (SPEC §6.2).
func SignedDistanceCircle(center Point, radiusM int, p Point) float64 {
	return Haversine(p, center) - float64(radiusM)
}

// --- Polygon ---

const (
	PolygonMinPoints = 3
	PolygonMaxPoints = 20
	PolygonMinAreaM2 = 1000.0
)

var (
	ErrPolygonPointCount = fmt.Errorf("area: polygon must have between %d and %d points", PolygonMinPoints, PolygonMaxPoints)
	ErrPolygonOutOfKorea = errors.New("area: a polygon vertex is outside Korea's bounding box")
	ErrPolygonSelfCross  = errors.New("area: polygon edges must not cross (self-intersecting)")
	ErrPolygonTooSmall   = fmt.Errorf("area: polygon area must be at least %.0f square meters", PolygonMinAreaM2)
)

// ValidatePolygon checks point count, bounds, self-intersection and minimum
// area (SPEC §6.2).
func ValidatePolygon(poly []Point) error {
	if len(poly) < PolygonMinPoints || len(poly) > PolygonMaxPoints {
		return ErrPolygonPointCount
	}
	for _, p := range poly {
		if !InKorea(p) {
			return ErrPolygonOutOfKorea
		}
	}
	if selfIntersects(poly) {
		return ErrPolygonSelfCross
	}
	if math.Abs(PolygonAreaM2(poly)) < PolygonMinAreaM2 {
		return ErrPolygonTooSmall
	}
	return nil
}

// PolygonAreaM2 returns the (signed) area of a polygon in square meters via
// the shoelace formula on the equirectangular projection around the
// polygon's own mean latitude.
func PolygonAreaM2(poly []Point) float64 {
	refLat := meanLat(poly)
	n := len(poly)
	var sum float64
	for i := 0; i < n; i++ {
		x1, y1 := projectMeters(refLat, poly[i])
		x2, y2 := projectMeters(refLat, poly[(i+1)%n])
		sum += x1*y2 - x2*y1
	}
	return sum / 2
}

func meanLat(poly []Point) float64 {
	var sum float64
	for _, p := range poly {
		sum += p.Lat
	}
	return sum / float64(len(poly))
}

// Centroid returns the arithmetic mean of the polygon's vertices (SPEC §6.2:
// "다각형은 꼭짓점 평균"). This is the vertex centroid, not the
// area-weighted centroid — matching the spec's literal wording.
func Centroid(poly []Point) Point {
	var sumLat, sumLng float64
	for _, p := range poly {
		sumLat += p.Lat
		sumLng += p.Lng
	}
	n := float64(len(poly))
	return Point{Lat: sumLat / n, Lng: sumLng / n}
}

// PointInPolygon reports whether p is inside poly using ray casting. Korea's
// small angular extent makes the lat/lng-plane approximation adequate (SPEC
// §6.2: "오차 무시 가능").
func PointInPolygon(p Point, poly []Point) bool {
	inside := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		pi, pj := poly[i], poly[j]
		if (pi.Lng > p.Lng) != (pj.Lng > p.Lng) {
			latAtP := pi.Lat + (p.Lng-pi.Lng)/(pj.Lng-pi.Lng)*(pj.Lat-pi.Lat)
			if p.Lat < latAtP {
				inside = !inside
			}
		}
	}
	return inside
}

// PolygonBBox returns [south, west, north, east] for the polygon vertices.
func PolygonBBox(poly []Point) [4]float64 {
	south, north := poly[0].Lat, poly[0].Lat
	west, east := poly[0].Lng, poly[0].Lng
	for _, p := range poly[1:] {
		south = math.Min(south, p.Lat)
		north = math.Max(north, p.Lat)
		west = math.Min(west, p.Lng)
		east = math.Max(east, p.Lng)
	}
	return [4]float64{south, west, north, east}
}

// SignedDistancePolygon returns the signed distance from p to the polygon
// boundary (negative when p is inside), via point-to-nearest-edge distance
// in the local equirectangular projection (SPEC §6.2).
func SignedDistancePolygon(poly []Point, p Point) float64 {
	refLat := meanLat(poly)
	px, py := projectMeters(refLat, p)
	n := len(poly)
	minDist := math.Inf(1)
	for i := 0; i < n; i++ {
		ax, ay := projectMeters(refLat, poly[i])
		bx, by := projectMeters(refLat, poly[(i+1)%n])
		d := distPointToSegment(px, py, ax, ay, bx, by)
		if d < minDist {
			minDist = d
		}
	}
	if PointInPolygon(p, poly) {
		return -minDist
	}
	return minDist
}

func distPointToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	lenSq := dx*dx + dy*dy
	if lenSq == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / lenSq
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	cx, cy := ax+t*dx, ay+t*dy
	return math.Hypot(px-cx, py-cy)
}

// selfIntersects reports whether any two non-adjacent edges of poly cross
// (SPEC §6.2, R9: "나비넥타이 순서" / bowtie ordering must be rejected).
func selfIntersects(poly []Point) bool {
	n := len(poly)
	if n < 4 {
		return false // a triangle can never self-intersect
	}
	for i := 0; i < n; i++ {
		a1, a2 := poly[i], poly[(i+1)%n]
		for j := i + 1; j < n; j++ {
			// Skip edges that share a vertex with edge i (adjacent edges,
			// including the wrap-around pair).
			if j == i || j == (i+1)%n || (j+1)%n == i {
				continue
			}
			b1, b2 := poly[j], poly[(j+1)%n]
			if segmentsIntersect(a1, a2, b1, b2) {
				return true
			}
		}
	}
	return false
}

func orientation(p, q, r Point) int {
	val := (q.Lng-p.Lng)*(r.Lat-q.Lat) - (q.Lat-p.Lat)*(r.Lng-q.Lng)
	const eps = 1e-12
	if val > eps {
		return 1
	}
	if val < -eps {
		return 2
	}
	return 0
}

func onSegment(p, q, r Point) bool {
	return q.Lat <= math.Max(p.Lat, r.Lat) && q.Lat >= math.Min(p.Lat, r.Lat) &&
		q.Lng <= math.Max(p.Lng, r.Lng) && q.Lng >= math.Min(p.Lng, r.Lng)
}

// segmentsIntersect reports whether segment p1p2 crosses segment p3p4
// (standard orientation-based test, including collinear-overlap cases).
func segmentsIntersect(p1, p2, p3, p4 Point) bool {
	o1 := orientation(p1, p2, p3)
	o2 := orientation(p1, p2, p4)
	o3 := orientation(p3, p4, p1)
	o4 := orientation(p3, p4, p2)

	if o1 != o2 && o3 != o4 {
		return true
	}
	if o1 == 0 && onSegment(p1, p3, p2) {
		return true
	}
	if o2 == 0 && onSegment(p1, p4, p2) {
		return true
	}
	if o3 == 0 && onSegment(p3, p1, p4) {
		return true
	}
	if o4 == 0 && onSegment(p3, p2, p4) {
		return true
	}
	return false
}

// REqPolygon returns the "equivalent radius" used by SPEC §6.1's transmit
// interval table: distance from the bbox center to the farthest vertex.
func REqPolygon(poly []Point) float64 {
	bbox := PolygonBBox(poly)
	center := Point{Lat: (bbox[0] + bbox[2]) / 2, Lng: (bbox[1] + bbox[3]) / 2}
	var maxD float64
	for _, p := range poly {
		d := Haversine(center, p)
		if d > maxD {
			maxD = d
		}
	}
	return maxD
}
