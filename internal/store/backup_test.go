package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackup_CreatesFileAndPrunes(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for i := 0; i < MaxBackups+3; i++ {
		path, err := db.Backup(context.Background(), dir)
		if err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("backup file missing: %v", statErr)
		}
		// Force a distinct filename each iteration (names are minute-grained).
		time.Sleep(1100 * time.Millisecond)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "backup"))
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	if len(entries) > MaxBackups {
		t.Errorf("backup count = %d, want <= %d", len(entries), MaxBackups)
	}
}
