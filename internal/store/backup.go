package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxBackups is how many backup files are kept (SPEC §12.4: "14개 보관").
const MaxBackups = 14

// Backup runs SQLite's VACUUM INTO to produce a consistent point-in-time
// copy under dataDir/backup/, named with a Seoul-local timestamp so
// operators reading the filename don't need to convert time zones (SPEC
// §12.4, §7.1's Asia/Seoul display convention). It then prunes older
// backups beyond MaxBackups.
func (db *DB) Backup(ctx context.Context, dataDir string) (path string, err error) {
	backupDir := filepath.Join(dataDir, "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		loc = time.FixedZone("Asia/Seoul", 9*60*60)
	}
	stamp := time.Now().In(loc).Format("20060102-150405")
	fullPath := filepath.Join(backupDir, fmt.Sprintf("app-%s.db", stamp))
	// VACUUM INTO refuses to overwrite an existing file — guard against the
	// (only realistically test-triggered) case of two backups in the same
	// second by adding a disambiguating suffix.
	for i := 2; fileExists(fullPath); i++ {
		fullPath = filepath.Join(backupDir, fmt.Sprintf("app-%s-%d.db", stamp, i))
	}

	// SQLite string literals double single quotes; the path itself is
	// server-controlled (from DATA_DIR), not user input.
	escaped := strings.ReplaceAll(filepath.ToSlash(fullPath), "'", "''")
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`VACUUM INTO '%s'`, escaped)); err != nil {
		return "", fmt.Errorf("vacuum into: %w", err)
	}

	if err := pruneBackups(backupDir, MaxBackups); err != nil {
		return fullPath, fmt.Errorf("backup succeeded but pruning failed: %w", err)
	}
	return fullPath, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "app-") && strings.HasSuffix(e.Name(), ".db") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamp-named, so lexical == chronological
	if len(names) <= keep {
		return nil
	}
	for _, old := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, old)); err != nil {
			return err
		}
	}
	return nil
}
