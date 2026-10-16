package transit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SaveCity replaces one city's patterns in a single transaction
// (SPEC_TRANSIT §4.1): on any error the previous data stays. pats[i].Stops
// index into stops. Stops are upserted; afterwards stops that no pattern
// uses any more are dropped.
func SaveCity(ctx context.Context, db *sql.DB, city int, stops []Stop, pats []Pattern, summary string, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM transit_pattern WHERE city = ?`, city); err != nil {
		return err
	}
	for _, s := range stops {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO transit_stop(id, city, name, lat, lng, kind) VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET name=excluded.name, lat=excluded.lat, lng=excluded.lng, kind=excluded.kind`,
			s.ID, city, s.Name, s.P.Lat, s.P.Lng, s.Kind); err != nil {
			return fmt.Errorf("stop %s: %w", s.ID, err)
		}
	}
	for _, p := range pats {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO transit_pattern(city, route_id, route_no, kind, first_min, last_min, hw_weekday, hw_sat, hw_sun)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			city, p.RouteID, p.RouteNo, p.Kind, nullIfNeg(p.FirstMin), nullIfNeg(p.LastMin),
			nullIfZero(p.Headway[Weekday]), nullIfZero(p.Headway[Saturday]), nullIfZero(p.Headway[Sunday]))
		if err != nil {
			return fmt.Errorf("route %s: %w", p.RouteNo, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for i, s := range p.Stops {
			var run any
			if p.RunSec != nil && p.RunSec[i] > 0 {
				run = p.RunSec[i]
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO transit_pattern_stop(pattern_id, seq, stop_id, run_sec) VALUES (?, ?, ?, ?)`,
				id, i, stops[s].ID, run); err != nil {
				return fmt.Errorf("route %s stop %d: %w", p.RouteNo, i, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM transit_stop WHERE id NOT IN (SELECT stop_id FROM transit_pattern_stop)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transit_sync(city, synced_at, summary) VALUES (?, ?, ?)
		 ON CONFLICT(city) DO UPDATE SET synced_at=excluded.synced_at, summary=excluded.summary`,
		city, now.UnixMilli(), summary); err != nil {
		return err
	}
	return tx.Commit()
}

// Version is the latest sync time over all cities (0 = no data yet). A
// changed value means the in-memory Network should be rebuilt.
func Version(ctx context.Context, db *sql.DB) (int64, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MAX(synced_at) FROM transit_sync`).Scan(&v)
	return v.Int64, err
}

// Load builds the Network from every stored city.
func Load(ctx context.Context, db *sql.DB) (*Network, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, lat, lng, kind FROM transit_stop ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var stops []Stop
	idx := map[string]int{}
	for rows.Next() {
		var s Stop
		if err := rows.Scan(&s.ID, &s.Name, &s.P.Lat, &s.P.Lng, &s.Kind); err != nil {
			rows.Close()
			return nil, err
		}
		idx[s.ID] = len(stops)
		stops = append(stops, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.QueryContext(ctx,
		`SELECT p.id, p.route_id, p.route_no, p.kind, COALESCE(p.first_min,-1), COALESCE(p.last_min,-1),
		        COALESCE(p.hw_weekday,0), COALESCE(p.hw_sat,0), COALESCE(p.hw_sun,0),
		        s.stop_id, COALESCE(s.run_sec,0)
		 FROM transit_pattern p JOIN transit_pattern_stop s ON s.pattern_id = p.id
		 ORDER BY p.id, s.seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pats []Pattern
	lastID := int64(-1)
	for rows.Next() {
		var id int64
		var p Pattern
		var stopID string
		var run int
		if err := rows.Scan(&id, &p.RouteID, &p.RouteNo, &p.Kind, &p.FirstMin, &p.LastMin,
			&p.Headway[Weekday], &p.Headway[Saturday], &p.Headway[Sunday], &stopID, &run); err != nil {
			return nil, err
		}
		if id != lastID {
			p.RunSec = []int{}
			pats = append(pats, p)
			lastID = id
		}
		cur := &pats[len(pats)-1]
		cur.Stops = append(cur.Stops, idx[stopID])
		cur.RunSec = append(cur.RunSec, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return NewNetwork(stops, pats), nil
}

func nullIfNeg(v int) any {
	if v < 0 {
		return nil
	}
	return v
}

func nullIfZero(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
