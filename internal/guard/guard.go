package guard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ag-nodeguard/internal/antigravity"
	"ag-nodeguard/internal/appconfig"
	"ag-nodeguard/internal/installer"
	"ag-nodeguard/internal/logwatch"
	"ag-nodeguard/internal/mihomo"
	"ag-nodeguard/internal/model"
	"ag-nodeguard/internal/oplock"
	"ag-nodeguard/internal/state"
	"ag-nodeguard/internal/subscriptions"
)

type Engine struct {
	Config appconfig.Config
	Paths  appconfig.Paths
	Log    *log.Logger
}

type Status struct {
	ObservedAt                time.Time            `json:"observed_at"`
	Installed                 bool                 `json:"installed"`
	Paused                    bool                 `json:"paused"`
	DaemonRunning             bool                 `json:"daemon_running"`
	MihomoVersion             string               `json:"mihomo_version,omitempty"`
	CurrentNode               string               `json:"current_node,omitempty"`
	CurrentDelayMS            int                  `json:"current_delay_ms,omitempty"`
	ExitIP                    string               `json:"exit_ip,omitempty"`
	ExitRegion                string               `json:"exit_region,omitempty"`
	AntigravityOK             bool                 `json:"antigravity_ok"`
	LocalHealthOK             bool                 `json:"local_health_ok"`
	LastHealthCheck           time.Time            `json:"last_health_check,omitempty"`
	LastProbe                 time.Time            `json:"last_probe,omitempty"`
	LastSwitch                time.Time            `json:"last_switch,omitempty"`
	NextRecovery              time.Time            `json:"next_recovery,omitempty"`
	SwitchesInWindow          int                  `json:"switches_in_window"`
	LastFailureKind           string               `json:"last_failure_kind,omitempty"`
	LastError                 string               `json:"last_error,omitempty"`
	LastAgentSignal           string               `json:"last_agent_signal,omitempty"`
	LastAgentSignalAt         time.Time            `json:"last_agent_signal_at,omitempty"`
	MaxSwitches               int                  `json:"max_switches_per_window"`
	PreferredSubscriptionUID  string               `json:"preferred_subscription_uid,omitempty"`
	PreferredSubscriptionName string               `json:"preferred_subscription_name,omitempty"`
	CurrentSubscriptionUID    string               `json:"current_subscription_uid,omitempty"`
	CurrentSubscriptionName   string               `json:"current_subscription_name,omitempty"`
	UsingFallback             bool                 `json:"using_fallback"`
	SubscriptionScanAt        time.Time            `json:"subscription_scan_at,omitempty"`
	Cooldowns                 map[string]time.Time `json:"cooldowns,omitempty"`
	State                     state.State          `json:"-"`
}

type DoctorCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Details string `json:"details"`
}

type SubscriptionSelectionResult struct {
	Subscription subscriptions.Subscription `json:"subscription"`
	Candidate    model.Candidate            `json:"candidate"`
	FallbackUsed bool                       `json:"fallback_used"`
}

func New(cfg appconfig.Config, paths appconfig.Paths, logger *log.Logger) *Engine {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Engine{Config: cfg, Paths: paths, Log: logger}
}

func (e *Engine) reloadConfig() {
	if cfg, err := appconfig.Load(e.Paths.ConfigFile); err == nil {
		e.Config = cfg
	}
}

func (e *Engine) acquireOperation(ctx context.Context) (func(), error) {
	path := e.Paths.ControlLock
	if path == "" {
		path = filepath.Join(filepath.Dir(e.Paths.StateFile), "control.lock")
	}
	lock, err := oplock.Acquire(ctx, path)
	if err != nil {
		return nil, err
	}
	return func() { _ = lock.Close() }, nil
}

func (e *Engine) Candidates(ctx context.Context) ([]model.Candidate, error) {
	e.reloadConfig()
	client, err := mihomo.New(e.Config)
	if err != nil {
		return nil, err
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	groupName := e.Config.ManagedGroup
	group, ok := proxies[groupName]
	if !ok || len(group.All) == 0 {
		return nil, fmt.Errorf("proxy group %q is unavailable", groupName)
	}
	delays, err := client.GroupDelay(ctx, groupName, e.Config.LatencyURL, e.Config.ProbeTimeout.Value())
	if err != nil {
		return nil, err
	}
	s, err := state.Load(e.Paths.StateFile)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	catalog, _ := subscriptions.Scan(e.Paths, e.Config)
	result := e.buildCandidates(proxies, group, delays, &s, now, true, catalog)
	s.LastProbe = now
	_ = state.Save(e.Paths.StateFile, s)
	sortCandidates(result)
	return result, nil
}

func (e *Engine) Subscriptions(ctx context.Context, refresh bool) ([]subscriptions.Subscription, error) {
	e.reloadConfig()
	if refresh {
		release, err := e.acquireOperation(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		client, err := mihomo.New(e.Config)
		if err != nil {
			return nil, err
		}
		if _, err := installer.Reconcile(ctx, e.Config, e.Paths, client, true); err != nil {
			return nil, err
		}
		e.reloadConfig()
	}
	catalog, err := subscriptions.Scan(e.Paths, e.Config)
	if err != nil {
		return nil, err
	}
	s, _ := state.Load(e.Paths.StateFile)
	items := append([]subscriptions.Subscription(nil), catalog.Items...)
	for index := range items {
		items[index].Selected = items[index].UID == e.Config.PreferredSubscriptionUID
		for _, node := range items[index].Nodes {
			delay := s.Node(node.RuntimeName).LastDelayMS
			if node.Eligible && delay > 0 && delay < e.Config.GreenThresholdMS {
				items[index].GreenNodeCount++
			}
		}
	}
	return items, nil
}

func (e *Engine) SelectSubscription(ctx context.Context, uid string) (SubscriptionSelectionResult, error) {
	e.reloadConfig()
	release, err := e.acquireOperation(ctx)
	if err != nil {
		return SubscriptionSelectionResult{}, err
	}
	defer release()
	catalog, err := subscriptions.Scan(e.Paths, e.Config)
	if err != nil {
		return SubscriptionSelectionResult{}, err
	}
	target, ok := catalog.Subscription(uid)
	if !ok {
		return SubscriptionSelectionResult{}, errors.New("subscription UID not found")
	}
	if !target.Supported {
		return SubscriptionSelectionResult{}, fmt.Errorf("subscription %q is unsupported: %s", target.Name, target.UnsupportedReason)
	}
	previousConfig := e.Config
	updated := e.Config
	updated.PreferredSubscriptionUID = uid
	if err := appconfig.Save(e.Paths.ConfigFile, updated); err != nil {
		return SubscriptionSelectionResult{}, err
	}
	e.Config = updated
	rollbackConfig := func() { _ = appconfig.Save(e.Paths.ConfigFile, previousConfig); e.Config = previousConfig }
	client, err := mihomo.New(updated)
	if err != nil {
		rollbackConfig()
		return SubscriptionSelectionResult{}, err
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		rollbackConfig()
		return SubscriptionSelectionResult{}, err
	}
	group, groupOK := proxies[updated.ManagedGroup]
	targetPresent := false
	if groupOK {
		for _, name := range group.All {
			if node, found := catalog.Node(name); found && node.SubscriptionUID == uid {
				targetPresent = true
				break
			}
		}
	}
	if !targetPresent {
		if _, err := installer.Reconcile(ctx, updated, e.Paths, client, true); err != nil {
			rollbackConfig()
			return SubscriptionSelectionResult{}, err
		}
		proxies, _ = client.Proxies(ctx)
		group = proxies[updated.ManagedGroup]
	}
	previousNode := group.Now
	candidates, err := e.Candidates(ctx)
	if err != nil {
		rollbackConfig()
		return SubscriptionSelectionResult{}, err
	}
	now := time.Now()
	var selected model.Candidate
	found := false
	for _, candidate := range candidates {
		if !candidate.CooldownUntil.After(now) && (candidate.Preferred || (updated.AllowSubscriptionFallback && !found)) {
			selected, found = candidate, true
			if candidate.Preferred {
				break
			}
		}
	}
	if !found {
		rollbackConfig()
		return SubscriptionSelectionResult{}, errors.New("selected subscription has no green candidate and fallback is unavailable")
	}
	if err := e.switchTo(ctx, selected.RuntimeName, "subscription selected", false, false); err != nil {
		if previousNode != "" {
			_ = client.Select(ctx, updated.ManagedGroup, previousNode)
		}
		rollbackConfig()
		return SubscriptionSelectionResult{}, err
	}
	target.Selected = true
	return SubscriptionSelectionResult{Subscription: target, Candidate: selected, FallbackUsed: !selected.Preferred}, nil
}

func (e *Engine) cachedCandidates(ctx context.Context) ([]model.Candidate, error) {
	s, err := state.Load(e.Paths.StateFile)
	if err != nil {
		return nil, err
	}
	maxAge := 2 * e.Config.ProbeInterval.Value()
	if s.LastProbe.IsZero() || time.Since(s.LastProbe) > maxAge {
		return nil, errors.New("cached latency is stale")
	}
	client, err := mihomo.New(e.Config)
	if err != nil {
		return nil, err
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return nil, err
	}
	group, ok := proxies[e.Config.ManagedGroup]
	if !ok || len(group.All) == 0 {
		return nil, errors.New("managed proxy group is unavailable")
	}
	delays := make(map[string]int, len(group.All))
	for _, name := range group.All {
		delays[name] = s.Node(name).LastDelayMS
	}
	catalog, _ := subscriptions.Scan(e.Paths, e.Config)
	result := e.buildCandidates(proxies, group, delays, &s, time.Now(), false, catalog)
	if len(result) == 0 {
		return nil, errors.New("no fresh cached green candidates")
	}
	sortCandidates(result)
	return result, nil
}

func (e *Engine) buildCandidates(proxies map[string]mihomo.Proxy, group mihomo.Proxy, delays map[string]int, s *state.State, now time.Time, record bool, catalog subscriptions.Catalog) []model.Candidate {
	result := make([]model.Candidate, 0, len(group.All))
	for _, name := range group.All {
		proxy, exists := proxies[name]
		delay := delays[name]
		node, mapped := catalog.Node(name)
		if !exists || !mihomo.IsLeaf(proxy) || (mapped && !node.Eligible) || (!mapped && !eligibleName(name, e.Config.AllowedRegions)) || delay <= 0 || delay >= e.Config.GreenThresholdMS {
			continue
		}
		if mapped && node.SubscriptionUID == e.Config.PreferredSubscriptionUID {
			if _, exists := s.Nodes[name]; !exists {
				if legacy := s.Nodes[node.Name]; legacy != nil {
					s.Nodes[name] = legacy
					delete(s.Nodes, node.Name)
				}
			}
		}
		if record {
			s.RecordDelay(name, delay)
		}
		if name == group.Now {
			s.CurrentNode = name
			s.CurrentDelayMS = delay
		}
		stats := s.Node(name)
		displayName := subscriptions.DisplayName(name)
		preferred := mapped && node.SubscriptionUID == e.Config.PreferredSubscriptionUID
		result = append(result, model.Candidate{
			RuntimeName: name, Name: displayName, Type: proxy.Type, DelayMS: delay,
			Score:           candidateScore(stats, delay, now) + regionPenalty(name, e.Config.RegionPriority),
			CooldownUntil:   stats.CooldownUntil,
			KnownSuccess:    stats.Successes > 0,
			SubscriptionUID: node.SubscriptionUID, SubscriptionName: node.SubscriptionName,
			Preferred: preferred, Fallback: mapped && !preferred,
		})
	}
	return result
}

func sortCandidates(result []model.Candidate) {
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Preferred != result[j].Preferred {
			return result[i].Preferred
		}
		if result[i].Score == result[j].Score {
			return result[i].Name < result[j].Name
		}
		return result[i].Score < result[j].Score
	})
}

func candidateScore(stats *state.NodeStats, delay int, now time.Time) int64 {
	if stats.CooldownUntil.After(now) {
		return 1_000_000_000 + stats.CooldownUntil.Sub(now).Milliseconds()
	}
	total := stats.Successes + stats.Failures
	failureRate := int64(500)
	if total > 0 {
		failureRate = int64(stats.Failures * 1000 / total)
	}
	knownPenalty := int64(200_000)
	if stats.Successes > 0 {
		knownPenalty = 0
	}
	recentPenalty := int64(0)
	if !stats.LastFailure.IsZero() && now.Sub(stats.LastFailure) < 24*time.Hour {
		recentPenalty = 50_000
	}
	return knownPenalty + failureRate*100 + recentPenalty + int64(delay*10+state.Jitter(stats.RecentDelays))
}

func eligibleName(name string, regions []string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, token := range []string{"direct", "reject", "剩余", "流量", "到期", "官网", "公告", "套餐", "倍率"} {
		if strings.Contains(lower, token) {
			return false
		}
	}
	for _, region := range regions {
		if region != "" && strings.Contains(lower, strings.ToLower(region)) {
			return true
		}
	}
	return false
}

func regionPenalty(name string, priority []string) int64 {
	lower := strings.ToLower(name)
	for index, region := range priority {
		if region != "" && strings.Contains(lower, strings.ToLower(region)) {
			return int64(index * 5_000)
		}
	}
	return int64(len(priority) * 5_000)
}

func (e *Engine) DiagnoseOnce(ctx context.Context, dryRun bool) ([]model.Candidate, error) {
	candidates, err := e.Candidates(ctx)
	if err != nil {
		return nil, err
	}
	if dryRun || len(candidates) == 0 {
		return candidates, nil
	}
	// An explicit non-dry run is a controlled failover test. It does not retry a task.
	if err := e.switchTo(ctx, candidates[0].RuntimeName, "manual diagnosis", false, true); err != nil {
		return candidates, err
	}
	return candidates, nil
}

// RecoverOnce performs the same bounded failover used for a detected executor
// network error. It is intentionally explicit and is useful when a failure card
// was already visible before the daemon started watching the log.
func (e *Engine) RecoverOnce(ctx context.Context) error {
	if _, err := os.Stat(e.Paths.PauseFile); err == nil {
		return errors.New("automatic recovery is paused")
	}
	kind := model.FailureConnection
	if s, err := state.Load(e.Paths.StateFile); err == nil {
		kind = recoveryFailureKind(s.LastFailureKind)
	}
	now := time.Now()
	return e.HandleFailure(ctx, model.FailureEvent{
		Kind:        kind,
		OccurredAt:  now,
		Fingerprint: "manual-recovery-" + now.UTC().Format("20060102T150405.000000000"),
	})
}

func recoveryFailureKind(last string) model.FailureKind {
	switch kind := model.FailureKind(last); kind {
	case model.FailureGeo, model.FailureDNS, model.FailureTimeout, model.FailureUnavailable, model.FailureConnection:
		return kind
	default:
		return model.FailureConnection
	}
}

func (e *Engine) switchTo(ctx context.Context, name, reason string, allowResume, closeConnections bool) error {
	if e.Config.ObserveOnly {
		return errors.New("observe_only is enabled")
	}
	client, err := mihomo.New(e.Config)
	if err != nil {
		return err
	}
	if err := client.Select(ctx, e.Config.ManagedGroup, name); err != nil {
		return err
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return err
	}
	if proxies[e.Config.ManagedGroup].Now != name {
		return fmt.Errorf("switch confirmation failed: selector reports %q", proxies[e.Config.ManagedGroup].Now)
	}
	if closeConnections {
		connections, err := client.Connections(ctx)
		if err != nil {
			e.Log.Printf("connections query after switch: %v", err)
		} else {
			for _, connection := range connections {
				if mihomo.IsAgentConnection(connection, e.Config.ProcessPath) {
					if closeErr := client.CloseConnection(ctx, connection.ID); closeErr != nil {
						e.Log.Printf("close Antigravity connection: %v", closeErr)
					}
				}
			}
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(e.Config.ReconnectDelay.Value()):
	}
	s, err := state.Load(e.Paths.StateFile)
	if err != nil {
		return err
	}
	s.CurrentNode = name
	s.LastSwitch = time.Now()
	s.Switches = append(s.Switches, s.LastSwitch)
	s.NextRecovery = time.Time{}
	s.LastError = reason
	if allowResume {
		e.resumeOnce(ctx, &s)
	}
	if err := state.Save(e.Paths.StateFile, s); err != nil {
		return err
	}
	if ip, region, checkErr := e.checkEgress(ctx); checkErr == nil {
		s.ExitIP, s.ExitRegion = ip, region
		node := s.Node(name)
		node.ExitIP, node.ExitRegion = ip, region
	} else {
		e.Log.Printf("egress verification: %v", checkErr)
	}
	if preflightErr := e.googlePreflight(ctx); preflightErr != nil {
		e.Log.Printf("Google preflight: %v", preflightErr)
	}
	return state.Save(e.Paths.StateFile, s)
}

func (e *Engine) resumeOnce(ctx context.Context, s *state.State) {
	if !e.Config.AutoResume {
		return
	}
	ag, err := antigravity.Discover(ctx)
	if err != nil {
		e.Log.Printf("resume skipped: %v", err)
		return
	}
	conversation, err := ag.LatestConversation(ctx)
	if err != nil {
		e.Log.Printf("resume skipped: %v", err)
		return
	}
	episode := conversation.CascadeID + ":" + strconv.Itoa(conversation.LastInputIndex)
	// One click per node for the same task prevents duplicate submissions while
	// still allowing the recovery loop to switch again and retry on a new node.
	attemptKey := episode + "|" + s.CurrentNode
	if lastAttempt, already := s.ResumedEpisodes[attemptKey]; already && time.Since(lastAttempt) < e.Config.NetworkCooldown.Value() {
		s.NextRecovery = lastAttempt.Add(e.Config.NetworkCooldown.Value())
		return
	}
	clicked, err := antigravity.ResumeLatestError(ctx)
	if err != nil || !clicked {
		s.NextRecovery = time.Now().Add(time.Minute)
		if err != nil {
			e.Log.Printf("background retry unavailable: %v", err)
		} else {
			e.Log.Printf("background retry card not found; will inspect again")
		}
		return
	}
	s.ResumedEpisodes[attemptKey] = time.Now()
	s.Pending = &state.PendingValidation{Episode: episode, Node: s.CurrentNode, Started: time.Now()}
	e.Log.Printf("background retry issued for episode %s on node %s", safeEpisode(episode), s.CurrentNode)
}

func safeEpisode(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:8] + "…" + value[len(value)-4:]
}

func (e *Engine) checkEgress(ctx context.Context) (string, string, error) {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", e.Config.MixedPort))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.Config.EgressURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("egress endpoint status %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	values := map[string]string{}
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			values[key] = value
		}
	}
	if values["ip"] == "" {
		return "", "", errors.New("egress endpoint did not return an IP")
	}
	return values["ip"], values["loc"], scanner.Err()
}

func (e *Engine) googlePreflight(ctx context.Context) error {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", e.Config.MixedPort))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, e.Config.GooglePreflightURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func (e *Engine) HandleFailure(ctx context.Context, event model.FailureEvent) error {
	if _, err := os.Stat(e.Paths.PauseFile); err == nil {
		return nil
	}
	e.reloadConfig()
	release, err := e.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()
	s, err := state.Load(e.Paths.StateFile)
	if err != nil {
		return err
	}
	now := time.Now()
	s.Prune(now, e.Config.SwitchWindow.Value())
	if event.Fingerprint != "" {
		if last, duplicate := s.FailureFingerprints[event.Fingerprint]; duplicate && now.Sub(last) < 10*time.Second {
			return nil
		}
		nodeFingerprint := event.Fingerprint + "|" + s.CurrentNode
		if _, duplicate := s.FailureFingerprints[nodeFingerprint]; duplicate {
			return nil
		}
		s.FailureFingerprints[event.Fingerprint] = now
		s.FailureFingerprints[nodeFingerprint] = now
	}
	s.LastFailureKind = string(event.Kind)
	s.LastError = "Agent network failure: " + string(event.Kind)
	s.AntigravityOK = false
	s.LastAgentSignal = "backend_log_failure"
	s.LastAgentSignalAt = now
	s.NextRecovery = time.Time{}
	pendingNode := ""
	if s.Pending != nil {
		pendingNode = s.Pending.Node
		node := s.Node(s.Pending.Node)
		node.Failures++
		node.LastFailure = now
		s.Pending = nil
	}
	if s.CurrentNode != "" {
		node := s.Node(s.CurrentNode)
		if pendingNode != s.CurrentNode {
			node.Failures++
		}
		node.LastFailure = now
		if event.Kind == model.FailureGeo {
			node.CooldownUntil = now.Add(e.Config.GeoCooldown.Value())
		} else {
			node.CooldownUntil = now.Add(e.Config.NetworkCooldown.Value())
		}
		node.CooldownReason = string(event.Kind)
		if node.ExitIP != "" {
			s.ExitCooldowns[state.ExitKey(s.CurrentNode, node.ExitIP)] = state.Cooldown{Until: node.CooldownUntil, Reason: string(event.Kind)}
		}
	}
	if len(s.Switches) >= e.Config.MaxSwitches {
		s.LastError = "switch rate limit reached"
		s.NextRecovery = s.Switches[0].Add(e.Config.SwitchWindow.Value())
		return state.Save(e.Paths.StateFile, s)
	}
	if err := state.Save(e.Paths.StateFile, s); err != nil {
		return err
	}
	candidates, err := e.cachedCandidates(ctx)
	usedCache := err == nil
	if err != nil {
		candidates, err = e.Candidates(ctx)
	}
	if err != nil {
		latest, loadErr := state.Load(e.Paths.StateFile)
		if loadErr == nil {
			latest.LastError = "candidate refresh failed: " + err.Error()
			latest.NextRecovery = now.Add(e.Config.ProbeInterval.Value())
			_ = state.Save(e.Paths.StateFile, latest)
		}
		return err
	}
	if candidate, ok := selectFailureCandidate(candidates, s.CurrentNode, event.Kind, now, e.Config.AllowSubscriptionFallback); ok {
		source := "live"
		if usedCache {
			source = "cached"
		}
		e.Log.Printf("failure %s from backend log: switching to %s / %s (%d ms, %s latency)", event.Kind, candidate.SubscriptionName, candidate.Name, candidate.DelayMS, source)
		return e.switchTo(ctx, candidate.RuntimeName, string(event.Kind), true, true)
	}
	s.LastError = "no green candidate outside cooldown"
	latest, loadErr := state.Load(e.Paths.StateFile)
	if loadErr == nil {
		latest.LastError = s.LastError
		latest.NextRecovery = nextCandidateAttempt(candidates, now, e.Config.ProbeInterval.Value())
		s = latest
	}
	return state.Save(e.Paths.StateFile, s)
}

func selectFailureCandidate(candidates []model.Candidate, current string, kind model.FailureKind, now time.Time, allowFallback bool) (model.Candidate, bool) {
	available := make([]model.Candidate, 0, len(candidates))
	hasPreferred := false
	for _, candidate := range candidates {
		if candidate.RuntimeName == current || candidate.CooldownUntil.After(now) {
			continue
		}
		available = append(available, candidate)
		hasPreferred = hasPreferred || candidate.Preferred
	}
	pool := available[:0]
	for _, candidate := range available {
		if hasPreferred && !candidate.Preferred {
			continue
		}
		if !hasPreferred && candidate.Fallback && !allowFallback {
			continue
		}
		pool = append(pool, candidate)
	}
	currentRegion := nodeRegion(subscriptions.DisplayName(current))
	if kind == model.FailureGeo && currentRegion != "" {
		for _, candidate := range pool {
			if region := nodeRegion(candidate.Name); region != "" && region != currentRegion {
				return candidate, true
			}
		}
	}
	if len(pool) > 0 {
		return pool[0], true
	}
	return model.Candidate{}, false
}

func nodeRegion(name string) string {
	lower := strings.ToLower(name)
	regions := []struct {
		name   string
		tokens []string
	}{
		{name: "jp", tokens: []string{"日本", "japan", "jp"}},
		{name: "sg", tokens: []string{"新加坡", "singapore", "sg"}},
		{name: "tw", tokens: []string{"台湾", "台灣", "taiwan", "tw"}},
		{name: "us", tokens: []string{"美国", "美國", "united states", "usa", "us"}},
	}
	for _, region := range regions {
		for _, token := range region.tokens {
			if strings.Contains(lower, token) {
				return region.name
			}
		}
	}
	return ""
}

func nextCandidateAttempt(candidates []model.Candidate, now time.Time, fallback time.Duration) time.Time {
	next := now.Add(fallback)
	for _, candidate := range candidates {
		if candidate.CooldownUntil.After(now) && candidate.CooldownUntil.Before(next) {
			next = candidate.CooldownUntil
		}
	}
	return next
}

func (e *Engine) processDeferredRecovery(ctx context.Context) {
	if _, err := os.Stat(e.Paths.PauseFile); err == nil {
		return
	}
	s, err := state.Load(e.Paths.StateFile)
	if err != nil || s.NextRecovery.IsZero() || time.Now().Before(s.NextRecovery) {
		return
	}
	if s.AntigravityOK || s.LastFailureKind == "" {
		s.NextRecovery = time.Time{}
		_ = state.Save(e.Paths.StateFile, s)
		return
	}
	s.NextRecovery = time.Time{}
	if err := state.Save(e.Paths.StateFile, s); err != nil {
		return
	}
	e.Log.Printf("deferred recovery window opened; continuing node rotation and background Retry")
	if err := e.RecoverOnce(ctx); err != nil {
		latest, loadErr := state.Load(e.Paths.StateFile)
		if loadErr == nil {
			latest.NextRecovery = time.Now().Add(time.Minute)
			_ = state.Save(e.Paths.StateFile, latest)
		}
		e.Log.Printf("deferred recovery: %v", err)
	}
}

// tryPreferredFailback returns from a fallback subscription only when the
// latest Agent conversation is idle. It never closes the existing stream and
// never triggers Retry, so a healthy task is not interrupted.
func (e *Engine) tryPreferredFailback(ctx context.Context) {
	e.reloadConfig()
	if e.Config.PreferredSubscriptionUID == "" {
		return
	}
	client, err := mihomo.New(e.Config)
	if err != nil {
		return
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return
	}
	group, ok := proxies[e.Config.ManagedGroup]
	if !ok || group.Now == "" {
		return
	}
	catalog, err := subscriptions.Scan(e.Paths, e.Config)
	if err != nil {
		return
	}
	current, mapped := catalog.Node(group.Now)
	if !mapped || current.SubscriptionUID == e.Config.PreferredSubscriptionUID {
		return
	}
	candidates, err := e.cachedCandidates(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	var target model.Candidate
	found := false
	for _, candidate := range candidates {
		if candidate.Preferred && candidate.RuntimeName != group.Now && !candidate.CooldownUntil.After(now) {
			target, found = candidate, true
			break
		}
	}
	if !found {
		return
	}
	ag, err := antigravity.Discover(ctx)
	if err != nil {
		return
	}
	conversation, err := ag.LatestConversation(ctx)
	if err != nil || conversation.IsRunning() {
		return
	}
	release, err := e.acquireOperation(ctx)
	if err != nil {
		return
	}
	defer release()
	// Recheck the selector after waiting for another recovery/rebuild process.
	if latest, latestErr := client.Proxies(ctx); latestErr != nil || latest[e.Config.ManagedGroup].Now != group.Now {
		return
	}
	e.Log.Printf("preferred subscription is green and Agent is idle: returning to %s / %s", target.SubscriptionName, target.Name)
	if err := e.switchTo(ctx, target.RuntimeName, "preferred subscription restored", false, false); err != nil {
		e.Log.Printf("preferred subscription failback: %v", err)
	}
}

func (e *Engine) HandleAgentSuccess(event model.SuccessEvent) error {
	s, err := state.Load(e.Paths.StateFile)
	if err != nil {
		return err
	}
	if !s.LastAgentSignalAt.IsZero() && event.OccurredAt.Before(s.LastAgentSignalAt) {
		return nil
	}
	s.AntigravityOK = true
	s.LastAgentSignal = "backend_log_success"
	s.LastAgentSignalAt = event.OccurredAt
	s.LastFailureKind = ""
	s.LastError = ""
	s.NextRecovery = time.Time{}
	if s.Pending != nil {
		node := s.Node(s.Pending.Node)
		node.Successes++
		node.LastSuccess = event.OccurredAt
		s.Pending = nil
	}
	e.Log.Printf("Agent streaming response observed in backend log; connection recovered")
	return state.Save(e.Paths.StateFile, s)
}

func (e *Engine) updateHealth(ctx context.Context) {
	s, loadErr := state.Load(e.Paths.StateFile)
	if loadErr != nil {
		e.Log.Printf("load state for health check: %v", loadErr)
		return
	}
	s.LastHealthCheck = time.Now()
	previousLocalError := strings.HasPrefix(s.LastError, "Antigravity health:")
	ag, err := antigravity.Discover(ctx)
	if err == nil {
		_, err = ag.CheckHealth(ctx)
	}
	s.LocalHealthOK = err == nil
	if err != nil {
		s.AntigravityOK = false
		s.LastError = "Antigravity health: " + err.Error()
	} else {
		if previousLocalError {
			s.LastError = ""
			if s.LastFailureKind == "" {
				s.AntigravityOK = true
			}
		}
		if s.Pending != nil {
			if conversation, rpcErr := ag.LatestConversation(ctx); rpcErr == nil {
				if conversation.IsRunning() {
					s.Pending.ObservedRunning = true
				}
				validationReady := time.Since(s.Pending.Started) >= e.Config.ValidationWindow.Value()
				if validationReady && validationSucceeded(conversation, s.Pending.ObservedRunning) {
					node := s.Node(s.Pending.Node)
					node.Successes++
					node.LastSuccess = time.Now()
					s.Pending = nil
					s.NextRecovery = time.Time{}
					s.AntigravityOK = true
					s.LastFailureKind = ""
					s.LastError = ""
				}
			}
		} else if s.LastFailureKind == "" {
			s.AntigravityOK = true
		}
	}
	_ = state.Save(e.Paths.StateFile, s)
}

func validationSucceeded(conversation antigravity.Conversation, observedRunning bool) bool {
	if !conversation.CompletedSuccessfully() {
		return false
	}
	return observedRunning
}

func (e *Engine) Run(ctx context.Context) error {
	lock, err := os.OpenFile(e.Paths.LockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("ag-nodeguard daemon is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	e.Log.Printf("daemon started")
	e.updateHealth(ctx)
	watcher := logwatch.Watcher{Path: e.Paths.AGLog, StartAtEnd: true, PollInterval: 250 * time.Millisecond}
	events := make(chan model.FailureEvent, 8)
	successes := make(chan model.SuccessEvent, 8)
	go func() {
		if watchErr := watcher.Run(ctx, events, successes); watchErr != nil && !errors.Is(watchErr, context.Canceled) {
			e.Log.Printf("log watcher: %v", watchErr)
		}
	}()
	healthTicker := time.NewTicker(e.Config.HealthInterval.Value())
	probeTicker := time.NewTicker(e.Config.ProbeInterval.Value())
	reconcileTicker := time.NewTicker(e.Config.SubscriptionScanInterval.Value())
	defer healthTicker.Stop()
	defer probeTicker.Stop()
	defer reconcileTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.Log.Printf("daemon stopped")
			return nil
		case event := <-events:
			if err := e.HandleFailure(ctx, event); err != nil {
				e.Log.Printf("failure handling: %v", err)
			}
		case success := <-successes:
			if err := e.HandleAgentSuccess(success); err != nil {
				e.Log.Printf("success handling: %v", err)
			}
		case <-healthTicker.C:
			e.updateHealth(ctx)
			e.processDeferredRecovery(ctx)
		case <-probeTicker.C:
			if _, err := e.Candidates(ctx); err != nil {
				e.Log.Printf("candidate refresh: %v", err)
			} else {
				e.tryPreferredFailback(ctx)
			}
		case <-reconcileTicker.C:
			e.reloadConfig()
			client, err := mihomo.New(e.Config)
			if err != nil {
				continue
			}
			release, lockErr := e.acquireOperation(ctx)
			if lockErr != nil {
				continue
			}
			if installer.NeedsReconcile(ctx, e.Config, e.Paths, client) {
				if _, reconcileErr := installer.Reconcile(ctx, e.Config, e.Paths, client, true); reconcileErr != nil {
					e.Log.Printf("subscription reconcile: %v", reconcileErr)
				} else {
					e.reloadConfig()
				}
			}
			release()
			if latest, stateErr := state.Load(e.Paths.StateFile); stateErr == nil {
				latest.LastSubscriptionScan = time.Now()
				_ = state.Save(e.Paths.StateFile, latest)
			}
			e.tryPreferredFailback(ctx)
		}
	}
}

func (e *Engine) CurrentStatus(ctx context.Context) (Status, error) {
	e.reloadConfig()
	s, err := state.Load(e.Paths.StateFile)
	if err != nil {
		return Status{}, err
	}
	now := time.Now()
	s.Prune(now, e.Config.SwitchWindow.Value())
	result := Status{ObservedAt: now, State: s, Installed: fileExists(e.Paths.LaunchAgent), Paused: fileExists(e.Paths.PauseFile), CurrentNode: subscriptions.DisplayName(s.CurrentNode), CurrentDelayMS: s.CurrentDelayMS, ExitIP: s.ExitIP, ExitRegion: s.ExitRegion, AntigravityOK: s.AntigravityOK, LocalHealthOK: s.LocalHealthOK, LastHealthCheck: s.LastHealthCheck, LastProbe: s.LastProbe, LastSwitch: s.LastSwitch, NextRecovery: s.NextRecovery, SwitchesInWindow: len(s.Switches), LastFailureKind: s.LastFailureKind, LastError: s.LastError, LastAgentSignal: s.LastAgentSignal, LastAgentSignalAt: s.LastAgentSignalAt, MaxSwitches: e.Config.MaxSwitches, PreferredSubscriptionUID: e.Config.PreferredSubscriptionUID, SubscriptionScanAt: s.LastSubscriptionScan, Cooldowns: map[string]time.Time{}}
	catalog, catalogErr := subscriptions.Scan(e.Paths, e.Config)
	if catalogErr == nil {
		if preferred, ok := catalog.Subscription(e.Config.PreferredSubscriptionUID); ok {
			result.PreferredSubscriptionName = preferred.Name
		}
		if node, ok := catalog.Node(s.CurrentNode); ok {
			result.CurrentSubscriptionUID = node.SubscriptionUID
			result.CurrentSubscriptionName = node.SubscriptionName
			result.UsingFallback = e.Config.PreferredSubscriptionUID != "" && node.SubscriptionUID != e.Config.PreferredSubscriptionUID
		}
	}
	for name, node := range s.Nodes {
		if node.CooldownUntil.After(now) {
			label := subscriptions.DisplayName(name)
			if mapped, ok := catalog.Node(name); ok {
				label = mapped.SubscriptionName + " / " + mapped.Name
			}
			result.Cooldowns[label] = node.CooldownUntil
		}
	}
	client, clientErr := mihomo.New(e.Config)
	if clientErr == nil {
		if version, versionErr := client.Version(ctx); versionErr == nil {
			result.MihomoVersion = version.Version
		}
		if proxies, proxyErr := client.Proxies(ctx); proxyErr == nil {
			if group, ok := proxies[e.Config.ManagedGroup]; ok && group.Now != "" {
				result.CurrentNode = subscriptions.DisplayName(group.Now)
				if node, mapped := catalog.Node(group.Now); mapped {
					result.CurrentSubscriptionUID = node.SubscriptionUID
					result.CurrentSubscriptionName = node.SubscriptionName
					result.UsingFallback = e.Config.PreferredSubscriptionUID != "" && node.SubscriptionUID != e.Config.PreferredSubscriptionUID
				}
			}
		}
	}
	// A regular lock file cannot be dialed. Probe the advisory lock instead.
	if lock, openErr := os.OpenFile(e.Paths.LockFile, os.O_CREATE|os.O_RDWR, 0o600); openErr == nil {
		if lockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr != nil {
			result.DaemonRunning = true
		} else {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
		lock.Close()
	}
	return result, nil
}

func (e *Engine) Doctor(ctx context.Context) []DoctorCheck {
	checks := []DoctorCheck{}
	appendCheck := func(name string, err error, details string) {
		if err != nil {
			checks = append(checks, DoctorCheck{Name: name, OK: false, Details: err.Error()})
			return
		}
		checks = append(checks, DoctorCheck{Name: name, OK: true, Details: details})
	}
	_, err := os.Stat(e.Paths.ProfilesFile)
	appendCheck("Clash Verge profile", err, e.Paths.ProfilesFile)
	clashVersion, clashVersionErr := appVersion(ctx, "/Applications/Clash Verge.app")
	if clashVersionErr == nil && !strings.HasPrefix(clashVersion, "2.5.") {
		clashVersionErr = fmt.Errorf("untested Clash Verge version %s (expected 2.5.x)", clashVersion)
	}
	appendCheck("Clash Verge version", clashVersionErr, clashVersion)
	agVersion, agVersionErr := appVersion(ctx, "/Applications/Antigravity.app")
	if agVersionErr == nil && !strings.HasPrefix(agVersion, "2.8.") {
		agVersionErr = fmt.Errorf("untested Antigravity version %s (expected 2.8.x)", agVersion)
	}
	appendCheck("Antigravity version", agVersionErr, agVersion)
	client, err := mihomo.New(e.Config)
	appendCheck("Mihomo controller", err, "Unix Socket or secure loopback controller")
	if err == nil {
		version, versionErr := client.Version(ctx)
		appendCheck("Mihomo version", versionErr, version.Version)
		proxies, proxyErr := client.Proxies(ctx)
		if proxyErr == nil {
			managed, managedOK := proxies[e.Config.ManagedGroup]
			if !managedOK || len(managed.All) == 0 {
				proxyErr = fmt.Errorf("managed group %q missing or empty", e.Config.ManagedGroup)
			}
		}
		appendCheck("Managed proxy group", proxyErr, e.Config.ManagedGroup)
	}
	catalog, catalogErr := subscriptions.Scan(e.Paths, e.Config)
	supported := 0
	for _, item := range catalog.Items {
		if item.Supported {
			supported++
		}
	}
	appendCheck("Clash subscriptions", catalogErr, fmt.Sprintf("%d supported remote profiles", supported))
	_, err = os.Stat(e.Paths.AGLog)
	appendCheck("Antigravity log", err, e.Paths.AGLog)
	ag, err := antigravity.Discover(ctx)
	appendCheck("Antigravity language server", err, "loopback health and Connect-RPC discovered")
	if err == nil {
		_, healthErr := ag.CheckHealth(ctx)
		appendCheck("Antigravity healthz", healthErr, "ok")
		conversation, rpcErr := ag.LatestConversation(ctx)
		rpcDetails := "latest conversation is readable"
		if conversation.Status != "" {
			rpcDetails += "; status=" + conversation.Status
		}
		appendCheck("Antigravity Connect-RPC", rpcErr, rpcDetails)
	}
	appendCheck("Antigravity CDP", antigravity.CheckCDP(ctx), "background page target available")
	if info, statErr := os.Stat(e.Paths.ConfigFile); statErr == nil {
		appendCheck("Config permissions", permissionError(info, 0o600), info.Mode().Perm().String())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		appendCheck("Config permissions", statErr, "")
	}
	if info, statErr := os.Stat(e.Paths.RuntimeConfig); statErr == nil {
		appendCheck("Runtime permissions", permissionError(info, 0o600), info.Mode().Perm().String())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		appendCheck("Runtime permissions", statErr, "")
	}
	if info, statErr := os.Stat(e.Paths.LogFile); statErr == nil {
		appendCheck("Log permissions", permissionError(info, 0o600), info.Mode().Perm().String())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		appendCheck("Log permissions", statErr, "")
	}
	return checks
}

func appVersion(ctx context.Context, appPath string) (string, error) {
	info := filepath.Join(appPath, "Contents", "Info.plist")
	output, err := exec.CommandContext(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleShortVersionString", info).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read %s version: %w: %s", filepath.Base(appPath), err, strings.TrimSpace(string(output)))
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", fmt.Errorf("%s has no version", filepath.Base(appPath))
	}
	return value, nil
}

func permissionError(info os.FileInfo, maximum os.FileMode) error {
	if info.Mode().Perm()&^maximum != 0 {
		return fmt.Errorf("permissions %s are broader than %s", info.Mode().Perm(), maximum)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
