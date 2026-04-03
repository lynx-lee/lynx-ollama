package service

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lynxlee/lynx-ollama-console/internal/model"
)

// InferenceTracker parses Ollama container GIN logs to extract inference events.
// It runs `docker logs --since=<last> --timestamps ollama` periodically.
type InferenceTracker struct {
	mu     sync.RWMutex
	events []model.InferEvent // ring buffer
	maxLen int

	// Console container IP for client identification
	consoleIP string

	lastSeen time.Time // last parsed log timestamp
	stopCh   chan struct{}
}

// NewInferenceTracker creates a new tracker with ring buffer of given size.
func NewInferenceTracker(maxEvents int) *InferenceTracker {
	return &InferenceTracker{
		events:   make([]model.InferEvent, 0, maxEvents),
		maxLen:   maxEvents,
		lastSeen: time.Now().Add(-10 * time.Minute), // look back 10 min on start
		stopCh:   make(chan struct{}),
	}
}

// Start begins periodic log polling.
func (t *InferenceTracker) Start(interval time.Duration) {
	// Detect console container IP
	t.consoleIP = detectConsoleIP()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		t.poll() // immediate first poll
		for {
			select {
			case <-ticker.C:
				t.poll()
			case <-t.stopCh:
				return
			}
		}
	}()
}

// Stop stops the tracker.
func (t *InferenceTracker) Stop() {
	close(t.stopCh)
}

// GetEvents returns recent inference events within the given time window.
func (t *InferenceTracker) GetEvents(windowSec int64) []model.InferEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()

	cutoff := time.Now().Unix() - windowSec
	var result []model.InferEvent
	for _, e := range t.events {
		if e.Timestamp >= cutoff {
			result = append(result, e)
		}
	}
	return result
}

// GetRecentEvents returns the last N events.
func (t *InferenceTracker) GetRecentEvents(n int) []model.InferEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if n > len(t.events) {
		n = len(t.events)
	}
	result := make([]model.InferEvent, n)
	copy(result, t.events[len(t.events)-n:])
	return result
}

// GIN log line pattern:
// [GIN] 2026/04/03 - 20:28:16 | 200 | 13.464760955s | 172.18.0.3 | POST "/api/chat"
var ginLogRe = regexp.MustCompile(
	`\[GIN\]\s+(\d{4}/\d{2}/\d{2}\s+-\s+\d{2}:\d{2}:\d{2})\s+\|\s+(\d+)\s+\|\s+([\d.]+(?:ns|µs|ms|s|m|h)?[\dsmhµn.]*)\s+\|\s+([\d.]+)\s+\|\s+(\w+)\s+"([^"]+)"`,
)

func (t *InferenceTracker) poll() {
	since := t.lastSeen.Format(time.RFC3339)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "logs", "--since", since, "--timestamps", "ollama")
	cmd.Stderr = cmd.Stdout // GIN logs go to stderr in Docker
	output, err := cmd.CombinedOutput()
	if err != nil {
		return
	}

	var newEvents []model.InferEvent
	var latestTS time.Time

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()

		// Only parse inference-related GIN lines
		if !strings.Contains(line, "[GIN]") {
			continue
		}
		if !strings.Contains(line, "/api/chat") &&
			!strings.Contains(line, "/v1/chat/completions") &&
			!strings.Contains(line, "/api/generate") {
			continue
		}

		matches := ginLogRe.FindStringSubmatch(line)
		if len(matches) < 7 {
			continue
		}

		ginTime := matches[1]     // "2026/04/03 - 20:28:16"
		status := matches[2]      // "200"
		duration := matches[3]    // "13.464760955s"
		clientIP := matches[4]    // "172.18.0.3"
		method := matches[5]      // "POST"
		path := matches[6]        // "/api/chat"

		// Parse timestamp from Docker log prefix (ISO format at line start)
		var logTS time.Time
		if len(line) > 30 && line[0] >= '0' && line[0] <= '9' {
			// Docker --timestamps prefix: "2026-04-03T12:28:16.324811006Z"
			if idx := strings.Index(line, " "); idx > 20 {
				logTS, _ = time.Parse(time.RFC3339Nano, line[:idx])
			}
		}
		if logTS.IsZero() {
			// Fallback: parse GIN timestamp
			logTS, _ = time.Parse("2006/01/02 - 15:04:05", ginTime)
		}
		if logTS.After(latestTS) {
			latestTS = logTS
		}

		statusCode, _ := strconv.Atoi(status)
		durationMs := parseDurationToMs(duration)

		event := model.InferEvent{
			Timestamp:  logTS.Unix(),
			ClientIP:   clientIP,
			ClientName: t.resolveClient(clientIP),
			Method:     method,
			Path:       path,
			Status:     statusCode,
			DurationMs: durationMs,
		}

		newEvents = append(newEvents, event)
	}

	if len(newEvents) > 0 {
		t.mu.Lock()
		t.events = append(t.events, newEvents...)
		// Trim ring buffer
		if len(t.events) > t.maxLen {
			t.events = t.events[len(t.events)-t.maxLen:]
		}
		t.mu.Unlock()
	}

	if !latestTS.IsZero() {
		t.lastSeen = latestTS.Add(time.Millisecond) // avoid re-parsing same line
	}
}

// resolveClient maps Docker network IPs to human-readable names.
func (t *InferenceTracker) resolveClient(ip string) string {
	if ip == t.consoleIP {
		return "console"
	}
	// Docker gateway (x.x.x.1) = external client via port mapping
	parts := strings.Split(ip, ".")
	if len(parts) == 4 && parts[3] == "1" {
		return "external"
	}
	return ip
}

// parseDurationToMs converts GIN duration strings to milliseconds.
// Formats: "13.464760955s", "166.964656ms", "103.346µs", "4m28s", "1m0s"
func parseDurationToMs(s string) int64 {
	s = strings.TrimSpace(s)

	// Go's time.ParseDuration handles most cases
	d, err := time.ParseDuration(s)
	if err == nil {
		return d.Milliseconds()
	}

	// Handle edge cases like "4m28s" → already handled by ParseDuration
	// Handle "5m12s" style
	var total float64
	if strings.Contains(s, "m") && strings.Contains(s, "s") {
		// e.g., "4m28s"
		parts := strings.SplitN(s, "m", 2)
		if len(parts) == 2 {
			min, _ := strconv.ParseFloat(parts[0], 64)
			sec, _ := strconv.ParseFloat(strings.TrimSuffix(parts[1], "s"), 64)
			total = (min*60 + sec) * 1000
			return int64(total)
		}
	}

	// Last resort: try as seconds
	fmt.Sscanf(s, "%f", &total)
	return int64(total * 1000)
}

// detectConsoleIP tries to find this container's IP on the Docker network.
func detectConsoleIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try hostname -i (works inside Docker containers)
	out, err := exec.CommandContext(ctx, "hostname", "-i").Output()
	if err == nil {
		ip := strings.TrimSpace(string(out))
		if ip != "" && ip != "127.0.0.1" {
			// May return multiple IPs, take first
			if parts := strings.Fields(ip); len(parts) > 0 {
				return parts[0]
			}
		}
	}

	slog.Info("inference tracker: could not detect console IP, using default")
	return "172.18.0.3" // common Docker Compose default
}
