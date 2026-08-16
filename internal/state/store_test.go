package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTripAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := New()
	s.RecordDelay("jp", 100)
	s.RecordDelay("jp", 140)
	s.Switches = []time.Time{time.Now().Add(-time.Hour), time.Now()}
	s.FailureFingerprints["old"] = time.Now().Add(-2 * time.Hour)
	s.ExitCooldowns[ExitKey("jp", "192.0.2.1")] = Cooldown{Until: time.Now().Add(-time.Minute)}
	s.Prune(time.Now(), 15*time.Minute)
	if len(s.Switches) != 1 || len(s.FailureFingerprints) != 0 || len(s.ExitCooldowns) != 0 {
		t.Fatalf("prune failed: %#v", s)
	}
	if Jitter(s.Node("jp").RecentDelays) != 40 {
		t.Fatal("unexpected jitter")
	}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Node("jp").LastDelayMS != 140 {
		t.Fatalf("last delay = %d", loaded.Node("jp").LastDelayMS)
	}
}
