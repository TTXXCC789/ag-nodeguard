package antigravity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	HealthPort int
	RPCPort    int
	CSRFToken  string
	HTTP       *http.Client
	HTTPS      *http.Client
}

type Health struct {
	InstanceID string `json:"instanceId"`
	Status     string `json:"status"`
}

type Conversation struct {
	CascadeID         string
	Status            string
	LastUserInputTime time.Time
	LastInputIndex    int
}

func (c Conversation) CompletedSuccessfully() bool {
	status := strings.ToLower(c.Status)
	return strings.Contains(status, "success") || strings.Contains(status, "complete") || strings.HasSuffix(status, "_idle")
}

func (c Conversation) IsRunning() bool {
	status := strings.ToLower(c.Status)
	return strings.Contains(status, "running") || strings.Contains(status, "busy")
}

var (
	portPattern = regexp.MustCompile(`127\.0\.0\.1:(\d+) \(LISTEN\)`)
	csrfPattern = regexp.MustCompile(`--csrf_token\s+([^\s]+)`)
)

func Discover(ctx context.Context) (*Client, error) {
	pidsOutput, err := exec.CommandContext(ctx, "pgrep", "-f", `/Applications/Antigravity\.app/Contents/Resources/bin/language_server --standalone`).Output()
	if err != nil {
		return nil, errors.New("Antigravity language_server is not running")
	}
	pids := strings.Fields(string(pidsOutput))
	if len(pids) == 0 {
		return nil, errors.New("Antigravity language_server pid not found")
	}
	pid := pids[0]
	commandOutput, err := exec.CommandContext(ctx, "ps", "-p", pid, "-o", "command=").Output()
	if err != nil {
		return nil, fmt.Errorf("read language_server command: %w", err)
	}
	match := csrfPattern.FindStringSubmatch(string(commandOutput))
	if len(match) != 2 {
		return nil, errors.New("Antigravity CSRF token not found")
	}
	lsofOutput, err := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-p", pid, "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		return nil, fmt.Errorf("discover language_server ports: %w", err)
	}
	seen := map[int]bool{}
	ports := []int{}
	for _, portMatch := range portPattern.FindAllStringSubmatch(string(lsofOutput), -1) {
		port, _ := strconv.Atoi(portMatch[1])
		if port > 0 && !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	if len(ports) < 2 {
		return nil, fmt.Errorf("expected two language_server ports, found %d", len(ports))
	}
	plain := &http.Client{Timeout: 2 * time.Second}
	insecureTransport := http.DefaultTransport.(*http.Transport).Clone()
	insecureTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // loopback-only self-signed Antigravity listener
	secure := &http.Client{Transport: insecureTransport, Timeout: 5 * time.Second}
	client := &Client{CSRFToken: match[1], HTTP: plain, HTTPS: secure}
	for _, port := range ports {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), nil)
		resp, requestErr := plain.Do(request)
		if requestErr == nil {
			var health Health
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health)
			resp.Body.Close()
			if decodeErr == nil && health.Status == "ok" {
				client.HealthPort = port
				continue
			}
		}
		client.RPCPort = port
	}
	if client.HealthPort == 0 || client.RPCPort == 0 {
		return nil, errors.New("could not distinguish Antigravity health and RPC ports")
	}
	return client, nil
}

func (c *Client) CheckHealth(ctx context.Context) (Health, error) {
	var result Health
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/healthz", c.HealthPort), nil)
	if err != nil {
		return result, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("healthz status %d", resp.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result)
	if err == nil && result.Status != "ok" {
		err = fmt.Errorf("healthz reported %q", result.Status)
	}
	return result, err
}

func (c *Client) LatestConversation(ctx context.Context) (Conversation, error) {
	endpoint := fmt.Sprintf("https://127.0.0.1:%d/exa.language_server_pb.LanguageServerService/GetAllCascadeTrajectories", c.RPCPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString("{}"))
	if err != nil {
		return Conversation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-codeium-csrf-token", c.CSRFToken)
	resp, err := c.HTTPS.Do(req)
	if err != nil {
		return Conversation{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Conversation{}, fmt.Errorf("conversation RPC status %d", resp.StatusCode)
	}
	var payload struct {
		TrajectorySummaries map[string]struct {
			Status                 string `json:"status"`
			LastUserInputTime      string `json:"lastUserInputTime"`
			LastUserInputStepIndex int    `json:"lastUserInputStepIndex"`
		} `json:"trajectorySummaries"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&payload); err != nil {
		return Conversation{}, err
	}
	latest := Conversation{}
	for id, summary := range payload.TrajectorySummaries {
		when, err := time.Parse(time.RFC3339Nano, summary.LastUserInputTime)
		if err != nil {
			continue
		}
		if latest.CascadeID == "" || when.After(latest.LastUserInputTime) {
			latest = Conversation{CascadeID: id, Status: summary.Status, LastUserInputTime: when, LastInputIndex: summary.LastUserInputStepIndex}
		}
	}
	if latest.CascadeID == "" {
		return Conversation{}, errors.New("no Antigravity conversation found")
	}
	return latest, nil
}
