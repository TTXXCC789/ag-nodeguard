package subscriptions

import (
	"os"
	"path/filepath"
	"testing"

	"ag-nodeguard/internal/appconfig"
)

func TestScanFiltersProjectsAndNamespacesDuplicateNames(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	mustWrite(t, filepath.Join(root, "profiles.yaml"), `current: first
items:
  - uid: first
    type: remote
    name: 套餐甲
    file: first.yaml
    option: {proxies: first-proxies, groups: first-groups, rules: first-rules}
  - uid: second
    type: remote
    name: 套餐乙
    file: second.yaml
    option: {proxies: second-proxies, groups: second-groups, rules: second-rules}
  - uid: merge-item
    type: merge
    name: Merge
    file: merge.yaml
`)
	mustWrite(t, filepath.Join(profilesDir, "first.yaml"), "proxies:\n  - {name: 日本01, type: ss, server: one, port: 1, cipher: aes-128-gcm, password: secret}\n")
	mustWrite(t, filepath.Join(profilesDir, "second.yaml"), "proxies:\n  - {name: 日本01, type: ss, server: two, port: 2, cipher: aes-128-gcm, password: secret}\n")
	for _, name := range []string{"first-proxies", "first-groups", "first-rules", "second-proxies", "second-groups", "second-rules"} {
		mustWrite(t, filepath.Join(profilesDir, name+".yaml"), "prepend: []\nappend: []\ndelete: []\n")
	}
	catalog, err := Scan(appconfig.Paths{ClashDir: root, ProfilesFile: filepath.Join(root, "profiles.yaml")}, appconfig.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 2 || !catalog.Items[0].ClashActive || !catalog.Items[0].Supported || !catalog.Items[1].Supported {
		t.Fatalf("unexpected subscriptions: %#v", catalog.Items)
	}
	first := catalog.Items[0].Nodes[0]
	second := catalog.Items[1].Nodes[0]
	if first.Name != second.Name || first.RuntimeName == second.RuntimeName {
		t.Fatalf("duplicate names were not isolated: %#v %#v", first, second)
	}
	if DisplayName(first.RuntimeName) != "日本01" || !IsManagedRuntimeName(first.RuntimeName) {
		t.Fatalf("runtime display mapping failed: %q", first.RuntimeName)
	}
	definitions := catalog.RuntimeDefinitions()
	if definitions[0]["password"] != "secret" || definitions[0]["name"] == "日本01" {
		t.Fatal("runtime definition was not cloned and namespaced")
	}
}

func TestScanRejectsUnsafePathsAndShowsProviderOnly(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	mustWrite(t, filepath.Join(root, "profiles.yaml"), `current: provider
items:
  - {uid: unsafe, type: remote, name: Unsafe, file: ../outside.yaml}
  - uid: provider
    type: remote
    name: Provider
    file: provider.yaml
    option: {proxies: p-proxies, groups: p-groups, rules: p-rules}
  - uid: linked
    type: remote
    name: Linked
    file: linked.yaml
    option: {proxies: l-proxies, groups: l-groups, rules: l-rules}
`)
	mustWrite(t, filepath.Join(profilesDir, "provider.yaml"), "proxy-providers:\n  source: {type: http, url: https://redacted.invalid}\n")
	mustWrite(t, filepath.Join(root, "real.yaml"), "proxies:\n  - {name: 日本01, type: direct}\n")
	if err := os.Symlink(filepath.Join(root, "real.yaml"), filepath.Join(profilesDir, "linked.yaml")); err != nil {
		t.Fatal(err)
	}
	catalog, err := Scan(appconfig.Paths{ClashDir: root, ProfilesFile: filepath.Join(root, "profiles.yaml")}, appconfig.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 3 {
		t.Fatalf("got %d subscriptions", len(catalog.Items))
	}
	if catalog.Items[0].Supported || catalog.Items[0].UnsupportedReason == "" {
		t.Fatal("path traversal was not rejected")
	}
	if catalog.Items[1].Supported || catalog.Items[1].UnsupportedReason != "仅包含 proxy-provider，当前版本暂不支持" {
		t.Fatalf("provider-only status = %#v", catalog.Items[1])
	}
	if catalog.Items[2].Supported || catalog.Items[2].UnsupportedReason == "" {
		t.Fatal("symlink was not rejected")
	}
}

func TestScanRejectsSymlinkedEnhancement(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	mustWrite(t, filepath.Join(root, "profiles.yaml"), `current: linked-enhancement
items:
  - uid: linked-enhancement
    type: remote
    name: Linked Enhancement
    file: profile.yaml
    option: {proxies: linked, groups: groups, rules: rules}
`)
	mustWrite(t, filepath.Join(profilesDir, "profile.yaml"), "proxies:\n  - {name: 日本01, type: direct}\n")
	mustWrite(t, filepath.Join(profilesDir, "groups.yaml"), "prepend: []\n")
	mustWrite(t, filepath.Join(profilesDir, "rules.yaml"), "prepend: []\n")
	mustWrite(t, filepath.Join(root, "outside.yaml"), "prepend: []\n")
	if err := os.Symlink(filepath.Join(root, "outside.yaml"), filepath.Join(profilesDir, "linked.yaml")); err != nil {
		t.Fatal(err)
	}
	catalog, err := Scan(appconfig.Paths{ClashDir: root, ProfilesFile: filepath.Join(root, "profiles.yaml")}, appconfig.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 1 || catalog.Items[0].Supported || catalog.Items[0].ProxiesFile != "" {
		t.Fatalf("symlinked enhancement was accepted: %#v", catalog.Items)
	}
}

func TestResolvePreferredMigratesSourceThenFallsBack(t *testing.T) {
	catalog := Catalog{CurrentUID: "active", Items: []Subscription{
		{UID: "first", Name: "Legacy Subscription", Supported: true},
		{UID: "active", Name: "Active Subscription", Supported: true, ClashActive: true},
	}}
	cfg := appconfig.Default()
	if got, ok := catalog.ResolvePreferred(cfg); !ok || got.UID != "first" {
		t.Fatalf("source group migration selected %#v", got)
	}
	cfg.SourceGroup = "missing"
	if got, ok := catalog.ResolvePreferred(cfg); !ok || got.UID != "active" {
		t.Fatalf("active fallback selected %#v", got)
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
