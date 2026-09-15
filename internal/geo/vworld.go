// Package geo is a thin client for two VWorld services used by the admin
// area editor (docs/SPEC_AREA_EDITOR.md §4.2, §4.3): the Data API
// (administrative-dong boundaries, for "select a whole 동 at once") and the
// Address API / geocoder (road-name + basic-number -> coordinates, for
// rally points and checkpoints). The browser can't call VWorld directly
// (CSP is connect-src 'self'), so the server proxies both.
//
// baseURL is overridable via NewClient so tests can point the client at an
// httptest.Server instead of the real api.vworld.kr.
package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.vworld.kr"

var (
	ErrUpstream            = errors.New("geo: upstream request failed")
	ErrNotFound            = errors.New("geo: address not found")
	ErrNationalPointNumber = errors.New("geo: national point numbers (국가지점번호) are not supported yet — use a road name and basic number instead")
)

// NationalPointNumberPattern matches Korea's 국가지점번호 format (two Korean
// syllables followed by 8 digits, e.g. "다사 12345678"), which this package
// deliberately does not resolve (docs/SPEC_AREA_EDITOR.md §4.3, §10).
var NationalPointNumberPattern = regexp.MustCompile(`^[가-힣]{2}\s*\d{4}\s*\d{4}$`)

type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{httpClient: &http.Client{Timeout: 8 * time.Second}, baseURL: baseURL}
}

// --- Administrative-dong search (Data API) ---

// AdminDong is one administrative-dong search result, with each polygon's
// outer ring simplified and point-capped for a small enough response to
// draw on a map (docs/SPEC_AREA_EDITOR.md §4.2).
type AdminDong struct {
	Code  string
	Name  string
	Full  string
	Rings [][][2]float64 // each ring: [[lat,lng], ...]; holes are discarded
}

const (
	maxRingPoints        = 2000
	simplifyToleranceDeg = 0.00005 // roughly 5m
)

// SearchAdminDong looks up administrative-dong (읍/면/동) boundaries whose
// name contains q. domain should be the registered BASE_URL host.
func (c *Client) SearchAdminDong(ctx context.Context, key, domain, q string) ([]AdminDong, error) {
	filter := "emd_kor_nm:like:" + q
	reqURL := fmt.Sprintf(
		"%s/req/data?service=data&request=GetFeature&data=LT_C_ADEMD_INFO&key=%s&domain=%s&attrFilter=%s&geometry=true&crs=EPSG:4326&size=20&page=1&format=json",
		c.baseURL, url.QueryEscape(key), url.QueryEscape(domain), url.QueryEscape(filter))

	var raw vworldDataResponse
	if err := c.getJSON(ctx, reqURL, &raw); err != nil {
		return nil, err
	}
	if raw.Response.Status != "OK" {
		return nil, fmt.Errorf("%w: %s", ErrUpstream, raw.Response.Error.Text)
	}

	out := make([]AdminDong, 0, len(raw.Response.Result.FeatureCollection.Features))
	for _, f := range raw.Response.Result.FeatureCollection.Features {
		rings := extractOuterRings(f.Geometry)
		for i := range rings {
			rings[i] = simplifyRing(rings[i], simplifyToleranceDeg)
			if len(rings[i]) > maxRingPoints {
				rings[i] = decimate(rings[i], maxRingPoints)
			}
		}
		out = append(out, AdminDong{
			Code:  f.Properties["emd_cd"],
			Name:  f.Properties["emd_kor_nm"],
			Full:  f.Properties["full_nm"],
			Rings: rings,
		})
	}
	return out, nil
}

type vworldDataResponse struct {
	Response struct {
		Status string `json:"status"`
		Error  struct {
			Code string `json:"code"`
			Text string `json:"text"`
		} `json:"error"`
		Result struct {
			FeatureCollection struct {
				Features []vworldFeature `json:"features"`
			} `json:"featureCollection"`
		} `json:"result"`
	} `json:"response"`
}

type vworldFeature struct {
	Geometry   vworldGeometry    `json:"geometry"`
	Properties map[string]string `json:"properties"`
}

type vworldGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// extractOuterRings pulls out only the outer ring of each polygon (holes,
// if any, are ignored — irrelevant for "which cells fall inside this 동").
// GeoJSON coordinate order is [lng, lat]; the returned rings are [lat, lng]
// to match this codebase's Point convention everywhere else.
func extractOuterRings(geom vworldGeometry) [][][2]float64 {
	switch geom.Type {
	case "Polygon":
		var rings [][][2]float64 // rings of [lng, lat]
		if err := json.Unmarshal(geom.Coordinates, &rings); err != nil || len(rings) == 0 {
			return nil
		}
		return [][][2]float64{swapLngLat(rings[0])}
	case "MultiPolygon":
		var polys [][][][2]float64 // polygons, each a list of rings of [lng, lat]
		if err := json.Unmarshal(geom.Coordinates, &polys); err != nil {
			return nil
		}
		out := make([][][2]float64, 0, len(polys))
		for _, poly := range polys {
			if len(poly) == 0 {
				continue
			}
			out = append(out, swapLngLat(poly[0]))
		}
		return out
	default:
		return nil
	}
}

func swapLngLat(ring [][2]float64) [][2]float64 {
	out := make([][2]float64, len(ring))
	for i, p := range ring {
		out[i] = [2]float64{p[1], p[0]}
	}
	return out
}

// simplifyRing runs Douglas-Peucker simplification on a closed ring.
func simplifyRing(pts [][2]float64, tolerance float64) [][2]float64 {
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0] = true
	keep[len(pts)-1] = true
	douglasPeucker(pts, 0, len(pts)-1, tolerance, keep)
	out := make([][2]float64, 0, len(pts))
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

func douglasPeucker(pts [][2]float64, start, end int, tolerance float64, keep []bool) {
	if end <= start+1 {
		return
	}
	maxDist, maxIdx := -1.0, -1
	for i := start + 1; i < end; i++ {
		d := perpendicularDistance(pts[i], pts[start], pts[end])
		if d > maxDist {
			maxDist, maxIdx = d, i
		}
	}
	if maxDist > tolerance {
		keep[maxIdx] = true
		douglasPeucker(pts, start, maxIdx, tolerance, keep)
		douglasPeucker(pts, maxIdx, end, tolerance, keep)
	}
}

func perpendicularDistance(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := ((p[0]-a[0])*dx + (p[1]-a[1])*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}

// decimate is the last-resort cap when simplification alone doesn't get a
// ring under maxRingPoints (dense urban dong boundaries can still be large
// even after Douglas-Peucker) — evenly sampled, always keeping the last
// point so the ring stays closed.
func decimate(pts [][2]float64, max int) [][2]float64 {
	if len(pts) <= max || max <= 1 {
		return pts
	}
	step := float64(len(pts)) / float64(max)
	out := make([][2]float64, 0, max)
	for i := 0; i < max; i++ {
		idx := int(float64(i) * step)
		if idx >= len(pts) {
			idx = len(pts) - 1
		}
		out = append(out, pts[idx])
	}
	if out[len(out)-1] != pts[len(pts)-1] {
		out = append(out, pts[len(pts)-1])
	}
	return out
}

// --- Geocoding (Address API) ---

// GeocodeResult is a resolved road-name-and-basic-number address.
type GeocodeResult struct {
	Lat, Lng float64
	Matched  string // full matched address, for display/confirmation
	Type     string // "road" | "parcel"
}

// Geocode resolves q (expected to be a road name + basic number, e.g.
// "공평로 88") to coordinates, trying road-address matching first and
// falling back to parcel (지번) matching. Returns ErrNationalPointNumber if
// q looks like a 국가지점번호 instead (docs/SPEC_AREA_EDITOR.md §4.3).
func (c *Client) Geocode(ctx context.Context, key, q string) (*GeocodeResult, error) {
	if NationalPointNumberPattern.MatchString(strings.TrimSpace(q)) {
		return nil, ErrNationalPointNumber
	}
	for _, addrType := range []string{"road", "parcel"} {
		res, err := c.geocodeOnce(ctx, key, q, addrType)
		if err != nil {
			return nil, err
		}
		if res != nil {
			return res, nil
		}
	}
	return nil, ErrNotFound
}

// geocodeOnce returns (nil, nil) — not (nil, error) — when VWorld simply
// found no match for this address type, so Geocode can fall through to the
// next type without treating "not found yet" as a hard failure.
func (c *Client) geocodeOnce(ctx context.Context, key, q, addrType string) (*GeocodeResult, error) {
	reqURL := fmt.Sprintf(
		"%s/req/address?service=address&request=getcoord&version=2.0&crs=epsg:4326&address=%s&refine=true&simple=false&format=json&type=%s&key=%s",
		c.baseURL, url.QueryEscape(q), addrType, url.QueryEscape(key))

	var raw vworldGeocodeResponse
	if err := c.getJSON(ctx, reqURL, &raw); err != nil {
		return nil, err
	}
	if raw.Response.Status != "OK" {
		return nil, nil
	}
	lat, errLat := strconv.ParseFloat(raw.Response.Result.Point.Y, 64)
	lng, errLng := strconv.ParseFloat(raw.Response.Result.Point.X, 64)
	if errLat != nil || errLng != nil {
		return nil, fmt.Errorf("%w: non-numeric coordinates in response", ErrUpstream)
	}
	return &GeocodeResult{Lat: lat, Lng: lng, Matched: raw.Response.Refined.Text, Type: addrType}, nil
}

type vworldGeocodeResponse struct {
	Response struct {
		Status  string `json:"status"`
		Refined struct {
			Text string `json:"text"`
		} `json:"refined"`
		Result struct {
			Point struct {
				X string `json:"x"`
				Y string `json:"y"`
			} `json:"point"`
		} `json:"result"`
	} `json:"response"`
}

func (c *Client) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrUpstream, err)
	}
	return nil
}
