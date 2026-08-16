package appconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	AppName             = "AntiGravityNodeGuard"
	DefaultSourceGroup  = "Legacy Subscription"
	DefaultManagedGroup = "AntiGravity-Agent"
)

type Config struct {
	SourceGroup               string   `yaml:"source_group"`
	PreferredSubscriptionUID  string   `yaml:"preferred_subscription_uid,omitempty"`
	AllowSubscriptionFallback bool     `yaml:"allow_subscription_fallback"`
	ManagedGroup              string   `yaml:"managed_group"`
	ProcessPath               string   `yaml:"antigravity_process_path"`
	MihomoSocket              string   `yaml:"mihomo_socket"`
	ExternalController        string   `yaml:"external_controller,omitempty"`
	ExternalSecret            string   `yaml:"external_secret,omitempty"`
	MixedPort                 int      `yaml:"mixed_port"`
	GreenThresholdMS          int      `yaml:"green_threshold_ms"`
	ProbeTimeout              Duration `yaml:"probe_timeout"`
	ProbeInterval             Duration `yaml:"probe_interval"`
	HealthInterval            Duration `yaml:"health_interval"`
	SubscriptionScanInterval  Duration `yaml:"subscription_scan_interval"`
	GeoCooldown               Duration `yaml:"geo_cooldown"`
	NetworkCooldown           Duration `yaml:"network_cooldown"`
	SwitchWindow              Duration `yaml:"switch_window"`
	MaxSwitches               int      `yaml:"max_switches_per_window"`
	ReconnectDelay            Duration `yaml:"reconnect_delay"`
	ValidationWindow          Duration `yaml:"validation_window"`
	LatencyURL                string   `yaml:"latency_url"`
	EgressURL                 string   `yaml:"egress_url"`
	GooglePreflightURL        string   `yaml:"google_preflight_url"`
	AllowedRegions            []string `yaml:"allowed_regions"`
	RegionPriority            []string `yaml:"region_priority"`
	AutoResume                bool     `yaml:"auto_resume"`
	ObserveOnly               bool     `yaml:"observe_only"`
}

type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

func Default() Config {
	return Config{
		SourceGroup:               DefaultSourceGroup,
		AllowSubscriptionFallback: true,
		ManagedGroup:              DefaultManagedGroup,
		ProcessPath:               "/Applications/Antigravity.app/Contents/Resources/bin/language_server",
		MihomoSocket:              "/tmp/verge/verge-mihomo.sock",
		MixedPort:                 7897,
		GreenThresholdMS:          250,
		ProbeTimeout:              Duration(5 * time.Second),
		ProbeInterval:             Duration(5 * time.Minute),
		HealthInterval:            Duration(5 * time.Second),
		SubscriptionScanInterval:  Duration(10 * time.Second),
		GeoCooldown:               Duration(24 * time.Hour),
		NetworkCooldown:           Duration(15 * time.Minute),
		SwitchWindow:              Duration(5 * time.Minute),
		MaxSwitches:               8,
		ReconnectDelay:            Duration(500 * time.Millisecond),
		ValidationWindow:          Duration(90 * time.Second),
		LatencyURL:                "https://www.gstatic.com/generate_204",
		EgressURL:                 "https://www.cloudflare.com/cdn-cgi/trace",
		GooglePreflightURL:        "https://daily-cloudcode-pa.googleapis.com",
		AllowedRegions:            []string{"日本", "新加坡", "台湾", "美国", "JP", "SG", "TW", "US"},
		RegionPriority:            []string{"日本", "新加坡", "台湾", "美国"},
		AutoResume:                true,
		ObserveOnly:               false,
	}
}

// MigrateRecoveryDefaults upgrades only values shipped by releases before
// fast recovery. Explicitly customized values are preserved.
func MigrateRecoveryDefaults(path string, cfg Config) (Config, error) {
	changed := false
	if cfg.HealthInterval.Value() == 15*time.Second {
		cfg.HealthInterval, changed = Duration(5*time.Second), true
	}
	if cfg.SwitchWindow.Value() == 15*time.Minute && cfg.MaxSwitches == 3 {
		cfg.SwitchWindow, cfg.MaxSwitches, changed = Duration(5*time.Minute), 8, true
	}
	if cfg.ReconnectDelay.Value() == 2*time.Second {
		cfg.ReconnectDelay, changed = Duration(500*time.Millisecond), true
	}
	if !changed {
		return cfg, nil
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return Config{}, err
	}
	if err := atomicWrite(path, data, 0o600); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.SourceGroup == "" || c.ManagedGroup == "" {
		return errors.New("source_group and managed_group are required")
	}
	if c.SourceGroup == c.ManagedGroup {
		return errors.New("source_group and managed_group must differ")
	}
	if c.GreenThresholdMS <= 0 || c.ProbeTimeout.Value() <= 0 || c.ProbeInterval.Value() <= 0 || c.HealthInterval.Value() <= 0 || c.SubscriptionScanInterval.Value() <= 0 || c.GeoCooldown.Value() <= 0 || c.NetworkCooldown.Value() <= 0 || c.SwitchWindow.Value() <= 0 || c.ReconnectDelay.Value() <= 0 || c.ValidationWindow.Value() <= 0 {
		return errors.New("health thresholds and intervals must be positive")
	}
	if c.MixedPort < 1 || c.MixedPort > 65535 || c.MaxSwitches < 1 {
		return errors.New("mixed_port or max_switches_per_window is invalid")
	}
	if c.ExternalController != "" && c.ExternalSecret == "" {
		return errors.New("external_secret is required when external_controller is set")
	}
	return nil
}

type Paths struct {
	Home          string
	SupportDir    string
	BinDir        string
	ConfigFile    string
	StateFile     string
	PauseFile     string
	LockFile      string
	ControlLock   string
	RuntimeConfig string
	ManifestFile  string
	InventoryFile string
	LogDir        string
	LogFile       string
	LaunchAgent   string
	ClashDir      string
	ProfilesFile  string
	ClashConfig   string
	AGLog         string
}

func ResolvePaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	support := filepath.Join(home, "Library", "Application Support", AppName)
	logDir := filepath.Join(home, "Library", "Logs", AppName)
	clashDir := filepath.Join(home, "Library", "Application Support", "io.github.clash-verge-rev.clash-verge-rev")
	return Paths{
		Home:          home,
		SupportDir:    support,
		BinDir:        filepath.Join(support, "bin"),
		ConfigFile:    filepath.Join(support, "config.yaml"),
		StateFile:     filepath.Join(support, "state.json"),
		PauseFile:     filepath.Join(support, "paused"),
		LockFile:      filepath.Join(support, "daemon.lock"),
		ControlLock:   filepath.Join(support, "control.lock"),
		RuntimeConfig: filepath.Join(clashDir, "ag-nodeguard-runtime.yaml"),
		ManifestFile:  filepath.Join(support, "managed-paths.json"),
		InventoryFile: filepath.Join(support, "subscriptions.json"),
		LogDir:        logDir,
		LogFile:       filepath.Join(logDir, "nodeguard.log"),
		LaunchAgent:   filepath.Join(home, "Library", "LaunchAgents", "com.local.ag-nodeguard.plist"),
		ClashDir:      clashDir,
		ProfilesFile:  filepath.Join(clashDir, "profiles.yaml"),
		ClashConfig:   filepath.Join(clashDir, "clash-verge.yaml"),
		AGLog:         filepath.Join(home, "Library", "Logs", "Antigravity", "language_server.log"),
	}, nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func SaveDefault(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := yaml.Marshal(Default())
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
