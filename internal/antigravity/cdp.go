package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type cdpTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func ResumeLatestError(ctx context.Context) (bool, error) {
	websocketURLs, err := discoverCDPs(ctx)
	if err != nil {
		return false, err
	}
	var lastErr error
	for _, websocketURL := range websocketURLs {
		clicked, evaluateErr := evaluateLatestError(ctx, websocketURL, true)
		if evaluateErr != nil {
			lastErr = evaluateErr
			continue
		}
		if clicked {
			return true, nil
		}
	}
	return false, lastErr
}

// HasLatestError inspects Antigravity's current page without changing UI state.
// Deferred recovery and success validation use it to distinguish a remaining
// error card from an idle conversation that recovered successfully.
func HasLatestError(ctx context.Context) (bool, error) {
	websocketURLs, err := discoverCDPs(ctx)
	if err != nil {
		return false, err
	}
	var lastErr error
	for _, websocketURL := range websocketURLs {
		visible, evaluateErr := evaluateLatestError(ctx, websocketURL, false)
		if evaluateErr != nil {
			lastErr = evaluateErr
			continue
		}
		if visible {
			return true, nil
		}
	}
	return false, lastErr
}

func evaluateLatestError(ctx context.Context, websocketURL string, click bool) (bool, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.DialContext(ctx, websocketURL, nil)
	if err != nil {
		return false, fmt.Errorf("connect CDP: %w", err)
	}
	defer conn.Close()
	expression := fmt.Sprintf(`(() => {
  const errorText = ['Agent terminated due to error', 'Agent execution terminated due to error'];
  const labels = new Set(['Retry', 'Try again', 'Continue']);
  const roots = [document];
  for (let i = 0; i < roots.length; i++) {
    for (const element of roots[i].querySelectorAll('*')) {
      if (element.shadowRoot) roots.push(element.shadowRoot);
    }
  }
  const buttons = roots.flatMap(root => [...root.querySelectorAll('button')]).reverse();
  for (const button of buttons) {
    if (!labels.has((button.textContent || '').trim()) || button.disabled) continue;
    let container = button.parentElement;
    for (let depth = 0; container && depth < 8; depth++, container = container.parentElement) {
      if (errorText.some(text => (container.textContent || '').includes(text))) {
        if (%t) button.click();
        return {clicked: true, label: (button.textContent || '').trim()};
      }
    }
  }
  return {clicked: false};
})()`, click)
	request := map[string]any{
		"id":     1,
		"method": "Runtime.evaluate",
		"params": map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true},
	}
	if err := conn.WriteJSON(request); err != nil {
		return false, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var response struct {
			ID     int `json:"id"`
			Result struct {
				Result struct {
					Value struct {
						Clicked bool `json:"clicked"`
					} `json:"value"`
				} `json:"result"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := conn.ReadJSON(&response); err != nil {
			return false, err
		}
		if response.ID != 1 {
			continue
		}
		if response.Error != nil {
			return false, fmt.Errorf("CDP Runtime.evaluate failed")
		}
		return response.Result.Result.Value.Clicked, nil
	}
}

// CheckCDP verifies that Antigravity exposes a background-inspectable page.
// It never evaluates script or changes application state.
func CheckCDP(ctx context.Context) error {
	_, err := discoverCDPs(ctx)
	return err
}

func discoverCDPs(ctx context.Context) ([]string, error) {
	pidsOutput, err := exec.CommandContext(ctx, "pgrep", "-f", `/Applications/Antigravity\.app/Contents/MacOS/Antigravity$`).Output()
	if err != nil {
		return nil, errors.New("Antigravity process not found")
	}
	pids := strings.Fields(string(pidsOutput))
	if len(pids) == 0 {
		return nil, errors.New("Antigravity process pid missing")
	}
	lsofOutput, err := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-p", pids[0], "-iTCP", "-sTCP:LISTEN").Output()
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	var websocketURLs []string
	for _, line := range strings.Split(string(lsofOutput), "\n") {
		match := portPattern.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		port, _ := strconv.Atoi(match[1])
		endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			continue
		}
		var targets []cdpTarget
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&targets)
		resp.Body.Close()
		if decodeErr != nil {
			continue
		}
		for _, target := range targets {
			if target.Type == "page" && target.WebSocketDebuggerURL != "" && !strings.HasPrefix(target.URL, "devtools://") {
				websocketURLs = append(websocketURLs, target.WebSocketDebuggerURL)
			}
		}
	}
	if len(websocketURLs) == 0 {
		return nil, errors.New("Antigravity CDP page target not found")
	}
	return websocketURLs, nil
}
