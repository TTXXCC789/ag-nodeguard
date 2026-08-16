package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ag-nodeguard/internal/guard"
)

func TestStreamStatusOutputsNDJSONAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var output bytes.Buffer
	when := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	err := streamStatus(ctx, time.Millisecond, func(context.Context) (guard.Status, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return guard.Status{ObservedAt: when, CurrentNode: "日本01", DaemonRunning: true}, nil
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), output.String())
	}
	for _, line := range lines {
		var status guard.Status
		if err := json.Unmarshal([]byte(line), &status); err != nil {
			t.Fatal(err)
		}
		if status.CurrentNode != "日本01" || !status.DaemonRunning {
			t.Fatalf("unexpected status: %#v", status)
		}
	}
}

func TestStreamStatusReturnsFetchError(t *testing.T) {
	want := context.DeadlineExceeded
	err := streamStatus(context.Background(), time.Second, func(context.Context) (guard.Status, error) {
		return guard.Status{}, want
	}, &bytes.Buffer{})
	if err != want {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
