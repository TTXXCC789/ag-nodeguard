package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type NodeStats struct {
	Successes      int       `json:"successes"`
	Failures       int       `json:"failures"`
	LastDelayMS    int       `json:"last_delay_ms,omitempty"`
	RecentDelays   []int     `json:"recent_delays,omitempty"`
	ExitIP         string    `json:"exit_ip,omitempty"`
	ExitRegion     string    `json:"exit_region,omitempty"`
	CooldownUntil  time.Time `json:"cooldown_until,omitempty"`
	CooldownReason string    `json:"cooldown_reason,omitempty"`
	LastSuccess    time.Time `json:"last_success,omitempty"`
	LastFailure    time.Time `json:"last_failure,omitempty"`
}

type PendingValidation struct {
	Episode         string    `json:"episode"`
	Node            string    `json:"node"`
	Started         time.Time `json:"started"`
	ObservedRunning bool      `json:"observed_running,omitempty"`
}

type Cooldown struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

type State struct {
	CurrentNode          string                `json:"current_node,omitempty"`
	CurrentDelayMS       int                   `json:"current_delay_ms,omitempty"`
	AntigravityOK        bool                  `json:"antigravity_ok"`
	LocalHealthOK        bool                  `json:"local_health_ok"`
	LastHealthCheck      time.Time             `json:"last_health_check,omitempty"`
	LastProbe            time.Time             `json:"last_probe,omitempty"`
	LastSwitch           time.Time             `json:"last_switch,omitempty"`
	NextRecovery         time.Time             `json:"next_recovery,omitempty"`
	LastFailureKind      string                `json:"last_failure_kind,omitempty"`
	LastError            string                `json:"last_error,omitempty"`
	LastAgentSignal      string                `json:"last_agent_signal,omitempty"`
	LastAgentSignalAt    time.Time             `json:"last_agent_signal_at,omitempty"`
	LastSubscriptionScan time.Time             `json:"last_subscription_scan,omitempty"`
	ExitIP               string                `json:"exit_ip,omitempty"`
	ExitRegion           string                `json:"exit_region,omitempty"`
	Nodes                map[string]*NodeStats `json:"nodes"`
	Switches             []time.Time           `json:"switches,omitempty"`
	ResumedEpisodes      map[string]time.Time  `json:"resumed_episodes"`
	Pending              *PendingValidation    `json:"pending_validation,omitempty"`
	FailureFingerprints  map[string]time.Time  `json:"failure_fingerprints,omitempty"`
	ExitCooldowns        map[string]Cooldown   `json:"exit_cooldowns,omitempty"`
}

func New() State {
	return State{Nodes: map[string]*NodeStats{}, ResumedEpisodes: map[string]time.Time{}, FailureFingerprints: map[string]time.Time{}, ExitCooldowns: map[string]Cooldown{}}
}

func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return State{}, err
	}
	s := New()
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("parse state: %w", err)
	}
	if s.Nodes == nil {
		s.Nodes = map[string]*NodeStats{}
	}
	if s.ResumedEpisodes == nil {
		s.ResumedEpisodes = map[string]time.Time{}
	}
	if s.FailureFingerprints == nil {
		s.FailureFingerprints = map[string]time.Time{}
	}
	if s.ExitCooldowns == nil {
		s.ExitCooldowns = map[string]Cooldown{}
	}
	return s, nil
}

func Save(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
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

func (s *State) Node(name string) *NodeStats {
	n := s.Nodes[name]
	if n == nil {
		n = &NodeStats{}
		s.Nodes[name] = n
	}
	return n
}

func (s *State) RecordDelay(name string, delay int) {
	n := s.Node(name)
	n.LastDelayMS = delay
	n.RecentDelays = append(n.RecentDelays, delay)
	if len(n.RecentDelays) > 10 {
		n.RecentDelays = n.RecentDelays[len(n.RecentDelays)-10:]
	}
}

func (s *State) Prune(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	kept := s.Switches[:0]
	for _, t := range s.Switches {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.Switches = kept
	for key, at := range s.ResumedEpisodes {
		if at.Before(now.Add(-7 * 24 * time.Hour)) {
			delete(s.ResumedEpisodes, key)
		}
	}
	for fingerprint, at := range s.FailureFingerprints {
		if at.Before(now.Add(-time.Hour)) {
			delete(s.FailureFingerprints, fingerprint)
		}
	}
	for key, cooldown := range s.ExitCooldowns {
		if !cooldown.Until.After(now) {
			delete(s.ExitCooldowns, key)
		}
	}
}

func ExitKey(node, ip string) string { return node + "\x00" + ip }

func Jitter(values []int) int {
	if len(values) < 2 {
		return 0
	}
	copyValues := append([]int(nil), values...)
	sort.Ints(copyValues)
	return copyValues[len(copyValues)-1] - copyValues[0]
}
