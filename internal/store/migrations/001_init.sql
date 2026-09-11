-- 001_init.sql — initial schema. See docs/SPEC.md §4.
-- All timestamp columns are UTC milliseconds (INTEGER). Never store string timestamps.
-- WAL / foreign_keys / busy_timeout / synchronous are set via the connection DSN,
-- not here (docs/SPEC.md §4: WAL cannot be changed inside a transaction, and
-- foreign_keys must be enabled per-connection).

CREATE TABLE team (
  no    INTEGER PRIMARY KEY CHECK (no BETWEEN 1 AND 99),
  name  TEXT NOT NULL
);

CREATE TABLE area (
  id        INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL CHECK (kind IN ('circle','polygon')),
  lat       REAL,
  lng       REAL,
  radius_m  INTEGER CHECK (radius_m BETWEEN 50 AND 5000),
  polygon   TEXT,
  nav_lat   REAL NOT NULL,
  nav_lng   REAL NOT NULL,
  bbox      TEXT NOT NULL,
  active    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX area_active_name ON area(name) WHERE active = 1;

CREATE TABLE member (
  id          INTEGER PRIMARY KEY,
  login_id    TEXT NOT NULL UNIQUE,
  pw_hash     TEXT NOT NULL,
  pw_must_change INTEGER NOT NULL DEFAULT 1,
  dept        TEXT NOT NULL,
  name        TEXT NOT NULL,
  office_tel  TEXT CHECK (office_tel IS NULL OR office_tel GLOB '[0-9][0-9][0-9][0-9]'),
  mobile      TEXT NOT NULL UNIQUE
              CHECK (mobile GLOB '01[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]'),
  team_no     INTEGER REFERENCES team(no),
  mission     TEXT,
  area_id     INTEGER REFERENCES area(id),
  note        TEXT,
  active      INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE preset (
  id     INTEGER PRIMARY KEY,
  kind   TEXT NOT NULL CHECK (kind IN ('incident_type','message','mission')),
  text   TEXT NOT NULL,
  sort   INTEGER NOT NULL DEFAULT 0,
  active INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE incident (
  id         INTEGER PRIMARY KEY,
  type_text  TEXT NOT NULL,
  message    TEXT NOT NULL,
  status     TEXT NOT NULL CHECK (status IN ('active','closed')),
  version    INTEGER NOT NULL DEFAULT 1,
  opened_by  TEXT NOT NULL,
  opened_at  INTEGER NOT NULL,
  closed_by  TEXT,
  closed_at  INTEGER
);
CREATE UNIQUE INDEX one_active_incident ON incident(status) WHERE status = 'active';

CREATE TABLE team_task (
  incident_id INTEGER NOT NULL REFERENCES incident(id),
  team_no     INTEGER NOT NULL REFERENCES team(no),
  mission     TEXT NOT NULL,
  area_id     INTEGER NOT NULL REFERENCES area(id),
  version     INTEGER NOT NULL DEFAULT 1,
  updated_by  TEXT NOT NULL,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (incident_id, team_no)
);

CREATE TABLE assignment (
  incident_id      INTEGER NOT NULL REFERENCES incident(id),
  member_id        INTEGER NOT NULL REFERENCES member(id),
  team_no          INTEGER NOT NULL,
  mission_override TEXT,
  area_override_id INTEGER REFERENCES area(id),
  eff_mission      TEXT NOT NULL,
  eff_area_id      INTEGER NOT NULL,
  mission_ver      INTEGER NOT NULL DEFAULT 1,
  ack_ver          INTEGER NOT NULL DEFAULT 1,
  state            TEXT NOT NULL CHECK (state IN
                     ('NOTIFIED','LOGGED_IN','LOC_DENIED','MOVING','ARRIVED','LEFT')),
  first_login_at   INTEGER,
  arrived_at       INTEGER,
  last_left_at     INTEGER,
  left_count       INTEGER NOT NULL DEFAULT 0,
  last_lat5        INTEGER,
  last_lng5        INTEGER,
  last_acc         INTEGER,
  last_fix_at      INTEGER,
  suspect          INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (incident_id, member_id)
);
CREATE INDEX assignment_team ON assignment(incident_id, team_no);

CREATE TABLE fix (
  incident_id INTEGER NOT NULL,
  member_id   INTEGER NOT NULL,
  ts          INTEGER NOT NULL,
  lat5        INTEGER NOT NULL,
  lng5        INTEGER NOT NULL,
  acc         INTEGER NOT NULL
);
CREATE INDEX fix_lookup ON fix(incident_id, member_id, ts);

CREATE TABLE event (
  id          INTEGER PRIMARY KEY,
  ts          INTEGER NOT NULL,
  incident_id INTEGER,
  actor       TEXT NOT NULL,
  type        TEXT NOT NULL,
  member_id   INTEGER,
  data        TEXT
);
CREATE INDEX event_incident ON event(incident_id, ts);

CREATE TABLE admin_user (
  id         INTEGER PRIMARY KEY,
  login_id   TEXT NOT NULL UNIQUE,
  pw_hash    TEXT NOT NULL,
  name       TEXT NOT NULL,
  dept       TEXT,
  role       TEXT NOT NULL CHECK (role IN ('admin','operator')),
  active     INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);

CREATE TABLE session (
  token_hash   TEXT PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN ('admin','member')),
  subject_id   INTEGER NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);

CREATE TABLE sms_log (
  id           INTEGER PRIMARY KEY,
  batch_id     TEXT NOT NULL,
  incident_id  INTEGER NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('open','close','resend')),
  member_id    INTEGER NOT NULL,
  provider     TEXT NOT NULL,
  status       TEXT NOT NULL CHECK (status IN ('prepared','sent','failed')),
  provider_ref TEXT,
  ts           INTEGER NOT NULL
);

-- Seed default teams 1..10 (SPEC §4: "기본 1~10조 시딩, 이름만 수정 가능").
INSERT INTO team (no, name) VALUES
  (1,'1조'),(2,'2조'),(3,'3조'),(4,'4조'),(5,'5조'),
  (6,'6조'),(7,'7조'),(8,'8조'),(9,'9조'),(10,'10조');
