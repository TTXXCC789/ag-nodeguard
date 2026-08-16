package installer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ag-nodeguard/internal/appconfig"
	"ag-nodeguard/internal/mihomo"
	"ag-nodeguard/internal/subscriptions"
)

func TestEligibleNamesFiltersGraphAndInformation(t *testing.T) {
	proxies := map[string]mihomo.Proxy{
		"日本01":      {Type: "Hysteria2"},
		"新加坡01":     {Type: "VLESS"},
		"美国组":       {Type: "Selector", All: []string{"美国01"}},
		"美国01":      {Type: "Trojan"},
		"剩余流量 20GB": {Type: "Shadowsocks"},
		"DIRECT":    {Type: "Direct"},
	}
	members := []string{"日本01", "新加坡01", "美国组", "剩余流量 20GB", "DIRECT", "日本01"}
	got := eligibleNames(members, proxies, []string{"日本", "新加坡", "美国"})
	want := []string{"日本01", "新加坡01"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("eligibleNames = %#v, want %#v", got, want)
	}
}

func TestAllRemoteProfilesReceiveNamespacedManagedRouting(t *testing.T) {
	dir := t.TempDir()
	profilesDir := filepath.Join(dir, "profiles")
	paths := appconfig.Paths{
		ClashDir: dir, ProfilesFile: filepath.Join(dir, "profiles.yaml"),
		BinDir: filepath.Join(dir, "bin"), ManifestFile: filepath.Join(dir, "support", "managed-paths.json"),
	}
	mustWrite(t, paths.ProfilesFile, `current: first
items:
  - uid: first
    type: remote
    name: 套餐甲
    file: first.yaml
    option: {proxies: first-proxies, groups: first-groups, rules: first-rules}
  - uid: second
    type: remote
    name: Provider套餐
    file: second.yaml
    option: {proxies: second-proxies, groups: second-groups, rules: second-rules}
`)
	mustWrite(t, filepath.Join(profilesDir, "first.yaml"), "proxies:\n  - {name: 日本01, type: ss, server: one, port: 1, cipher: aes-128-gcm, password: secret}\n")
	mustWrite(t, filepath.Join(profilesDir, "second.yaml"), "proxy-providers:\n  source: {type: http, url: https://redacted.invalid}\n")
	for _, prefix := range []string{"first", "second"} {
		mustWrite(t, filepath.Join(profilesDir, prefix+"-proxies.yaml"), "prepend:\n  - {name: UserProxy, type: direct}\nappend: []\ndelete: []\n")
		mustWrite(t, filepath.Join(profilesDir, prefix+"-groups.yaml"), "prepend:\n  - {name: UserGroup, type: select, proxies: [DIRECT]}\nappend: []\ndelete: []\n")
		mustWrite(t, filepath.Join(profilesDir, prefix+"-rules.yaml"), "prepend: [DOMAIN,example.com,DIRECT]\nappend: []\ndelete: []\n")
	}
	cfg := appconfig.Default()
	catalog, err := subscriptions.Scan(paths, cfg)
	if err != nil {
		t.Fatal(err)
	}
	group := managedGroup(cfg, catalog.EligibleRuntimeNames("first"))
	if err := patchAllEnhancements(paths, cfg, catalog, group); err != nil {
		t.Fatal(err)
	}
	if err := patchAllEnhancements(paths, cfg, catalog, group); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"first", "second"} {
		var proxyFile groupEnhancement
		if err := readYAML(filepath.Join(profilesDir, prefix+"-proxies.yaml"), &proxyFile); err != nil {
			t.Fatal(err)
		}
		managedProxies := 0
		for _, proxy := range proxyFile.Prepend {
			if subscriptions.IsManagedRuntimeName(proxy["name"].(string)) {
				managedProxies++
			}
		}
		if managedProxies != 1 || len(proxyFile.Prepend) != 2 {
			t.Fatalf("%s proxy enhancement not idempotent: %#v", prefix, proxyFile.Prepend)
		}
		var groups groupEnhancement
		if err := readYAML(filepath.Join(profilesDir, prefix+"-groups.yaml"), &groups); err != nil {
			t.Fatal(err)
		}
		if len(groups.Prepend) != 2 || groups.Prepend[0]["name"] != cfg.ManagedGroup || groups.Prepend[1]["name"] != "UserGroup" {
			t.Fatalf("%s managed group missing: %#v", prefix, groups.Prepend)
		}
	}
	if err := removeEnhancements(paths, cfg); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"first", "second"} {
		var proxyFile groupEnhancement
		if err := readYAML(filepath.Join(profilesDir, prefix+"-proxies.yaml"), &proxyFile); err != nil {
			t.Fatal(err)
		}
		if len(proxyFile.Prepend) != 1 || proxyFile.Prepend[0]["name"] != "UserProxy" {
			t.Fatalf("%s user proxy was not restored: %#v", prefix, proxyFile.Prepend)
		}
	}
}

func TestEnhancementInstallIsIdempotentAndRemovalPreservesUserData(t *testing.T) {
	dir := t.TempDir()
	profilesDir := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := appconfig.Paths{
		ClashDir: dir, ProfilesFile: filepath.Join(dir, "profiles.yaml"),
		BinDir: filepath.Join(dir, "bin"), ManifestFile: filepath.Join(dir, "support", "managed-paths.json"),
	}
	mustWrite(t, paths.ProfilesFile, "current: p1\nitems:\n  - uid: p1\n    type: remote\n    file: p1.yaml\n    option:\n      groups: groups1\n      rules: rules1\n")
	groupsPath := filepath.Join(profilesDir, "groups1.yaml")
	rulesPath := filepath.Join(profilesDir, "rules1.yaml")
	mustWrite(t, groupsPath, "prepend:\n  - name: UserGroup\n    type: select\n    proxies: [DIRECT]\nappend: []\ndelete: []\n")
	mustWrite(t, rulesPath, "prepend:\n  - DOMAIN,example.com,DIRECT\nappend: []\ndelete: []\n")
	cfg := appconfig.Default()
	group := managedGroup(cfg, []string{"日本01", "新加坡01"})
	if err := patchEnhancements(paths, cfg, group); err != nil {
		t.Fatal(err)
	}
	if err := patchEnhancements(paths, cfg, group); err != nil {
		t.Fatal(err)
	}
	var groups groupEnhancement
	if err := readYAML(groupsPath, &groups); err != nil {
		t.Fatal(err)
	}
	managedCount := 0
	for _, value := range groups.Prepend {
		if value["name"] == cfg.ManagedGroup {
			managedCount++
		}
	}
	if managedCount != 1 || len(groups.Prepend) != 2 {
		t.Fatalf("groups after repeat install: %#v", groups.Prepend)
	}
	if err := removeEnhancements(paths, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.ManifestFile); !os.IsNotExist(err) {
		t.Fatalf("managed path manifest was not removed: %v", err)
	}
	if err := readYAML(groupsPath, &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups.Prepend) != 1 || groups.Prepend[0]["name"] != "UserGroup" {
		t.Fatalf("user group not preserved: %#v", groups.Prepend)
	}
	var rules ruleEnhancement
	if err := readYAML(rulesPath, &rules); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules.Prepend, []string{"DOMAIN,example.com,DIRECT"}) {
		t.Fatalf("user rule not preserved: %#v", rules.Prepend)
	}
}

func TestSynthesizeRuntimePrependsProcessRules(t *testing.T) {
	dir := t.TempDir()
	paths := appconfig.Paths{ClashConfig: filepath.Join(dir, "clash.yaml"), RuntimeConfig: filepath.Join(dir, "runtime.yaml"), BinDir: filepath.Join(dir, "bin")}
	mustWrite(t, paths.ClashConfig, "mixed-port: 7897\nproxies:\n  - name: Example-01\n    type: ss\n    server: 127.0.0.1\n    port: 1\n    cipher: aes-128-gcm\n    password: test\nproxy-groups:\n  - name: Example Subscription\n    type: select\n    proxies: [Example-01]\nrules:\n  - MATCH,Example Subscription\n")
	cfg := appconfig.Default()
	if err := synthesizeRuntime(paths, cfg, nil, managedGroup(cfg, []string{"Example-01"})); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := readYAML(paths.RuntimeConfig, &document); err != nil {
		t.Fatal(err)
	}
	rules := asStringSlice(document["rules"])
	if len(rules) != 3 || rules[0] != "PROCESS-PATH,"+cfg.ProcessPath+","+cfg.ManagedGroup || rules[2] != "MATCH,Example Subscription" {
		t.Fatalf("unexpected rules: %#v", rules)
	}
}

func TestEnhancementSnapshotRollback(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "groups.yaml")
	second := filepath.Join(dir, "rules.yaml")
	mustWrite(t, first, "original groups\n")
	mustWrite(t, second, "original rules\n")
	snapshot, err := takeSnapshot(first, second)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, first, "broken\n")
	mustWrite(t, second, "broken\n")
	if err := snapshot.restore(); err != nil {
		t.Fatal(err)
	}
	firstData, _ := os.ReadFile(first)
	secondData, _ := os.ReadFile(second)
	if string(firstData) != "original groups\n" || string(secondData) != "original rules\n" {
		t.Fatal("snapshot did not restore exact contents")
	}
}

func TestPrivateLogAndLaunchAgentUmask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeguard.log")
	if err := os.WriteFile(path, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateLog(path); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %o", info.Mode().Perm())
	}
	plist := launchAgentPlist("/tmp/ag-nodeguard", appconfig.Paths{LogFile: path})
	if !strings.Contains(plist, "<key>Umask</key><integer>63</integer>") {
		t.Fatal("LaunchAgent does not enforce umask 077")
	}
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
