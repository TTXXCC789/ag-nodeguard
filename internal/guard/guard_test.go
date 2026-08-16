package guard

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ag-nodeguard/internal/antigravity"
	"ag-nodeguard/internal/appconfig"
	"ag-nodeguard/internal/model"
	"ag-nodeguard/internal/state"
)

func TestEligibleName(t *testing.T) {
	regions := []string{"日本", "新加坡", "美国", "JP", "SG", "US"}
	for _, name := range []string{"日本07aws", "SG-01", "美国 Hysteria2"} {
		if !eligibleName(name, regions) {
			t.Fatalf("%q should be eligible", name)
		}
	}
	for _, name := range []string{"DIRECT", "日本 剩余流量 20GB", "香港01", "官网入口"} {
		if eligibleName(name, regions) {
			t.Fatalf("%q should be excluded", name)
		}
	}
}

func TestCandidateScorePrefersRealSuccessThenLatency(t *testing.T) {
	now := time.Now()
	known := &state.NodeStats{Successes: 2, LastDelayMS: 180, RecentDelays: []int{170, 180}}
	unknown := &state.NodeStats{RecentDelays: []int{50, 55}}
	if candidateScore(known, 180, now) >= candidateScore(unknown, 55, now) {
		t.Fatal("known Agent success should outweigh lower unverified latency")
	}
	cooled := &state.NodeStats{Successes: 10, CooldownUntil: now.Add(time.Hour)}
	if candidateScore(cooled, 20, now) < 1_000_000_000 {
		t.Fatal("cooldown must dominate score")
	}
}

func TestRegionPenaltyFollowsConfiguredPriority(t *testing.T) {
	priority := []string{"日本", "新加坡", "美国"}
	if regionPenalty("日本01", priority) >= regionPenalty("新加坡01", priority) {
		t.Fatal("Japan should precede Singapore")
	}
}

func TestValidationRequiresObservedRunningTransition(t *testing.T) {
	idle := antigravity.Conversation{Status: "CASCADE_RUN_STATUS_IDLE"}
	if !validationSucceeded(idle, true) {
		t.Fatal("an observed running-to-idle transition should validate")
	}
	if validationSucceeded(idle, false) {
		t.Fatal("ambiguous idle status must not validate")
	}
}

func TestNextCandidateAttemptUsesEarliestCooldown(t *testing.T) {
	now := time.Now()
	candidates := []model.Candidate{
		{Name: "日本01", CooldownUntil: now.Add(12 * time.Minute)},
		{Name: "日本02", CooldownUntil: now.Add(3 * time.Minute)},
	}
	got := nextCandidateAttempt(candidates, now, 5*time.Minute)
	if got.Sub(now) != 3*time.Minute {
		t.Fatalf("next attempt = %s, want 3m", got.Sub(now))
	}
}

func TestGeoFailurePrefersAnotherRegion(t *testing.T) {
	now := time.Now()
	candidates := []model.Candidate{
		{RuntimeName: "日本03aws", Name: "日本03aws", DelayMS: 80, Preferred: true},
		{RuntimeName: "新加坡07aws", Name: "新加坡07aws", DelayMS: 120, Preferred: true},
		{RuntimeName: "美国01", Name: "美国01", DelayMS: 150, Preferred: true},
	}
	selected, ok := selectFailureCandidate(candidates, "日本12aws", model.FailureGeo, now, true)
	if !ok || selected.Name != "新加坡07aws" {
		t.Fatalf("selected %#v, want first non-Japan candidate", selected)
	}
	selected, ok = selectFailureCandidate(candidates, "日本12aws", model.FailureConnection, now, true)
	if !ok || selected.Name != "日本03aws" {
		t.Fatalf("network failure selected %#v, want best scored candidate", selected)
	}
}

func TestFailureSelectionUsesPreferredBeforeLowerLatencyFallback(t *testing.T) {
	now := time.Now()
	candidates := []model.Candidate{
		{RuntimeName: "fallback-fast", Name: "日本01", DelayMS: 20, Fallback: true},
		{RuntimeName: "preferred-slow", Name: "日本02", DelayMS: 220, Preferred: true},
	}
	selected, ok := selectFailureCandidate(candidates, "current", model.FailureConnection, now, true)
	if !ok || selected.RuntimeName != "preferred-slow" {
		t.Fatalf("selected %#v, want preferred subscription", selected)
	}
}

func TestFailureSelectionFallsBackOnlyWhenPreferredExhausted(t *testing.T) {
	now := time.Now()
	candidates := []model.Candidate{
		{RuntimeName: "preferred-cooled", Name: "日本01", Preferred: true, CooldownUntil: now.Add(time.Hour)},
		{RuntimeName: "fallback-ready", Name: "新加坡01", Fallback: true},
	}
	selected, ok := selectFailureCandidate(candidates, "current", model.FailureConnection, now, true)
	if !ok || selected.RuntimeName != "fallback-ready" {
		t.Fatalf("selected %#v, want fallback", selected)
	}
	if _, ok := selectFailureCandidate(candidates, "current", model.FailureConnection, now, false); ok {
		t.Fatal("fallback was selected while disabled")
	}
}

func TestRecoveryPreservesBackendFailureKind(t *testing.T) {
	for _, kind := range []model.FailureKind{model.FailureGeo, model.FailureDNS, model.FailureTimeout, model.FailureUnavailable, model.FailureConnection} {
		if got := recoveryFailureKind(string(kind)); got != kind {
			t.Fatalf("recoveryFailureKind(%q) = %q", kind, got)
		}
	}
	if got := recoveryFailureKind("quota"); got != model.FailureConnection {
		t.Fatalf("unknown failure kind = %q, want connection fallback", got)
	}
}

func TestBackendSuccessClearsDeferredRecovery(t *testing.T) {
	dir := t.TempDir()
	paths := appconfig.Paths{StateFile: filepath.Join(dir, "state.json")}
	s := state.New()
	s.CurrentNode = "新加坡07aws"
	s.AntigravityOK = false
	s.LastFailureKind = string(model.FailureGeo)
	s.NextRecovery = time.Now().Add(time.Minute)
	s.Pending = &state.PendingValidation{Episode: "episode", Node: s.CurrentNode, Started: time.Now()}
	if err := state.Save(paths.StateFile, s); err != nil {
		t.Fatal(err)
	}
	engine := New(appconfig.Default(), paths, log.New(io.Discard, "", 0))
	at := time.Now()
	if err := engine.HandleAgentSuccess(model.SuccessEvent{OccurredAt: at}); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.Load(paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AntigravityOK || loaded.LastFailureKind != "" || !loaded.NextRecovery.IsZero() || loaded.Pending != nil {
		t.Fatalf("success did not clear failure state: %#v", loaded)
	}
	if loaded.Node("新加坡07aws").Successes != 1 {
		t.Fatal("successful node was not recorded")
	}
}

func TestRateLimitSchedulesDeferredRecovery(t *testing.T) {
	dir := t.TempDir()
	paths := appconfig.Paths{StateFile: filepath.Join(dir, "state.json"), PauseFile: filepath.Join(dir, "paused")}
	cfg := appconfig.Default()
	cfg.MaxSwitches = 3
	cfg.SwitchWindow = appconfig.Duration(15 * time.Minute)
	now := time.Now()
	s := state.New()
	s.CurrentNode = "日本01"
	s.Switches = []time.Time{now.Add(-2 * time.Minute), now.Add(-time.Minute), now}
	if err := state.Save(paths.StateFile, s); err != nil {
		t.Fatal(err)
	}
	engine := New(cfg, paths, log.New(io.Discard, "", 0))
	event := model.FailureEvent{Kind: model.FailureConnection, Fingerprint: "rate-limited", OccurredAt: now}
	if err := engine.HandleFailure(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.Load(paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	want := s.Switches[0].Add(cfg.SwitchWindow.Value())
	if loaded.NextRecovery.Before(want.Add(-time.Second)) || loaded.NextRecovery.After(want.Add(time.Second)) {
		t.Fatalf("next recovery = %s, want %s", loaded.NextRecovery, want)
	}
}

func TestGeoFailureSwitchesOnceToGreenCandidate(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agng-guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "mihomo.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	selected := "日本01"
	selectCalls := 0
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"proxies": map[string]any{
			"AntiGravity-Agent": map[string]any{"type": "Selector", "now": selected, "all": []string{"日本01", "新加坡01"}},
			"日本01":              map[string]any{"type": "Hysteria2"}, "新加坡01": map[string]any{"type": "VLESS"},
		}})
	})
	mux.HandleFunc("/group/AntiGravity-Agent/delay", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"日本01":90,"新加坡01":120}`))
	})
	mux.HandleFunc("/proxies/AntiGravity-Agent", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		selected, selectCalls = body["name"], selectCalls+1
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/connections", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"connections":[]}`)) })
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	cfg := appconfig.Default()
	cfg.MihomoSocket = socket
	cfg.AutoResume = false
	cfg.ReconnectDelay = appconfig.Duration(time.Millisecond)
	cfg.MixedPort = 1
	paths := appconfig.Paths{StateFile: filepath.Join(dir, "state.json"), PauseFile: filepath.Join(dir, "paused")}
	s := state.New()
	s.CurrentNode = "日本01"
	if err := state.Save(paths.StateFile, s); err != nil {
		t.Fatal(err)
	}
	engine := New(cfg, paths, log.New(io.Discard, "", 0))
	event := model.FailureEvent{Kind: model.FailureGeo, Fingerprint: "episode-1", OccurredAt: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.HandleFailure(ctx, event); err != nil {
		t.Fatal(err)
	}
	loaded, _ := state.Load(paths.StateFile)
	if loaded.CurrentNode != "新加坡01" || selectCalls != 1 {
		t.Fatalf("node=%q selectCalls=%d", loaded.CurrentNode, selectCalls)
	}
	if !loaded.Node("日本01").CooldownUntil.After(time.Now().Add(23 * time.Hour)) {
		t.Fatal("geo failure did not apply 24h cooldown")
	}
	if err := engine.HandleFailure(ctx, event); err != nil {
		t.Fatal(err)
	}
	if selectCalls != 1 {
		t.Fatalf("duplicate event caused another switch: %d", selectCalls)
	}
}
