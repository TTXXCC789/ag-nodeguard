package logwatch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"ag-nodeguard/internal/model"
)

var timestampPattern = regexp.MustCompile(`^[^\[]*\[?(\d{4})[-/]?(\d{2})[-/]?(\d{2})[ T](\d{2}):(\d{2}):(\d{2})`)

func Classify(line string, now time.Time) (model.FailureEvent, bool) {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, "agent executor error") {
		return model.FailureEvent{}, false
	}
	switch {
	case strings.Contains(lower, "user location is not supported for the api use"):
		return event(model.FailureGeo, line, now), true
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "dns"), strings.Contains(lower, "name resolution"):
		return event(model.FailureDNS, line, now), true
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		return event(model.FailureTimeout, line, now), true
	case strings.Contains(lower, "unavailable"):
		return event(model.FailureUnavailable, line, now), true
	case strings.Contains(lower, "connection reset"), strings.Contains(lower, "connection closed"), strings.Contains(lower, "connection refused"), strings.Contains(lower, "transport is closing"), hasEOF(lower):
		return event(model.FailureConnection, line, now), true
	default:
		return model.FailureEvent{}, false
	}
}

// ClassifySuccess recognizes a real Agent streaming response in the backend
// language-server log. Model-list, updater and telemetry requests do not count.
func ClassifySuccess(line string, now time.Time) (model.SuccessEvent, bool) {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, "streamgeneratecontent") || !strings.Contains(lower, "responseid:") || !strings.Contains(lower, "daily-cloudcode-pa.googleapis.com") {
		return model.SuccessEvent{}, false
	}
	return model.SuccessEvent{OccurredAt: parseTime(line, now)}, true
}

func hasEOF(line string) bool {
	for _, field := range strings.FieldsFunc(line, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_')
	}) {
		if field == "eof" {
			return true
		}
	}
	return false
}

func event(kind model.FailureKind, line string, fallback time.Time) model.FailureEvent {
	occurred := parseTime(line, fallback)
	minute := occurred.UTC().Truncate(time.Minute).Format(time.RFC3339)
	// One executor failure is commonly rendered as several differently-worded
	// log lines. Kind+minute intentionally gives that episode one fingerprint.
	sum := sha256.Sum256([]byte(string(kind) + "|" + minute))
	return model.FailureEvent{Kind: kind, OccurredAt: occurred, Fingerprint: hex.EncodeToString(sum[:8])}
}

func parseTime(line string, fallback time.Time) time.Time {
	match := timestampPattern.FindStringSubmatch(line)
	if len(match) != 7 {
		return fallback
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", strings.Join(match[1:4], "-")+" "+strings.Join(match[4:7], ":"), time.Local)
	if err != nil {
		return fallback
	}
	return parsed
}

type Watcher struct {
	Path         string
	PollInterval time.Duration
	StartAtEnd   bool
}

func (w Watcher) Run(ctx context.Context, events chan<- model.FailureEvent, successes chan<- model.SuccessEvent) error {
	interval := w.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	var file *os.File
	var fileInfo os.FileInfo
	var offset int64
	var pending string
	initialOpen := true
	lastByKind := map[model.FailureKind]time.Time{}
	closeCurrent := func() {
		if file != nil {
			_ = file.Close()
		}
		file = nil
		fileInfo = nil
		offset = 0
		pending = ""
	}
	defer closeCurrent()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if file == nil {
			opened, err := os.Open(w.Path)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			} else {
				file = opened
				fileInfo, _ = opened.Stat()
				if w.StartAtEnd && initialOpen && fileInfo != nil {
					offset = fileInfo.Size()
					_, _ = file.Seek(offset, io.SeekStart)
				}
				initialOpen = false
			}
		}
		if file != nil {
			currentInfo, err := os.Stat(w.Path)
			if err == nil && fileInfo != nil && (!os.SameFile(fileInfo, currentInfo) || currentInfo.Size() < offset) {
				closeCurrent()
				continue
			}
			if err := readAvailable(file, &offset, &pending, func(line string) {
				e, ok := Classify(line, time.Now())
				if ok {
					if previous := lastByKind[e.Kind]; !previous.IsZero() && e.OccurredAt.Sub(previous) < 10*time.Second {
						return
					}
					lastByKind[e.Kind] = e.OccurredAt
					select {
					case events <- e:
					default:
					}
					return
				}
				if success, successOK := ClassifySuccess(line, time.Now()); successOK && successes != nil {
					select {
					case successes <- success:
					default:
					}
				}
			}); err != nil {
				closeCurrent()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func readAvailable(file *os.File, offset *int64, pending *string, consume func(string)) error {
	if _, err := file.Seek(*offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReader(file)
	for {
		chunk, err := reader.ReadString('\n')
		*offset += int64(len(chunk))
		if chunk != "" {
			*pending += chunk
			if strings.HasSuffix(*pending, "\n") {
				consume(strings.TrimRight(*pending, "\r\n"))
				*pending = ""
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
