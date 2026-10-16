-- 004_transit.sql — docs/SPEC_TRANSIT.md §4.2.
-- Bus/metro route data loaded ahead of time by `server transit sync`.
-- Stops are shared between cities (a Daegu route can pass Gyeongsan
-- stops), so a city re-sync replaces its patterns and then drops only the
-- stops no pattern uses any more.

CREATE TABLE transit_stop (
  id    TEXT PRIMARY KEY,
  city  INTEGER NOT NULL,
  name  TEXT NOT NULL,
  lat   REAL NOT NULL,
  lng   REAL NOT NULL,
  kind  TEXT NOT NULL CHECK (kind IN ('bus','metro'))
);

CREATE TABLE transit_pattern (
  id         INTEGER PRIMARY KEY,
  city       INTEGER NOT NULL,
  route_id   TEXT NOT NULL,
  route_no   TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('bus','metro')),
  first_min  INTEGER,
  last_min   INTEGER,
  hw_weekday INTEGER,
  hw_sat     INTEGER,
  hw_sun     INTEGER
);
CREATE INDEX transit_pattern_city ON transit_pattern(city);

CREATE TABLE transit_pattern_stop (
  pattern_id INTEGER NOT NULL REFERENCES transit_pattern(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  stop_id    TEXT NOT NULL REFERENCES transit_stop(id),
  run_sec    INTEGER,
  PRIMARY KEY (pattern_id, seq)
);
CREATE INDEX transit_pattern_stop_stop ON transit_pattern_stop(stop_id);

CREATE TABLE transit_sync (
  city      INTEGER PRIMARY KEY,
  synced_at INTEGER NOT NULL,
  summary   TEXT NOT NULL
);
