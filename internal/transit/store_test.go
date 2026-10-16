package transit

import (
	"context"
	"strings"
	"testing"
	"time"

	"emergencycallup/internal/store"
)

// Saved data loads back into a network that answers the same as the
// original; a failed re-sync leaves the previous data in place (T7).
func TestSaveLoadAndFailedResync(t *testing.T) {
	db, err := store.OpenMemory(strings.ReplaceAll(t.Name(), "/", "_"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if v, err := Version(ctx, db.DB); err != nil || v != 0 {
		t.Fatalf("empty Version = %d, %v; want 0", v, err)
	}

	n, pts := testNet(10)
	if err := SaveCity(ctx, db.DB, 22, n.Stops, n.Patterns, `{"routes":2}`, time.UnixMilli(1000)); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(ctx, db.DB)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := n.From(pts[0], q(8*60)).Seconds(pts[4])
	if got := loaded.From(pts[0], q(8*60)).Seconds(pts[4]); got != want {
		t.Errorf("loaded network: %d s, want %d", got, want)
	}

	bad := append([]Stop(nil), n.Stops...)
	bad[3].Kind = "tram" // violates the CHECK constraint mid-transaction
	if err := SaveCity(ctx, db.DB, 22, bad, n.Patterns, `{}`, time.UnixMilli(2000)); err == nil {
		t.Fatal("bad save succeeded")
	}
	if v, _ := Version(ctx, db.DB); v != 1000 {
		t.Errorf("Version after failed save = %d, want 1000", v)
	}
	again, err := Load(ctx, db.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Patterns) != 2 || len(again.Stops) != 4 { // stop 4 is on no route, so the save dropped it
		t.Errorf("after failed save: %d patterns, %d stops; want 2, 4", len(again.Patterns), len(again.Stops))
	}
}
