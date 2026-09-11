// Command sim is the load/regression simulator required by docs/SPEC.md
// §3.3 and Phase 4's R16: several hundred virtual field-personnel clients
// driving a real, already-running server over HTTP, following the exact
// client transmission policy from SPEC §6.1 (MIN_GAP/MOVE_TRIGGER/heartbeat)
// so the server sees realistic traffic, not a synthetic firehose.
//
// It talks only to /api/v1 like any other client — it does not touch the
// database directly.
//
// Usage (against a server already running with SIM_ADMIN_ID/PW as a valid
// admin account, e.g. one created via `server admin create`):
//
//	SIM_BASE_URL=http://localhost:8080 SIM_ADMIN_ID=admin SIM_ADMIN_PW=... \
//	SIM_MEMBERS=500 SIM_DURATION=1h go run ./cmd/sim
//
// SIM_DURATION defaults to 5m for a quick smoke run; SPEC's Phase-4
// acceptance test (R16) asks for a full 1h run before sign-off.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

var (
	baseURL  = getenv("SIM_BASE_URL", "http://localhost:8080")
	adminID  = getenv("SIM_ADMIN_ID", "")
	adminPW  = getenv("SIM_ADMIN_PW", "")
	nMembers = getenvInt("SIM_MEMBERS", 500)
	nTeams   = getenvInt("SIM_TEAMS", 10)
)

// centerLat/centerLng anchors the whole simulated area cluster (Daegu-ish,
// matching the SPEC's own examples).
const centerLat, centerLng = 35.8714, 128.6014
const areaRadiusM = 150

func main() {
	durationStr := getenv("SIM_DURATION", "5m")
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		log.Fatalf("invalid SIM_DURATION %q: %v", durationStr, err)
	}
	if adminID == "" || adminPW == "" {
		log.Fatal("SIM_ADMIN_ID and SIM_ADMIN_PW are required (create one with `server admin create`)")
	}

	fmt.Printf("sim: %d members, %d teams, target %s, base=%s\n", nMembers, nTeams, duration, baseURL)

	admin := newClient()
	if err := admin.post("/a/login", map[string]any{"id": adminID, "pw": adminPW}, nil); err != nil {
		log.Fatalf("admin login: %v", err)
	}

	areaIDs := setupAreas(admin, nTeams)
	members := setupMembers(admin, nMembers, nTeams, areaIDs)
	openIncident(admin, nTeams, areaIDs)

	metrics := newMetrics()
	stop := time.Now().Add(duration)

	var wg sync.WaitGroup
	for i, m := range members {
		wg.Add(1)
		go func(idx int, mm *simMember) {
			defer wg.Done()
			runMember(mm, areaIDs[mm.teamNo], stop, metrics)
		}(i, m)
	}

	// Two mid-run mission changes (SPEC Phase 4 acceptance: "중간 임무 변경 2회").
	go func() {
		time.Sleep(duration / 3)
		changeTeamTask(admin, 1, areaIDs)
		time.Sleep(duration / 3)
		changeTeamTask(admin, 2, areaIDs)
	}()

	wg.Wait()
	metrics.report(duration)
}

// ---------------------------------------------------------------- HTTP client

type client struct {
	http *http.Client
}

func newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{http: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
}

func (c *client) do(method, path string, body any, out any) (int, time.Duration, error) {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, baseURL+"/api/v1"+path, reader)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return 0, elapsed, err
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, elapsed, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.StatusCode, elapsed, nil
}

func (c *client) post(path string, body any, out any) error {
	_, _, err := c.do(http.MethodPost, path, body, out)
	return err
}

// ---------------------------------------------------------------- Setup

func setupAreas(admin *client, nTeams int) map[int64]int64 {
	ids := make(map[int64]int64, nTeams)
	for t := 1; t <= nTeams; t++ {
		lat := centerLat + float64(t)*0.01
		var res struct {
			ID int64 `json:"id"`
		}
		err := admin.post("/a/areas", map[string]any{
			"name": fmt.Sprintf("sim-area-%d-%d", t, time.Now().UnixNano()),
			"kind": "circle", "lat": lat, "lng": centerLng, "r": areaRadiusM,
		}, &res)
		if err != nil {
			log.Fatalf("create area for team %d: %v", t, err)
		}
		ids[int64(t)] = res.ID
	}
	return ids
}

type simMember struct {
	id      int64
	loginID string
	pw      string
	teamNo  int64
	client  *client
}

func setupMembers(admin *client, n, nTeams int, areaIDs map[int64]int64) []*simMember {
	members := make([]*simMember, 0, n)
	suffix := time.Now().UnixNano() % 1_000_000
	for i := 0; i < n; i++ {
		teamNo := int64(i%nTeams + 1)
		mobile := fmt.Sprintf("010%08d", (suffix+int64(i))%100000000)
		loginID := fmt.Sprintf("sim%d_%d", suffix, i)
		var res struct {
			ID              int64  `json:"id"`
			InitialPassword string `json:"initialPassword"`
		}
		err := admin.post("/a/members", map[string]any{
			"loginId": loginID, "dept": "sim", "name": fmt.Sprintf("sim-%d", i),
			"mobile": mobile, "teamNo": teamNo,
		}, &res)
		if err != nil {
			log.Fatalf("create member %d: %v", i, err)
		}
		members = append(members, &simMember{id: res.ID, loginID: loginID, pw: res.InitialPassword, teamNo: teamNo, client: newClient()})
	}
	return members
}

func openIncident(admin *client, nTeams int, areaIDs map[int64]int64) {
	teams := make([]map[string]any, 0, nTeams)
	for t := 1; t <= nTeams; t++ {
		teams = append(teams, map[string]any{"no": int64(t), "mission": "sim-mission", "areaId": areaIDs[int64(t)]})
	}
	if err := admin.post("/a/incidents", map[string]any{
		"type": "sim-drill", "message": "load simulation", "teams": teams, "members": []any{},
	}, nil); err != nil {
		log.Fatalf("open incident: %v", err)
	}
}

func changeTeamTask(admin *client, teamNo int64, areaIDs map[int64]int64) {
	_ = admin.post(fmt.Sprintf("/a/incidents/current/teams/%d", teamNo), map[string]any{
		"mission": "sim-mission-changed", "areaId": areaIDs[teamNo],
	}, nil)
}

// ---------------------------------------------------------------- Member behavior

// runMember drives one simulated device through: login -> consent -> walk
// toward the area -> settle at the boundary -> a brief excursion outside
// (이탈) -> return (복귀) -> loiter until stop, all gated by the exact
// SPEC §6.1 client send policy (min gap / move trigger / heartbeat).
func runMember(m *simMember, areaID int64, stop time.Time, metrics *metrics) {
	if err := m.client.post("/f/login", map[string]any{"id": m.loginID, "pw": m.pw}, nil); err != nil {
		metrics.recordError()
		return
	}
	_ = m.client.post("/f/consent", map[string]any{"granted": true, "textVer": "sim"}, nil)

	// Random starting point ~1-2km from this team's area center.
	angle := rand.Float64() * 2 * math.Pi
	dist := 1000 + rand.Float64()*1000
	teamAreaLat := centerLat + float64(m.teamNo)*0.01
	lat := teamAreaLat + (dist*math.Cos(angle))/111320.0
	lng := centerLng + (dist*math.Sin(angle))/(111320.0*math.Cos(teamAreaLat*math.Pi/180))

	phase := "approach"
	phaseStart := time.Now()
	n := 15 // server-directed interval, seconds
	lastSent := time.Time{}
	var lastSentLat, lastSentLng float64
	var lastSentAcc int
	hasSent := false

	targetLat, targetLng := teamAreaLat, centerLng

	for time.Now().Before(stop) {
		now := time.Now()
		switch phase {
		case "approach":
			lat, lng = stepToward(lat, lng, targetLat, targetLng, 8) // ~8 m/s brisk walk/vehicle
			if haversine(lat, lng, targetLat, targetLng) < 20 {
				phase, phaseStart = "settled", now
			}
		case "settled":
			lat, lng = targetLat, targetLng
			if now.Sub(phaseStart) > 60*time.Second && rand.Float64() < 0.005 {
				phase, phaseStart = "excursion", now
			}
		case "excursion":
			lat, lng = stepToward(lat, lng, targetLat+0.003, targetLng, 8)
			if now.Sub(phaseStart) > 40*time.Second {
				phase, phaseStart = "return", now
			}
		case "return":
			lat, lng = stepToward(lat, lng, targetLat, targetLng, 8)
			if haversine(lat, lng, targetLat, targetLng) < 20 {
				phase, phaseStart = "settled", now
			}
		}

		acc := 8 + rand.Intn(15)
		shouldSend := !hasSent
		if hasSent {
			d := haversine(lat, lng, lastSentLat, lastSentLng)
			if d >= 25 {
				shouldSend = true
			}
			if now.Sub(lastSent) >= time.Duration(n)*time.Second {
				shouldSend = true
			}
			if lastSentAcc > 100 && acc <= 100 {
				shouldSend = true
			}
		}
		if shouldSend && (lastSent.IsZero() || now.Sub(lastSent) >= 5*time.Second) {
			var res struct {
				N int `json:"n"`
			}
			status, elapsed, err := m.client.do(http.MethodPost, "/f/fix", map[string]any{
				"la": lat, "lo": lng, "ac": acc, "ts": now.UnixMilli(),
			}, &res)
			metrics.recordFix(elapsed, status, err)
			if err == nil && res.N > 0 {
				n = res.N
			}
			lastSent, lastSentLat, lastSentLng, lastSentAcc, hasSent = now, lat, lng, acc, true
		}
		time.Sleep(time.Duration(1+rand.Intn(2)) * time.Second)
	}
}

func stepToward(lat, lng, tLat, tLng, speedMS float64) (float64, float64) {
	d := haversine(lat, lng, tLat, tLng)
	if d < 1 {
		return tLat, tLng
	}
	frac := math.Min(1, speedMS*1.5/d) // ~1.5s of travel per loop tick
	return lat + (tLat-lat)*frac, lng + (tLng-lng)*frac
}

func haversine(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371000.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// ---------------------------------------------------------------- Metrics

type metrics struct {
	mu         sync.Mutex
	latencies  []time.Duration
	errors     int64
	fixCount   int64
	statusHist map[int]int64
}

func newMetrics() *metrics {
	return &metrics{statusHist: map[int]int64{}}
}

func (m *metrics) recordFix(d time.Duration, status int, err error) {
	atomic.AddInt64(&m.fixCount, 1)
	m.mu.Lock()
	m.latencies = append(m.latencies, d)
	m.statusHist[status]++
	m.mu.Unlock()
	if err != nil {
		atomic.AddInt64(&m.errors, 1)
	}
}

func (m *metrics) recordError() { atomic.AddInt64(&m.errors, 1) }

func (m *metrics) report(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sort.Slice(m.latencies, func(i, j int) bool { return m.latencies[i] < m.latencies[j] })
	n := len(m.latencies)
	pct := func(p float64) time.Duration {
		if n == 0 {
			return 0
		}
		idx := int(p * float64(n))
		if idx >= n {
			idx = n - 1
		}
		return m.latencies[idx]
	}
	fmt.Printf("\n=== sim report (%s) ===\n", duration)
	fmt.Printf("fixes sent: %d, errors: %d\n", m.fixCount, m.errors)
	fmt.Printf("latency p50=%v p95=%v p99=%v\n", pct(0.50), pct(0.95), pct(0.99))
	fmt.Printf("status codes: %v\n", m.statusHist)
	fmt.Println("NOTE: this checks throughput/latency/error-rate (SPEC §13.1) end-to-end")
	fmt.Println("over real HTTP. It does NOT independently re-verify arrival/departure")
	fmt.Println("judgement correctness against ground truth (SPEC R16's \"판정 오류 0\") —")
	fmt.Println("cross-check GET /a/incidents/{id}/report.csv against the phase timeline")
	fmt.Println("this run printed above (approach/settled/excursion/return) by hand,")
	fmt.Println("or extend this simulator to assert it before relying on R16 sign-off.")
}
