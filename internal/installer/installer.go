package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ag-nodeguard/internal/appconfig"
	"ag-nodeguard/internal/mihomo"
	"ag-nodeguard/internal/subscriptions"
	"go.yaml.in/yaml/v3"
)

const launchLabel = "com.local.ag-nodeguard"

type profileOptions struct {
	Proxies string `yaml:"proxies"`
	Groups  string `yaml:"groups"`
	Rules   string `yaml:"rules"`
}

type profileItem struct {
	UID    string         `yaml:"uid"`
	Type   string         `yaml:"type"`
	File   string         `yaml:"file"`
	Option profileOptions `yaml:"option"`
}

type profilesDocument struct {
	Current string        `yaml:"current"`
	Items   []profileItem `yaml:"items"`
}

type groupEnhancement struct {
	Prepend []map[string]any `yaml:"prepend"`
	Append  []map[string]any `yaml:"append"`
	Delete  []string         `yaml:"delete"`
}

type ruleEnhancement struct {
	Prepend []string `yaml:"prepend"`
	Append  []string `yaml:"append"`
	Delete  []string `yaml:"delete"`
}

type ReconcileResult struct {
	CandidateCount int
	InitialNode    string
	RuntimePath    string
	PreferredUID   string
	Subscriptions  int
}

type managedPathPair struct {
	Proxies string `json:"proxies,omitempty"`
	Groups  string `json:"groups"`
	Rules   string `json:"rules"`
}

func Install(ctx context.Context, paths appconfig.Paths) error {
	for _, dir := range []string{paths.SupportDir, paths.BinDir, paths.LogDir, filepath.Dir(paths.LaunchAgent)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if err := appconfig.SaveDefault(paths.ConfigFile); err != nil {
		return err
	}
	if err := ensurePrivateLog(paths.LogFile); err != nil {
		return err
	}
	cfg, err := appconfig.Load(paths.ConfigFile)
	if err != nil {
		return err
	}
	cfg, err = appconfig.MigrateRecoveryDefaults(paths.ConfigFile, cfg)
	if err != nil {
		return fmt.Errorf("migrate recovery settings: %w", err)
	}
	client, err := mihomo.New(cfg)
	if err != nil {
		return err
	}
	version, err := client.Version(ctx)
	if err != nil {
		return err
	}
	if err := mihomo.CompatibleVersion(version.Version); err != nil {
		return err
	}
	installedBinary := filepath.Join(paths.BinDir, "ag-nodeguard")
	if err := copyCurrentExecutable(installedBinary); err != nil {
		return err
	}
	plist := launchAgentPlist(installedBinary, paths)
	if err := atomicWrite(paths.LaunchAgent, []byte(plist), 0o600); err != nil {
		return err
	}
	if _, err := Reconcile(ctx, cfg, paths, client, true); err != nil {
		_ = os.Remove(paths.LaunchAgent)
		return fmt.Errorf("configure managed routing: %w", err)
	}
	_ = bootout(ctx, paths)
	if err := bootstrapLaunchAgent(ctx, paths.LaunchAgent); err != nil {
		_ = removeEnhancements(paths, cfg)
		_ = client.ReloadConfig(context.Background(), paths.ClashConfig)
		return err
	}
	return nil
}

func bootstrapLaunchAgent(ctx context.Context, plist string) error {
	var lastErr error
	var lastOutput string
	for attempt, delay := range []time.Duration{0, 150 * time.Millisecond, 350 * time.Millisecond, 700 * time.Millisecond} {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		output, err := exec.CommandContext(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist).CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr, lastOutput = err, strings.TrimSpace(string(output))
	}
	return fmt.Errorf("launchctl bootstrap: %w: %s", lastErr, lastOutput)
}

func ensurePrivateLog(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func Uninstall(ctx context.Context, paths appconfig.Paths) error {
	_ = bootout(ctx, paths)
	cfg, cfgErr := appconfig.Load(paths.ConfigFile)
	if cfgErr == nil {
		if err := removeEnhancements(paths, cfg); err != nil {
			return err
		}
		if client, err := mihomo.New(cfg); err == nil {
			_ = client.ReloadConfig(ctx, paths.ClashConfig)
		}
	}
	if err := os.Remove(paths.LaunchAgent); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(paths.RuntimeConfig); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(paths.SupportDir); err != nil {
		return err
	}
	return os.RemoveAll(paths.LogDir)
}

func Reconcile(ctx context.Context, cfg appconfig.Config, paths appconfig.Paths, client *mihomo.Client, apply bool) (ReconcileResult, error) {
	originalConfig := cfg
	preferredPersisted := false
	version, err := client.Version(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := mihomo.CompatibleVersion(version.Version); err != nil {
		return ReconcileResult{}, err
	}
	catalog, err := subscriptions.Scan(paths, cfg)
	if err != nil {
		return ReconcileResult{}, err
	}
	preferred, ok := catalog.ResolvePreferred(cfg)
	if !ok {
		return ReconcileResult{}, errors.New("no supported Clash Verge remote subscriptions")
	}
	if cfg.PreferredSubscriptionUID != preferred.UID {
		cfg.PreferredSubscriptionUID = preferred.UID
	}
	names := catalog.EligibleRuntimeNames(preferred.UID)
	if len(names) == 0 {
		return ReconcileResult{}, errors.New("subscriptions have no eligible leaf nodes")
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	// Preserve an existing dedicated selection across reinstall/profile refresh.
	current := resolveLeaf(cfg.ManagedGroup, proxies)
	if current != "" && !subscriptions.IsManagedRuntimeName(current) {
		current = subscriptions.RuntimeName(preferred.UID, current)
	}
	if !contains(names, current) {
		current = firstPreferredName(catalog, preferred.UID)
	}
	if current != "" {
		names = moveFirst(names, current)
	}
	group := managedGroup(cfg, names)
	runtimeBefore, runtimeErr := os.ReadFile(paths.RuntimeConfig)
	runtimeExisted := runtimeErr == nil
	if runtimeErr != nil && !errors.Is(runtimeErr, os.ErrNotExist) {
		return ReconcileResult{}, runtimeErr
	}
	restoreRuntime := func() error {
		if runtimeExisted {
			return atomicWrite(paths.RuntimeConfig, runtimeBefore, 0o600)
		}
		if err := os.Remove(paths.RuntimeConfig); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := synthesizeRuntime(paths, cfg, catalog.RuntimeDefinitions(), group); err != nil {
		return ReconcileResult{}, err
	}
	if err := validateRuntime(ctx, paths); err != nil {
		if restoreErr := restoreRuntime(); restoreErr != nil {
			err = fmt.Errorf("%w; runtime rollback failed: %v", err, restoreErr)
		}
		return ReconcileResult{}, err
	}
	touched := enhancementFiles(catalog)
	snapshot, err := takeSnapshot(touched...)
	if err != nil {
		return ReconcileResult{}, err
	}
	rollback := func(cause error) (ReconcileResult, error) {
		if restoreErr := snapshot.restore(); restoreErr != nil {
			cause = fmt.Errorf("%w; enhancement rollback failed: %v", cause, restoreErr)
		}
		if apply {
			_ = client.ReloadConfig(context.Background(), paths.ClashConfig)
		}
		if restoreErr := restoreRuntime(); restoreErr != nil {
			cause = fmt.Errorf("%w; runtime rollback failed: %v", cause, restoreErr)
		}
		if preferredPersisted {
			if restoreErr := appconfig.Save(paths.ConfigFile, originalConfig); restoreErr != nil {
				cause = fmt.Errorf("%w; preferred subscription rollback failed: %v", cause, restoreErr)
			}
		}
		return ReconcileResult{}, cause
	}
	if err := patchAllEnhancements(paths, cfg, catalog, group); err != nil {
		return rollback(err)
	}
	if apply {
		if err := client.ReloadConfig(ctx, paths.RuntimeConfig); err != nil {
			return rollback(err)
		}
		restoreSelections(ctx, client, proxies, cfg.ManagedGroup)
		if current != "" && contains(names, current) {
			if err := client.Select(ctx, cfg.ManagedGroup, current); err != nil {
				return rollback(fmt.Errorf("select initial managed node: %w", err))
			}
		}
	}
	if cfg.PreferredSubscriptionUID != originalConfig.PreferredSubscriptionUID {
		if err := appconfig.Save(paths.ConfigFile, cfg); err != nil {
			return rollback(fmt.Errorf("save preferred subscription: %w", err))
		}
		preferredPersisted = true
	}
	if err := subscriptions.WriteSnapshot(paths.InventoryFile, catalog.Fingerprint); err != nil {
		return rollback(fmt.Errorf("save subscription inventory: %w", err))
	}
	return ReconcileResult{CandidateCount: len(names), InitialNode: current, RuntimePath: paths.RuntimeConfig, PreferredUID: preferred.UID, Subscriptions: len(catalog.Items)}, nil
}

type enhancementSnapshot struct {
	paths []string
	data  [][]byte
}

func takeSnapshot(paths ...string) (enhancementSnapshot, error) {
	snapshot := enhancementSnapshot{paths: paths, data: make([][]byte, len(paths))}
	for index, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return enhancementSnapshot{}, err
		}
		snapshot.data[index] = data
	}
	return snapshot, nil
}

func (s enhancementSnapshot) restore() error {
	var result error
	for index, path := range s.paths {
		if err := atomicWrite(path, s.data[index], 0o600); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func ManagedGroupPresent(ctx context.Context, cfg appconfig.Config, client *mihomo.Client) bool {
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return false
	}
	group, ok := proxies[cfg.ManagedGroup]
	return ok && len(group.All) > 0
}

func NeedsReconcile(ctx context.Context, cfg appconfig.Config, paths appconfig.Paths, client *mihomo.Client) bool {
	catalog, err := subscriptions.Scan(paths, cfg)
	if err != nil {
		return false
	}
	storedFingerprint, err := subscriptions.ReadSnapshot(paths.InventoryFile)
	if err != nil || storedFingerprint != catalog.Fingerprint {
		return true
	}
	proxies, err := client.Proxies(ctx)
	if err != nil {
		return false
	}
	managed, managedOK := proxies[cfg.ManagedGroup]
	if !managedOK {
		return true
	}
	preferred, ok := catalog.ResolvePreferred(cfg)
	if !ok {
		return true
	}
	expected := catalog.EligibleRuntimeNames(preferred.UID)
	if len(expected) != len(managed.All) {
		return true
	}
	actual := make(map[string]bool, len(managed.All))
	for _, name := range managed.All {
		actual[name] = true
	}
	for _, name := range expected {
		if !actual[name] {
			return true
		}
	}
	return false
}

func eligibleNames(members []string, proxies map[string]mihomo.Proxy, regions []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, name := range members {
		proxy, ok := proxies[name]
		if !ok || !mihomo.IsLeaf(proxy) || looksInformational(name) || !allowedRegion(name, regions) || seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, name)
	}
	return result
}

func looksInformational(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{"剩余", "流量", "到期", "官网", "电报", "订阅", "更新", "群", "http://", "https://", "请到", "每次使用"} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func allowedRegion(name string, regions []string) bool {
	if len(regions) == 0 {
		return true
	}
	lower := strings.ToLower(name)
	for _, region := range regions {
		if strings.Contains(lower, strings.ToLower(region)) {
			return true
		}
	}
	return false
}

func resolveLeaf(name string, proxies map[string]mihomo.Proxy) string {
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		seen[name] = true
		proxy, ok := proxies[name]
		if !ok {
			return ""
		}
		if mihomo.IsLeaf(proxy) {
			return name
		}
		name = proxy.Now
	}
	return ""
}

func moveFirst(names []string, desired string) []string {
	for index, name := range names {
		if name == desired {
			copy(names[1:index+1], names[:index])
			names[0] = desired
			break
		}
	}
	return names
}

func managedGroup(cfg appconfig.Config, names []string) map[string]any {
	return map[string]any{"name": cfg.ManagedGroup, "type": "select", "proxies": names, "hidden": true}
}

func firstPreferredName(catalog subscriptions.Catalog, preferredUID string) string {
	for _, item := range catalog.Items {
		if item.UID != preferredUID || !item.Supported {
			continue
		}
		for _, node := range item.Nodes {
			if node.Eligible {
				return node.RuntimeName
			}
		}
	}
	return ""
}

func enhancementFiles(catalog subscriptions.Catalog) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, item := range catalog.Items {
		// Provider-only subscriptions cannot contribute nodes yet, but their
		// enhancements must still carry the managed group. This keeps the
		// Anti Gravity route alive if the user makes one Clash's active profile.
		if item.ProxiesFile == "" || item.GroupsFile == "" || item.RulesFile == "" {
			continue
		}
		for _, path := range []string{item.ProxiesFile, item.GroupsFile, item.RulesFile} {
			if path != "" && !seen[path] {
				seen[path] = true
				result = append(result, path)
			}
		}
	}
	return result
}

func patchAllEnhancements(paths appconfig.Paths, cfg appconfig.Config, catalog subscriptions.Catalog, group map[string]any) error {
	definitions := catalog.RuntimeDefinitions()
	for _, item := range catalog.Items {
		if item.ProxiesFile == "" || item.GroupsFile == "" || item.RulesFile == "" {
			continue
		}
		if err := patchEnhancementsAt(paths, cfg, item.ProxiesFile, item.GroupsFile, item.RulesFile, definitions, group); err != nil {
			return fmt.Errorf("patch subscription %q enhancements: %w", item.Name, err)
		}
	}
	return nil
}

func patchEnhancementsAt(paths appconfig.Paths, cfg appconfig.Config, proxiesPath, groupsPath, rulesPath string, definitions []map[string]any, group map[string]any) error {
	for _, path := range []string{proxiesPath, groupsPath, rulesPath} {
		if err := backupOnce(path); err != nil {
			return err
		}
	}
	proxies := groupEnhancement{}
	if err := readYAML(proxiesPath, &proxies); err != nil {
		return err
	}
	filteredProxies := proxies.Prepend[:0]
	for _, existing := range proxies.Prepend {
		if !subscriptions.IsManagedRuntimeName(fmt.Sprint(existing["name"])) {
			filteredProxies = append(filteredProxies, existing)
		}
	}
	proxies.Prepend = append(append([]map[string]any{}, definitions...), filteredProxies...)
	if err := writeYAML(proxiesPath, proxies); err != nil {
		return err
	}
	groups := groupEnhancement{}
	if err := readYAML(groupsPath, &groups); err != nil {
		return err
	}
	filteredGroups := groups.Prepend[:0]
	for _, existing := range groups.Prepend {
		if fmt.Sprint(existing["name"]) != cfg.ManagedGroup {
			filteredGroups = append(filteredGroups, existing)
		}
	}
	groups.Prepend = append([]map[string]any{group}, filteredGroups...)
	if err := writeYAML(groupsPath, groups); err != nil {
		return err
	}
	rules := ruleEnhancement{}
	if err := readYAML(rulesPath, &rules); err != nil {
		return err
	}
	filteredRules := rules.Prepend[:0]
	for _, existing := range rules.Prepend {
		if !isManagedRule(existing, cfg.ManagedGroup) {
			filteredRules = append(filteredRules, existing)
		}
	}
	rules.Prepend = append(processRules(paths, cfg), filteredRules...)
	if err := writeYAML(rulesPath, rules); err != nil {
		return err
	}
	return recordManagedPaths(paths.ManifestFile, managedPathPair{Proxies: proxiesPath, Groups: groupsPath, Rules: rulesPath})
}

func patchEnhancements(paths appconfig.Paths, cfg appconfig.Config, group map[string]any) error {
	groupsPath, rulesPath, err := activeEnhancementPaths(paths)
	if err != nil {
		return err
	}
	if err := backupOnce(groupsPath); err != nil {
		return err
	}
	if err := backupOnce(rulesPath); err != nil {
		return err
	}
	groups := groupEnhancement{}
	if err := readYAML(groupsPath, &groups); err != nil {
		return err
	}
	filteredGroups := groups.Prepend[:0]
	for _, existing := range groups.Prepend {
		if fmt.Sprint(existing["name"]) != cfg.ManagedGroup {
			filteredGroups = append(filteredGroups, existing)
		}
	}
	groups.Prepend = append([]map[string]any{group}, filteredGroups...)
	if err := writeYAML(groupsPath, groups); err != nil {
		return err
	}
	rules := ruleEnhancement{}
	if err := readYAML(rulesPath, &rules); err != nil {
		return err
	}
	managedRules := processRules(paths, cfg)
	filteredRules := rules.Prepend[:0]
	for _, existing := range rules.Prepend {
		if !isManagedRule(existing, cfg.ManagedGroup) {
			filteredRules = append(filteredRules, existing)
		}
	}
	rules.Prepend = append(managedRules, filteredRules...)
	if err := writeYAML(rulesPath, rules); err != nil {
		return err
	}
	return recordManagedPaths(paths.ManifestFile, managedPathPair{Groups: groupsPath, Rules: rulesPath})
}

func removeEnhancements(paths appconfig.Paths, cfg appconfig.Config) error {
	pairs, err := readManagedPaths(paths.ManifestFile)
	if err != nil {
		return err
	}
	if activePair, activeErr := activeEnhancementPair(paths); activeErr == nil {
		pairs = append(pairs, activePair)
	}
	seen := map[string]bool{}
	for _, pair := range pairs {
		key := pair.Groups + "\x00" + pair.Rules
		if pair.Groups == "" || pair.Rules == "" || seen[key] {
			continue
		}
		seen[key] = true
		if err := removeEnhancementsAt(pair.Proxies, pair.Groups, pair.Rules, cfg); err != nil {
			return err
		}
	}
	if err := os.Remove(paths.ManifestFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeEnhancementsAt(proxiesPath, groupsPath, rulesPath string, cfg appconfig.Config) error {
	if proxiesPath != "" {
		proxies := groupEnhancement{}
		if err := readYAML(proxiesPath, &proxies); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			kept := proxies.Prepend[:0]
			for _, proxy := range proxies.Prepend {
				if !subscriptions.IsManagedRuntimeName(fmt.Sprint(proxy["name"])) {
					kept = append(kept, proxy)
				}
			}
			proxies.Prepend = kept
			if err := writeYAML(proxiesPath, proxies); err != nil {
				return err
			}
		}
	}
	groups := groupEnhancement{}
	if err := readYAML(groupsPath, &groups); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		kept := groups.Prepend[:0]
		for _, group := range groups.Prepend {
			if fmt.Sprint(group["name"]) != cfg.ManagedGroup {
				kept = append(kept, group)
			}
		}
		groups.Prepend = kept
		if err := writeYAML(groupsPath, groups); err != nil {
			return err
		}
	}
	rules := ruleEnhancement{}
	if err := readYAML(rulesPath, &rules); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		kept := rules.Prepend[:0]
		for _, rule := range rules.Prepend {
			if !isManagedRule(rule, cfg.ManagedGroup) {
				kept = append(kept, rule)
			}
		}
		rules.Prepend = kept
		if err := writeYAML(rulesPath, rules); err != nil {
			return err
		}
	}
	for _, original := range []string{proxiesPath, groupsPath, rulesPath} {
		if original == "" {
			continue
		}
		backup := original + ".ag-nodeguard.bak"
		if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func activeEnhancementPair(paths appconfig.Paths) (managedPathPair, error) {
	var profiles profilesDocument
	if err := readYAML(paths.ProfilesFile, &profiles); err != nil {
		return managedPathPair{}, err
	}
	for _, item := range profiles.Items {
		if item.UID == profiles.Current && item.Type == "remote" {
			base := filepath.Join(paths.ClashDir, "profiles")
			return managedPathPair{
				Proxies: filepath.Join(base, item.Option.Proxies+".yaml"),
				Groups:  filepath.Join(base, item.Option.Groups+".yaml"),
				Rules:   filepath.Join(base, item.Option.Rules+".yaml"),
			}, nil
		}
	}
	return managedPathPair{}, fmt.Errorf("active profile %q not found", profiles.Current)
}

func recordManagedPaths(path string, pair managedPathPair) error {
	if path == "" {
		return nil
	}
	pairs, err := readManagedPaths(path)
	if err != nil {
		return err
	}
	for _, existing := range pairs {
		if existing == pair {
			return nil
		}
	}
	pairs = append(pairs, pair)
	data, err := json.MarshalIndent(pairs, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0o600)
}

func readManagedPaths(path string) ([]managedPathPair, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pairs []managedPathPair
	if err := json.Unmarshal(data, &pairs); err != nil {
		return nil, fmt.Errorf("parse managed path manifest: %w", err)
	}
	return pairs, nil
}

func activeEnhancementPaths(paths appconfig.Paths) (string, string, error) {
	var profiles profilesDocument
	if err := readYAML(paths.ProfilesFile, &profiles); err != nil {
		return "", "", err
	}
	for _, item := range profiles.Items {
		if item.UID == profiles.Current && item.Type == "remote" {
			if item.Option.Groups == "" || item.Option.Rules == "" {
				return "", "", errors.New("active profile has no group/rule enhancements")
			}
			base := filepath.Join(paths.ClashDir, "profiles")
			return filepath.Join(base, item.Option.Groups+".yaml"), filepath.Join(base, item.Option.Rules+".yaml"), nil
		}
	}
	return "", "", fmt.Errorf("active profile %q not found", profiles.Current)
}

func synthesizeRuntime(paths appconfig.Paths, cfg appconfig.Config, definitions []map[string]any, group map[string]any) error {
	var document map[string]any
	if err := readYAML(paths.ClashConfig, &document); err != nil {
		return err
	}
	proxies := asMapSlice(document["proxies"])
	filteredProxies := proxies[:0]
	for _, existing := range proxies {
		if !subscriptions.IsManagedRuntimeName(fmt.Sprint(existing["name"])) {
			filteredProxies = append(filteredProxies, existing)
		}
	}
	document["proxies"] = append(filteredProxies, definitions...)
	groups := asMapSlice(document["proxy-groups"])
	filteredGroups := groups[:0]
	for _, existing := range groups {
		if fmt.Sprint(existing["name"]) != cfg.ManagedGroup {
			filteredGroups = append(filteredGroups, existing)
		}
	}
	document["proxy-groups"] = append(filteredGroups, group)
	rules := asStringSlice(document["rules"])
	filteredRules := rules[:0]
	for _, existing := range rules {
		if !isManagedRule(existing, cfg.ManagedGroup) {
			filteredRules = append(filteredRules, existing)
		}
	}
	document["rules"] = append(processRules(paths, cfg), filteredRules...)
	data, err := yaml.Marshal(document)
	if err != nil {
		return err
	}
	return atomicWrite(paths.RuntimeConfig, data, 0o600)
}

func restoreSelections(ctx context.Context, client *mihomo.Client, before map[string]mihomo.Proxy, managedGroup string) {
	after, err := client.Proxies(ctx)
	if err != nil {
		return
	}
	for name, previous := range before {
		if name == managedGroup || previous.Now == "" || len(previous.All) == 0 {
			continue
		}
		current, ok := after[name]
		if !ok || current.Now == previous.Now || !contains(current.All, previous.Now) {
			continue
		}
		_ = client.Select(ctx, name, previous.Now)
	}
}

func validateRuntime(ctx context.Context, paths appconfig.Paths) error {
	core := "/Applications/Clash Verge.app/Contents/MacOS/verge-mihomo"
	if _, err := os.Stat(core); err != nil {
		return fmt.Errorf("mihomo core missing: %w", err)
	}
	validateCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(validateCtx, core, "-t", "-d", paths.ClashDir, "-f", paths.RuntimeConfig).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mihomo config validation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func processRules(paths appconfig.Paths, cfg appconfig.Config) []string {
	return []string{
		"PROCESS-PATH," + cfg.ProcessPath + "," + cfg.ManagedGroup,
		"PROCESS-PATH," + filepath.Join(paths.BinDir, "ag-nodeguard") + "," + cfg.ManagedGroup,
	}
}

func isManagedRule(rule, group string) bool {
	parts := strings.Split(rule, ",")
	return len(parts) >= 3 && strings.EqualFold(parts[0], "PROCESS-PATH") && parts[len(parts)-1] == group
}

func readYAML(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func writeYAML(path string, value any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func backupOnce(path string) error {
	backup := path + ".ag-nodeguard.bak"
	if _, err := os.Stat(backup); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return atomicWrite(backup, data, 0o600)
}

func asMapSlice(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]map[string]any); ok {
			return typed
		}
		return nil
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func asStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, fmt.Sprint(item))
	}
	return result
}

func copyCurrentExecutable(destination string) error {
	source, err := os.Executable()
	if err != nil {
		return err
	}
	source, _ = filepath.EvalSymlinks(source)
	destination, _ = filepath.Abs(destination)
	if source == destination {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".binary-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, destination)
}

func launchAgentPlist(binary string, paths appconfig.Paths) string {
	escape := html.EscapeString
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>` + launchLabel + `</string>
<key>ProgramArguments</key><array><string>` + escape(binary) + `</string><string>daemon</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer>
<key>Umask</key><integer>63</integer>
<key>StandardOutPath</key><string>` + escape(paths.LogFile) + `</string>
<key>StandardErrorPath</key><string>` + escape(paths.LogFile) + `</string>
<key>ProcessType</key><string>Background</string>
</dict></plist>
`
}

func bootout(ctx context.Context, paths appconfig.Paths) error {
	output, err := exec.CommandContext(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel)).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "Could not find service") && !strings.Contains(string(output), "No such process") {
		return fmt.Errorf("launchctl bootout: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
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

func SortedKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
