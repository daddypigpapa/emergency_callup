package store

import (
	"database/sql"
	"testing"
)

// R23: migration 003 (area table rebuild for kind='grid') must preserve
// pre-existing data and references through the DROP/rename, and leave
// foreign_keys consistent. Builds a DB with only 001+002 applied, inserts
// a circle area and a member referencing it (as if this were a real
// deployment upgrading), then runs the full migrate() (which applies 003)
// and checks everything survived.
func TestMigration003_PreservesExistingAreaAndReferences(t *testing.T) {
	dsn := "file:migration003test?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sqlDB}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, name := range []string{"001_init.sql", "002_settings.sql"} {
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		version, err := versionOf(name)
		if err != nil {
			t.Fatalf("versionOf %s: %v", name, err)
		}
		if err := db.applyMigration(name, version, sqlBytes, false); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	// Pre-003 state: a circle area and a member referencing it, using the
	// old area schema (no grid_size/cells columns yet).
	if _, err := db.Exec(`INSERT INTO area(id, name, kind, lat, lng, radius_m, nav_lat, nav_lng, bbox, active, created_at)
		VALUES (1, '옛지역', 'circle', 35.8714, 128.6014, 150, 35.8714, 128.6014, '[35.87,128.60,35.874,128.603]', 1, 1000)`); err != nil {
		t.Fatalf("insert pre-existing area: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO member(id, login_id, pw_hash, dept, name, mobile, team_no, area_id, active, created_at, updated_at)
		VALUES (1, 'm1', 'x', '부서', '이름', '01011112222', 1, 1, 1, 1000, 1000)`); err != nil {
		t.Fatalf("insert pre-existing member referencing area: %v", err)
	}

	// Now run the full migrator: it should see 001/002 already applied and
	// apply 003 (the area rebuild) on top of this real data.
	if err := db.migrate(); err != nil {
		t.Fatalf("migrate (applying 003): %v", err)
	}

	var name, kind string
	if err := db.QueryRow(`SELECT name, kind FROM area WHERE id = 1`).Scan(&name, &kind); err != nil {
		t.Fatalf("area row missing after rebuild: %v", err)
	}
	if name != "옛지역" || kind != "circle" {
		t.Errorf("area after rebuild = (%q, %q), want (옛지역, circle)", name, kind)
	}

	var memberAreaID int64
	if err := db.QueryRow(`SELECT area_id FROM member WHERE id = 1`).Scan(&memberAreaID); err != nil {
		t.Fatalf("member row missing after rebuild: %v", err)
	}
	if memberAreaID != 1 {
		t.Errorf("member.area_id after rebuild = %d, want 1 (reference must survive)", memberAreaID)
	}

	// foreign_keys must be back on and clean.
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("pragma foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d after migrate, want 1 (re-enabled)", fk)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if rows.Next() {
		t.Error("foreign_key_check reports a violation after migration 003")
	}
	rows.Close()

	// New kind='grid' rows must now be insertable.
	if _, err := db.Exec(`INSERT INTO area(id, name, kind, grid_size, cells, nav_lat, nav_lng, bbox, active, created_at)
		VALUES (2, '새격자지역', 'grid', 250, '[[0,0]]', 35.87, 128.60, '[35.87,128.60,35.874,128.603]', 1, 2000)`); err != nil {
		t.Errorf("inserting a grid-kind area should succeed after 003: %v", err)
	}
}

// R27-adjacent sanity check, but really about migration idempotency:
// re-running migrate() on an already-fully-migrated DB must be a no-op
// (no duplicate schema_migrations rows, no re-applying 003 against a
// table that's already been rebuilt).
func TestMigration003_MigrateIsIdempotent(t *testing.T) {
	db, err := OpenMemory("migration003-idempotent")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := db.migrate(); err != nil {
		t.Fatalf("second migrate call: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = 3`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_migrations row count for version 3 = %d, want 1", count)
	}
}
