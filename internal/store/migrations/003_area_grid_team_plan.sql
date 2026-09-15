-- +fk_off
-- 003_area_grid_team_plan.sql — docs/SPEC_AREA_EDITOR.md §3.2.
-- Adds 'grid' as an area kind (grid_size + cells columns), plus team_plan
-- and checkpoint tables for pre-registering per-team rally points and
-- checkpoints, and team_task columns to snapshot them at incident open.
--
-- area.kind's CHECK constraint can't be widened with ALTER, so the table
-- is rebuilt. The leading "-- +fk_off" marker (see internal/store/store.go)
-- tells the migration runner to disable foreign_keys for this file and
-- verify with PRAGMA foreign_key_check before committing.

CREATE TABLE area_new (
  id        INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL CHECK (kind IN ('circle','polygon','grid')),
  lat       REAL,
  lng       REAL,
  radius_m  INTEGER CHECK (radius_m BETWEEN 50 AND 5000),
  polygon   TEXT,
  grid_size INTEGER CHECK (grid_size IN (100,250,500,1000)),
  cells     TEXT,
  nav_lat   REAL NOT NULL,
  nav_lng   REAL NOT NULL,
  bbox      TEXT NOT NULL,
  active    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);

INSERT INTO area_new (id, name, kind, lat, lng, radius_m, polygon, nav_lat, nav_lng, bbox, active, created_at)
  SELECT id, name, kind, lat, lng, radius_m, polygon, nav_lat, nav_lng, bbox, active, created_at FROM area;

DROP TABLE area;
ALTER TABLE area_new RENAME TO area;
CREATE UNIQUE INDEX area_active_name ON area(name) WHERE active = 1;

CREATE TABLE team_plan (
  team_no    INTEGER PRIMARY KEY REFERENCES team(no),
  mission    TEXT NOT NULL DEFAULT '',
  area_id    INTEGER REFERENCES area(id),
  rally_lat  REAL,
  rally_lng  REAL,
  rally_addr TEXT,
  updated_by TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE checkpoint (
  id        INTEGER PRIMARY KEY,
  team_no   INTEGER NOT NULL REFERENCES team(no),
  seq       INTEGER NOT NULL,
  name      TEXT NOT NULL,
  lat       REAL NOT NULL,
  lng       REAL NOT NULL,
  addr      TEXT,
  radius_m  INTEGER NOT NULL DEFAULT 50 CHECK (radius_m BETWEEN 20 AND 500),
  UNIQUE (team_no, seq)
);

ALTER TABLE team_task ADD COLUMN rally_lat REAL;
ALTER TABLE team_task ADD COLUMN rally_lng REAL;
ALTER TABLE team_task ADD COLUMN checkpoints TEXT;
