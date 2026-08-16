package mihomo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ag-nodeguard/internal/appconfig"
)

type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

type Version struct {
	Meta    bool   `json:"meta"`
	Version string `json:"version"`
}

type DelayHistory struct {
	Delay int `json:"delay"`
}

type Proxy struct {
	Type    string         `json:"type"`
	Now     string         `json:"now"`
	All     []string       `json:"all"`
	Alive   *bool          `json:"alive,omitempty"`
	History []DelayHistory `json:"history,omitempty"`
	TestURL string         `json:"testUrl,omitempty"`
}

type ProxiesResponse struct {
	Proxies map[string]Proxy `json:"proxies"`
}

type ConnectionMetadata struct {
	Host          string `json:"host"`
	DestinationIP string `json:"destinationIP"`
	Process       string `json:"process"`
	ProcessPath   string `json:"processPath"`
}

type Connection struct {
	ID       string             `json:"id"`
	Metadata ConnectionMetadata `json:"metadata"`
	Chains   []string           `json:"chains"`
	Rule     string             `json:"rule"`
}

type ConnectionsResponse struct {
	Connections []Connection `json:"connections"`
}

var (
	socketArgPattern = regexp.MustCompile(`(?:-ext-ctl-unix|--external-controller-unix|external-controller-unix)[=\s]+([^\s]+)`)
	versionPattern   = regexp.MustCompile(`(?i)(?:mihomo\s+)?v?(\d+)\.(\d+)\.(\d+)`)
)

func New(cfg appconfig.Config) (*Client, error) {
	socketPath := cfg.MihomoSocket
	if info, err := os.Stat(socketPath); err != nil || info.Mode()&os.ModeSocket == 0 {
		discoveryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		discovered, discoverErr := discoverSocket(discoveryCtx)
		cancel()
		if discoverErr == nil {
			socketPath = discovered
		}
	}
	if socketPath != "" {
		if info, err := os.Stat(socketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
			dialer := &net.Dialer{Timeout: 3 * time.Second}
			transport := &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			}
			return &Client{baseURL: "http://unix", http: &http.Client{Transport: transport, Timeout: 40 * time.Second}}, nil
		}
	}
	if cfg.ExternalController == "" {
		return nil, fmt.Errorf("mihomo unix socket unavailable: %s", cfg.MihomoSocket)
	}
	return newTCPClient(cfg)
}

func newTCPClient(cfg appconfig.Config) (*Client, error) {
	parsed, err := url.Parse(cfg.ExternalController)
	if err != nil {
		return nil, fmt.Errorf("parse external controller: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("external controller must use http or https")
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return nil, errors.New("external controller must use a loopback address")
	}
	if cfg.ExternalSecret == "" {
		return nil, errors.New("external controller requires a secret")
	}
	return &Client{baseURL: strings.TrimRight(cfg.ExternalController, "/"), secret: cfg.ExternalSecret, http: &http.Client{Timeout: 40 * time.Second}}, nil
}

func discoverSocket(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "pgrep", "-f", "verge-mihomo").Output()
	if err != nil {
		return "", err
	}
	for _, pid := range strings.Fields(string(output)) {
		command, commandErr := exec.CommandContext(ctx, "ps", "-p", pid, "-o", "command=").Output()
		if commandErr != nil {
			continue
		}
		match := socketArgPattern.FindStringSubmatch(string(command))
		if len(match) == 2 {
			path := strings.Trim(match[1], `"'`)
			if info, statErr := os.Stat(path); statErr == nil && info.Mode()&os.ModeSocket != 0 {
				return path, nil
			}
		}
	}
	return "", errors.New("mihomo Unix Socket argument not found")
}

func CompatibleVersion(value string) error {
	match := versionPattern.FindStringSubmatch(value)
	if len(match) != 4 {
		return fmt.Errorf("unrecognized Mihomo version %q", value)
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major != 1 || minor < 19 {
		return fmt.Errorf("unsupported Mihomo version %q; require 1.19 or newer 1.x", value)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 16<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(limited, 4096))
		return fmt.Errorf("mihomo %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, limited)
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("decode mihomo response: %w", err)
	}
	return nil
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	var result Version
	err := c.request(ctx, http.MethodGet, "/version", nil, &result)
	return result, err
}

func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) {
	var result ProxiesResponse
	err := c.request(ctx, http.MethodGet, "/proxies", nil, &result)
	return result.Proxies, err
}

func (c *Client) GroupDelay(ctx context.Context, group, target string, timeout time.Duration) (map[string]int, error) {
	path := "/group/" + url.PathEscape(group) + "/delay?url=" + url.QueryEscape(target) + "&timeout=" + fmt.Sprintf("%d", timeout.Milliseconds())
	result := map[string]int{}
	err := c.request(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (c *Client) Select(ctx context.Context, group, name string) error {
	return c.request(ctx, http.MethodPut, "/proxies/"+url.PathEscape(group), map[string]string{"name": name}, nil)
}

func (c *Client) Connections(ctx context.Context) ([]Connection, error) {
	var result ConnectionsResponse
	err := c.request(ctx, http.MethodGet, "/connections", nil, &result)
	return result.Connections, err
}

func (c *Client) CloseConnection(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("connection id is empty")
	}
	return c.request(ctx, http.MethodDelete, "/connections/"+url.PathEscape(id), nil, nil)
}

func (c *Client) ReloadConfig(ctx context.Context, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPut, "/configs?force=true", map[string]string{"path": abs, "payload": ""}, nil)
}

func IsLeaf(proxy Proxy) bool {
	switch strings.ToLower(proxy.Type) {
	case "selector", "urltest", "fallback", "loadbalance", "relay", "direct", "reject", "rejectdrop", "pass":
		return false
	default:
		return len(proxy.All) == 0
	}
}

func IsAgentConnection(c Connection, processPath string) bool {
	cleanExpected := filepath.Clean(processPath)
	cleanActual := filepath.Clean(c.Metadata.ProcessPath)
	processMatches := cleanActual == cleanExpected || strings.EqualFold(c.Metadata.Process, "language_server") || strings.EqualFold(filepath.Base(cleanActual), "language_server")
	if !processMatches {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(c.Metadata.Host, "."))
	if host == "" {
		return false
	}
	for _, suffix := range []string{"googleapis.com", "google.com", "gstatic.com", "goog"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}
