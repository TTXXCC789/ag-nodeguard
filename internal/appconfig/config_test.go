package appconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAddsSubscriptionDefaultsToLegacyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	legacy := `source_group: Legacy Subscription
managed_group: AntiGravity-Agent
antigravity_process_path: /Applications/Antigravity.app/Contents/Resources/bin/language_server
mihomo_socket: /tmp/test.sock
mixed_port: 7897
green_threshold_ms: 250
probe_timeout: 5s
probe_interval: 5m
health_interval: 5s
geo_cooldown: 24h
network_cooldown: 15m
switch_window: 5m
max_switches_per_window: 8
reconnect_delay: 500ms
validation_window: 90s
latency_url: https://example.invalid
egress_url: https://example.invalid
google_preflight_url: https://example.invalid
allowed_regions: [日本]
region_priority: [日本]
auto_resume: true
observe_only: false
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowSubscriptionFallback || cfg.SubscriptionScanInterval.Value() != 10*time.Second {
		t.Fatalf("legacy subscription defaults not applied: %#v", cfg)
	}
	cfg.PreferredSubscriptionUID = "uid-1"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.PreferredSubscriptionUID != "uid-1" {
		t.Fatalf("preferred subscription was not saved: %#v %v", loaded, err)
	}
}

func TestMigrateRecoveryDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.HealthInterval = Duration(15 * time.Second)
	cfg.SwitchWindow = Duration(15 * time.Minute)
	cfg.MaxSwitches = 3
	cfg.ReconnectDelay = Duration(2 * time.Second)
	migrated, err := MigrateRecoveryDefaults(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.HealthInterval.Value() != 5*time.Second || migrated.SwitchWindow.Value() != 5*time.Minute || migrated.MaxSwitches != 8 || migrated.ReconnectDelay.Value() != 500*time.Millisecond {
		t.Fatalf("unexpected migrated config: %#v", migrated)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MaxSwitches != 8 {
		t.Fatal("migration was not persisted")
	}
}

func TestMigrateRecoveryDefaultsPreservesCustomLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.SwitchWindow = Duration(20 * time.Minute)
	cfg.MaxSwitches = 4
	migrated, err := MigrateRecoveryDefaults(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.SwitchWindow.Value() != 20*time.Minute || migrated.MaxSwitches != 4 {
		t.Fatal("custom switch limits were overwritten")
	}
}
