package logwatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ag-nodeguard/internal/model"
)

func TestClassifyOnlyAgentExecutorFailures(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.Local)
	tests := []struct {
		line string
		kind model.FailureKind
		ok   bool
	}{
		{"agent executor error: FAILED_PRECONDITION: User location is not supported for the API use", model.FailureGeo, true},
		{"agent executor error: rpc error: code = Unavailable", model.FailureUnavailable, true},
		{"agent executor error: dial tcp: lookup api.example: no such host", model.FailureDNS, true},
		{"agent executor error: context deadline exceeded", model.FailureTimeout, true},
		{"agent executor error: connection reset by peer", model.FailureConnection, true},
		{`2026-08-16 15:56:33 agent executor error: model unreachable: request failed: Post "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse": EOF`, model.FailureConnection, true},
		{"agent executor error: model unreachable because the selected model is disabled", "", false},
		{"updater error: connection reset by peer", "", false},
		{`background cache refresh: request failed: Get "https://daily-cloudcode-pa.googleapis.com": EOF`, "", false},
		{"quota exceeded", "", false},
		{"authentication required", "", false},
		{"User location is not supported for the API use", "", false},
	}
	for _, test := range tests {
		event, ok := Classify(test.line, now)
		if ok != test.ok || event.Kind != test.kind {
			t.Fatalf("Classify(%q) = (%q, %t), want (%q, %t)", test.line, event.Kind, ok, test.kind, test.ok)
		}
	}
}

func TestMultilineFailureDeduplicatesByKindAndMinute(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.Local)
	first, _ := Classify("agent executor error: unavailable: transport closed", now)
	second, _ := Classify("agent executor error: UNAVAILABLE: a different rendering", now.Add(20*time.Second))
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprints differ: %s != %s", first.Fingerprint, second.Fingerprint)
	}
}

func TestClassifySuccessOnlyAgentStreamingResponse(t *testing.T) {
	now := time.Now()
	if _, ok := ClassifySuccess(`I0816 16:16:47 URL: https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse Trace: 0x1 ResponseID: abc`, now); !ok {
		t.Fatal("streaming ResponseID should be a backend success signal")
	}
	for _, line := range []string{
		`URL: https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels ResponseID: abc`,
		`URL: https://example.com/v1internal:streamGenerateContent ResponseID: abc`,
		`streamGenerateContent started`,
	} {
		if _, ok := ClassifySuccess(line, now); ok {
			t.Fatalf("unexpected success classification: %s", line)
		}
	}
}

func TestWatcherHandlesRotationFromBeginning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "language_server.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan model.FailureEvent, 2)
	watcher := Watcher{Path: path, StartAtEnd: true, PollInterval: 10 * time.Millisecond}
	go func() { _ = watcher.Run(ctx, events, nil) }()
	time.Sleep(40 * time.Millisecond)
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	line := "agent executor error: connection reset by peer\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Kind != model.FailureConnection {
			t.Fatalf("kind = %q", event.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rotated log event")
	}
}
