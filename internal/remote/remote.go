// Package remote contains the small network surface used by the TUI:
// health probing and a WebSocket dialer for the stt-remote/1 feed.
package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Health mirrors GET /healthz (CONTRATOS §9.1).
type Health struct {
	Status   string `json:"status"`
	Protocol string `json:"protocol"`
	Session  bool   `json:"session"`
}

// CheckHealth performs GET /healthz with a short timeout.
func CheckHealth(ctx context.Context, healthURL string) (Health, error) {
	var h Health
	if healthURL == "" {
		return h, fmt.Errorf("health URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return h, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return h, err
	}
	if resp.StatusCode != http.StatusOK {
		return h, fmt.Errorf("health: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return h, fmt.Errorf("health: bad JSON: %w", err)
	}
	if h.Status == "" {
		h.Status = "ok"
	}
	return h, nil
}

// DialError reports a failed WebSocket handshake. Status is non-zero when the
// server answered with an HTTP error before the upgrade (401/503).
type DialError struct {
	Status int
	Err    error
}

func (e *DialError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("WebSocket handshake failed: HTTP %d", e.Status)
	}
	return fmt.Sprintf("WebSocket dial: %v", e.Err)
}

func (e *DialError) Unwrap() error { return e.Err }

// Dial connects to the stt-remote/1 feed with bearer authentication.
func Dial(ctx context.Context, wsURL, token string) (*websocket.Conn, error) {
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	d := websocket.Dialer{
		HandshakeTimeout: 8 * time.Second,
		ReadBufferSize:   8 << 10,
		WriteBufferSize:  8 << 10,
	}
	conn, resp, err := d.DialContext(ctx, wsURL, header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		return nil, &DialError{Status: status, Err: err}
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

// Event is the subset of stt-events/1 + stt-remote/1 that the TUI formats.
// Unknown fields are ignored by contract (CONTRATOS §3); the raw message is
// kept separately for the raw view.
type Event struct {
	Type string `json:"type"`

	// stt-events/1
	Text   string   `json:"text"`
	T0     *float64 `json:"t0"`
	T1     *float64 `json:"t1"`
	Model  string   `json:"model"`
	RTF    *float64 `json:"rtf"`
	Frame  *int     `json:"frame"`
	Frames *int     `json:"frames"`
	AudioS *float64 `json:"audio_s"`
	// skipped_s / rss_kb are diagnostics added over time.
	SkippedS *float64 `json:"skipped_s"`
	RSSKB    *int64   `json:"rss_kb"`

	// error
	Code    string `json:"code"`
	Message string `json:"message"`
	Fatal   *bool  `json:"fatal"`

	// hello (stt-remote/1)
	Protocol string        `json:"protocol"`
	Server   string        `json:"server"`
	Session  *HelloSession `json:"session"`
	Audio    *Audio        `json:"audio"`
}

type HelloSession struct {
	ID        string `json:"id"`
	Engine    string `json:"engine"`
	Partials  bool   `json:"partials"`
	StartedAt string `json:"started_at"`
}

type Audio struct {
	Rate     int    `json:"rate"`
	Channels int    `json:"channels"`
	Format   string `json:"format"`
}

// ParseEvent decodes one text frame. It is deliberately permissive: an
// unparseable/unknown event is still surfaced by the caller.
func ParseEvent(data []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}
