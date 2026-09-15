package store

import (
	"testing"
)

// R13: restart must preserve data (open the same data dir twice).
func TestOpen_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := db1.Exec(`INSERT INTO team(no, name) VALUES (50, 'x50조')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	var name string
	if err := db2.QueryRow(`SELECT name FROM team WHERE no = 50`).Scan(&name); err != nil {
		t.Fatalf("row missing after reopen: %v", err)
	}
	if name != "x50조" {
		t.Errorf("name = %q, want x50조", name)
	}

	// Default teams 1..10 must be seeded exactly once (migration not re-run).
	var count int
	if err := db2.QueryRow(`SELECT COUNT(1) FROM team WHERE no BETWEEN 1 AND 10`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 10 {
		t.Errorf("seeded team count = %d, want 10", count)
	}
}

func TestOpen_SchemaHasCoreTables(t *testing.T) {
	db, err := OpenMemory("schema-check")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	tables := []string{"team", "area", "member", "preset", "incident", "team_task",
		"assignment", "fix", "event", "admin_user", "session", "sms_log",
		"team_plan", "checkpoint"}
	for _, tbl := range tables {
		var n int
		err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n)
		if err != nil {
			t.Fatalf("query for %s: %v", tbl, err)
		}
		if n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
}

func TestOpen_ForeignKeysEnabled(t *testing.T) {
	db, err := OpenMemory("fk-check")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}
