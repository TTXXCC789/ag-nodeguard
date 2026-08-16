package subscriptions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"ag-nodeguard/internal/appconfig"
	"go.yaml.in/yaml/v3"
)

const RuntimePrefix = "__AGNG__"

var safeUID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type profileOptions struct {
	Proxies string `yaml:"proxies"`
	Groups  string `yaml:"groups"`
	Rules   string `yaml:"rules"`
}

type profileItem struct {
	UID     string         `yaml:"uid"`
	Type    string         `yaml:"type"`
	Name    string         `yaml:"name"`
	File    string         `yaml:"file"`
	Updated int64          `yaml:"updated"`
	Option  profileOptions `yaml:"option"`
}

type profilesDocument struct {
	Current string        `yaml:"current"`
	Items   []profileItem `yaml:"items"`
}

type profileDocument struct {
	Proxies        []map[string]any `yaml:"proxies"`
	ProxyProviders map[string]any   `yaml:"proxy-providers"`
}

type Node struct {
	RuntimeName      string         `json:"-"`
	Name             string         `json:"name"`
	Type             string         `json:"type"`
	SubscriptionUID  string         `json:"subscription_uid"`
	SubscriptionName string         `json:"subscription_name"`
	Eligible         bool           `json:"-"`
	Definition       map[string]any `json:"-"`
}

type Subscription struct {
	UID               string    `json:"uid"`
	Name              string    `json:"name"`
	Selected          bool      `json:"selected"`
	ClashActive       bool      `json:"clash_active"`
	Supported         bool      `json:"supported"`
	UnsupportedReason string    `json:"unsupported_reason,omitempty"`
	NodeCount         int       `json:"node_count"`
	EligibleNodeCount int       `json:"eligible_node_count"`
	GreenNodeCount    int       `json:"green_node_count"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
	ProfileFile       string    `json:"-"`
	ProxiesFile       string    `json:"-"`
	GroupsFile        string    `json:"-"`
	RulesFile         string    `json:"-"`
	Nodes             []Node    `json:"-"`
}

type Catalog struct {
	CurrentUID  string
	Fingerprint string
	Items       []Subscription
	byRuntime   map[string]Node
}

type snapshot struct {
	Fingerprint string `json:"fingerprint"`
}

func Scan(paths appconfig.Paths, cfg appconfig.Config) (Catalog, error) {
	indexData, err := os.ReadFile(paths.ProfilesFile)
	if err != nil {
		return Catalog{}, err
	}
	var profiles profilesDocument
	if err := yaml.Unmarshal(indexData, &profiles); err != nil {
		return Catalog{}, fmt.Errorf("parse Clash Verge profiles index: %w", err)
	}
	profilesDir := filepath.Join(paths.ClashDir, "profiles")
	hasher := sha256.New()
	_, _ = hasher.Write(indexData)
	catalog := Catalog{CurrentUID: profiles.Current, byRuntime: map[string]Node{}}
	for _, item := range profiles.Items {
		if item.Type != "remote" {
			continue
		}
		subscription := Subscription{UID: item.UID, Name: item.Name, ClashActive: item.UID == profiles.Current}
		if subscription.Name == "" {
			subscription.Name = item.UID
		}
		if !safeUID.MatchString(item.UID) {
			subscription.UnsupportedReason = "订阅 UID 不安全"
			catalog.Items = append(catalog.Items, subscription)
			continue
		}
		profilePath, pathErr := safeProfilePath(profilesDir, item.File)
		if pathErr != nil {
			subscription.UnsupportedReason = pathErr.Error()
			catalog.Items = append(catalog.Items, subscription)
			continue
		}
		subscription.ProfileFile = profilePath
		subscription.ProxiesFile, _ = enhancementPath(profilesDir, item.Option.Proxies)
		subscription.GroupsFile, _ = enhancementPath(profilesDir, item.Option.Groups)
		subscription.RulesFile, _ = enhancementPath(profilesDir, item.Option.Rules)
		data, readErr := os.ReadFile(profilePath)
		if readErr != nil {
			subscription.UnsupportedReason = "订阅文件不可读"
			catalog.Items = append(catalog.Items, subscription)
			continue
		}
		_, _ = hasher.Write([]byte(item.UID))
		_, _ = hasher.Write(data)
		var document profileDocument
		if parseErr := yaml.Unmarshal(data, &document); parseErr != nil {
			subscription.UnsupportedReason = "订阅 YAML 无法解析"
			catalog.Items = append(catalog.Items, subscription)
			continue
		}
		subscription.NodeCount = len(document.Proxies)
		if len(document.Proxies) == 0 {
			if len(document.ProxyProviders) > 0 {
				subscription.UnsupportedReason = "仅包含 proxy-provider，当前版本暂不支持"
			} else {
				subscription.UnsupportedReason = "订阅不包含直接代理节点"
			}
			catalog.Items = append(catalog.Items, subscription)
			continue
		}
		nameMap := map[string]string{}
		for _, definition := range document.Proxies {
			name := strings.TrimSpace(fmt.Sprint(definition["name"]))
			if name != "" {
				nameMap[name] = RuntimeName(item.UID, name)
			}
		}
		for _, definition := range document.Proxies {
			name := strings.TrimSpace(fmt.Sprint(definition["name"]))
			if name == "" {
				continue
			}
			cloned := cloneMap(definition)
			runtimeName := nameMap[name]
			cloned["name"] = runtimeName
			for _, reference := range []string{"dialer-proxy", "detour"} {
				if raw, ok := cloned[reference].(string); ok {
					if rewritten := nameMap[raw]; rewritten != "" {
						cloned[reference] = rewritten
					}
				}
			}
			node := Node{
				RuntimeName: runtimeName, Name: name, Type: fmt.Sprint(definition["type"]),
				SubscriptionUID: item.UID, SubscriptionName: subscription.Name,
				Eligible: eligibleName(name, cfg.AllowedRegions), Definition: cloned,
			}
			if node.Eligible {
				subscription.EligibleNodeCount++
			}
			subscription.Nodes = append(subscription.Nodes, node)
			catalog.byRuntime[runtimeName] = node
		}
		subscription.Supported = subscription.EligibleNodeCount > 0 && subscription.ProxiesFile != "" && subscription.GroupsFile != "" && subscription.RulesFile != ""
		if !subscription.Supported && subscription.UnsupportedReason == "" {
			subscription.UnsupportedReason = "订阅缺少可用地区节点或增强配置"
		}
		if item.Updated > 0 {
			subscription.UpdatedAt = time.Unix(item.Updated, 0)
		}
		catalog.Items = append(catalog.Items, subscription)
	}
	catalog.Fingerprint = hex.EncodeToString(hasher.Sum(nil))
	return catalog, nil
}

func RuntimeName(uid, name string) string {
	sum := sha256.Sum256([]byte(uid))
	return RuntimePrefix + hex.EncodeToString(sum[:4]) + "__" + name
}

func DisplayName(runtime string) string {
	if !strings.HasPrefix(runtime, RuntimePrefix) {
		return runtime
	}
	if index := strings.Index(strings.TrimPrefix(runtime, RuntimePrefix), "__"); index >= 0 {
		return strings.TrimPrefix(runtime, RuntimePrefix)[index+2:]
	}
	return runtime
}

func IsManagedRuntimeName(name string) bool { return strings.HasPrefix(name, RuntimePrefix) }

func (c Catalog) Node(runtime string) (Node, bool) {
	node, ok := c.byRuntime[runtime]
	return node, ok
}

func (c Catalog) Subscription(uid string) (Subscription, bool) {
	for _, item := range c.Items {
		if item.UID == uid {
			return item, true
		}
	}
	return Subscription{}, false
}

func (c Catalog) ResolvePreferred(cfg appconfig.Config) (Subscription, bool) {
	if cfg.PreferredSubscriptionUID != "" {
		if item, ok := c.Subscription(cfg.PreferredSubscriptionUID); ok && item.Supported {
			return item, true
		}
	}
	for _, item := range c.Items {
		if item.Supported && item.Name == cfg.SourceGroup {
			return item, true
		}
	}
	for _, item := range c.Items {
		if item.Supported && item.ClashActive {
			return item, true
		}
	}
	for _, item := range c.Items {
		if item.Supported {
			return item, true
		}
	}
	return Subscription{}, false
}

func (c Catalog) RuntimeDefinitions() []map[string]any {
	result := []map[string]any{}
	for _, item := range c.Items {
		if !item.Supported {
			continue
		}
		for _, node := range item.Nodes {
			result = append(result, node.Definition)
		}
	}
	return result
}

func (c Catalog) EligibleRuntimeNames(preferredUID string) []string {
	preferred := []string{}
	fallback := []string{}
	for _, item := range c.Items {
		if !item.Supported {
			continue
		}
		for _, node := range item.Nodes {
			if !node.Eligible {
				continue
			}
			if item.UID == preferredUID {
				preferred = append(preferred, node.RuntimeName)
			} else {
				fallback = append(fallback, node.RuntimeName)
			}
		}
	}
	return append(preferred, fallback...)
}

func ReadSnapshot(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var value snapshot
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	return value.Fingerprint, nil
}

func WriteSnapshot(path, fingerprint string) error {
	data, err := json.MarshalIndent(snapshot{Fingerprint: fingerprint}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0o600)
}

func safeProfilePath(root, file string) (string, error) {
	if file == "" || filepath.Base(file) != file {
		return "", errors.New("订阅文件路径不安全")
	}
	ext := strings.ToLower(filepath.Ext(file))
	if ext != ".yaml" && ext != ".yml" {
		return "", errors.New("订阅文件扩展名不受支持")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(rootAbs, file)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("订阅文件必须是普通文件")
	}
	return path, nil
}

func enhancementPath(root, uid string) (string, error) {
	if uid == "" || !safeUID.MatchString(uid) {
		return "", errors.New("增强配置 UID 不安全")
	}
	return safeProfilePath(root, uid+".yaml")
}

func eligibleName(name string, regions []string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, token := range []string{"direct", "reject", "剩余", "流量", "到期", "官网", "公告", "套餐", "倍率", "电报", "订阅", "更新"} {
		if strings.Contains(lower, token) {
			return false
		}
	}
	if len(regions) == 0 {
		return true
	}
	for _, region := range regions {
		if region != "" && strings.Contains(lower, strings.ToLower(region)) {
			return true
		}
	}
	return false
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".inventory-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func Sort(items []Subscription) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
}
