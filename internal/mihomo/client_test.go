package mihomo

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ag-nodeguard/internal/appconfig"
)

func TestUnixSocketAPI(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "mihomo.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	selected := "日本01"
	closed := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"meta":true,"version":"v1.19.29"}`))
	})
	mux.HandleFunc("/proxies", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"proxies": map[string]any{
			"AntiGravity-Agent": map[string]any{"type": "Selector", "now": selected, "all": []string{"日本01", "新加坡01"}},
			"日本01":              map[string]any{"type": "Hysteria2"}, "新加坡01": map[string]any{"type": "VLESS"},
		}})
	})
	mux.HandleFunc("/group/AntiGravity-Agent/delay", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"日本01":95,"新加坡01":180}`))
	})
	mux.HandleFunc("/proxies/AntiGravity-Agent", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		selected = body["name"]
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/connections", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connections":[{"id":"abc","metadata":{"host":"daily-cloudcode-pa.googleapis.com","process":"language_server","processPath":"/Applications/Antigravity.app/Contents/Resources/bin/language_server"}}]}`))
	})
	mux.HandleFunc("/connections/abc", func(w http.ResponseWriter, _ *http.Request) { closed = "abc"; w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	cfg := appconfig.Default()
	cfg.MihomoSocket = socket
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	version, err := client.Version(ctx)
	if err != nil || CompatibleVersion(version.Version) != nil {
		t.Fatalf("version: %#v, %v", version, err)
	}
	delays, err := client.GroupDelay(ctx, cfg.ManagedGroup, cfg.LatencyURL, time.Second)
	if err != nil || delays["日本01"] != 95 {
		t.Fatalf("delays: %#v, %v", delays, err)
	}
	if err := client.Select(ctx, cfg.ManagedGroup, "新加坡01"); err != nil {
		t.Fatal(err)
	}
	proxies, _ := client.Proxies(ctx)
	if proxies[cfg.ManagedGroup].Now != "新加坡01" {
		t.Fatal("selection not confirmed by fake service")
	}
	connections, _ := client.Connections(ctx)
	if len(connections) != 1 || !IsAgentConnection(connections[0], cfg.ProcessPath) {
		t.Fatal("agent connection filter failed")
	}
	if err := client.CloseConnection(ctx, connections[0].ID); err != nil || closed != "abc" {
		t.Fatalf("close failed: %v, %q", err, closed)
	}
}

func TestFallbackRejectsLANAndMissingSecret(t *testing.T) {
	cfg := appconfig.Default()
	cfg.MihomoSocket = filepath.Join(t.TempDir(), "missing")
	cfg.ExternalController = "http://0.0.0.0:9090"
	cfg.ExternalSecret = "secret"
	if _, err := newTCPClient(cfg); err == nil {
		t.Fatal("LAN controller should be rejected")
	}
	cfg.ExternalController = "http://127.0.0.1:9090"
	cfg.ExternalSecret = ""
	if _, err := newTCPClient(cfg); err == nil {
		t.Fatal("controller without secret should be rejected")
	}
	_ = os.Remove(cfg.MihomoSocket)
}

func TestCompatibleVersion(t *testing.T) {
	if CompatibleVersion("v1.19.29") != nil || CompatibleVersion("Mihomo v1.20.0") != nil {
		t.Fatal("expected supported version")
	}
	if CompatibleVersion("v1.18.9") == nil || CompatibleVersion("v2.0.0") == nil {
		t.Fatal("expected incompatible version")
	}
}

func TestAPICallHonorsContextTimeout(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agng-timeout-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "slow.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})}
	go server.Serve(listener)
	defer server.Close()
	cfg := appconfig.Default()
	cfg.MihomoSocket = socket
	client, _ := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Proxies(ctx); err == nil {
		t.Fatal("expected context timeout")
	}
}

func TestAgentConnectionRequiresGoogleHost(t *testing.T) {
	connection := Connection{Metadata: ConnectionMetadata{Process: "language_server"}}
	if IsAgentConnection(connection, "/Applications/Antigravity.app/Contents/Resources/bin/language_server") {
		t.Fatal("hostless connection must not be closed")
	}
	connection.Metadata.Host = "antigravity-unleash.goog"
	if !IsAgentConnection(connection, "/Applications/Antigravity.app/Contents/Resources/bin/language_server") {
		t.Fatal("Google .goog connection should be closed after a node switch")
	}
	connection.Metadata.Host = "example.com"
	if IsAgentConnection(connection, "/Applications/Antigravity.app/Contents/Resources/bin/language_server") {
		t.Fatal("non-Google connection must not be closed")
	}
}
