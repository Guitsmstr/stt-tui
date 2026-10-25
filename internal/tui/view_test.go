package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"stt-tui/internal/remote"
	"stt-tui/internal/settings"
)

func testModel(w, h int) Model {
	m := New(settings.Options{NoADB: true})
	m.width = w
	m.height = h
	m.resize()
	m.focus = focusInput
	m.state = stateConnected
	m.settings = settings.Settings{
		WSURL:   "ws://127.0.0.1:4719/v1/feed",
		HTTPURL: "http://127.0.0.1:4719/healthz",
		Source:  "adb",
		Device:  "test-device",
		Token:   strings.Repeat("a", 64),
	}
	m.sessionID = "20261024-212959"
	m.engine = "sherpa"
	m.partials = true
	m.sessionActive = true
	m.healthSeen = true
	m.health = remote.Health{Status: "ok", Protocol: "stt-remote/1", Session: true}
	m.healthAt = time.Date(2026, 10, 24, 22, 10, 0, 0, time.Local)
	m.streaming = true
	m.eventCount = 3
	m.entries = []entry{
		{at: time.Date(2026, 10, 24, 22, 10, 1, 0, time.Local), app: true, typ: "app", text: "feed connected: 127.0.0.1:4719"},
		{at: time.Date(2026, 10, 24, 22, 10, 2, 0, time.Local), typ: "final", text: "Ever tried ever failed no matter try again", raw: `{"type":"final","text":"Ever tried ever failed no matter try again"}`, event: remote.Event{Type: "final", Text: "Ever tried ever failed no matter try again"}},
		{at: time.Date(2026, 10, 24, 22, 10, 3, 0, time.Local), typ: "partial", text: "fail better", event: remote.Event{Type: "partial", Text: "fail better"}},
	}
	m.rebuildOutput()
	return m
}

func TestViewShape(t *testing.T) {
	m := testModel(80, 24)
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24", len(lines))
	}
	for i, line := range lines[:3] {
		if ansi.StringWidth(line) != 80 {
			t.Errorf("art line %d width=%d, want 80", i, ansi.StringWidth(line))
		}
		if !strings.Contains(ansi.Strip(line), "█") && !strings.Contains(ansi.Strip(line), "▀") {
			t.Errorf("art line %d does not look like the wordmark: %q", i, ansi.Strip(line))
		}
	}
	// The box occupies rows 4..21 at 80x24 (art 3 + gap 1 + box 18).
	for i := 4; i < 22; i++ {
		if got := ansi.StringWidth(lines[i]); got != 80 {
			t.Errorf("box line %d width=%d, want 80: %q", i, got, ansi.Strip(lines[i]))
		}
	}
	if !strings.Contains(ansi.Strip(lines[4]), "╭") {
		t.Errorf("missing top border: %q", ansi.Strip(lines[4]))
	}
	if !strings.Contains(ansi.Strip(lines[21]), "╰") {
		t.Errorf("missing bottom border: %q", ansi.Strip(lines[21]))
	}
	if strings.TrimSpace(ansi.Strip(lines[22])) != "" || strings.TrimSpace(ansi.Strip(lines[23])) != "" {
		t.Errorf("bottom space is not blank")
	}
	plain := ansi.Strip(view)
	for _, want := range []string{"INFO & OPTIONS", "OUTPUT · LIVE FEED", "Ever tried ever failed", "ACTIONS", "Health check"} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func TestOnlyTranscriptFilter(t *testing.T) {
	m := testModel(80, 24)
	m.onlyTranscript = true
	m.rebuildOutput()
	lines := m.outputLines()
	for _, l := range lines {
		p := ansi.Strip(l.text)
		if strings.Contains(p, "feed connected") {
			t.Errorf("onlyTranscript leaked app line: %q", p)
		}
	}
	if len(lines) == 0 {
		t.Fatal("onlyTranscript produced no output")
	}

	m.onlyTranscript = false
	m.rawMode = true
	m.rebuildOutput()
	lines = m.outputLines()
	found := false
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l.text), `"type":"final"`) {
			found = true
		}
	}
	if !found {
		t.Error("raw mode did not show final JSON")
	}
}

func TestWrap(t *testing.T) {
	got := wrapPlain("one two three four five six", 10)
	want := []string{"one two", "three four", "five six"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrap = %#v, want %#v", got, want)
	}
	long := wrapPlain(strings.Repeat("x", 25), 10)
	if len(long) != 3 {
		t.Fatalf("long word wrap = %#v", long)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"192.0.2.1:4719":              "ws://192.0.2.1:4719/v1/feed",
		"http://192.0.2.1:4719/":      "ws://192.0.2.1:4719/v1/feed",
		"https://example.test/custom": "wss://example.test/custom",
		"ws://127.0.0.1:4719/v1/feed": "ws://127.0.0.1:4719/v1/feed",
	}
	for in, want := range cases {
		if got := settings.NormalizeWSURL(in); got != want {
			t.Errorf("NormalizeWSURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := settings.DeriveHealthURL("ws://127.0.0.1:4720/v1/feed"); got != "http://127.0.0.1:4720/healthz" {
		t.Errorf("DeriveHealthURL = %q", got)
	}
}

func TestCommandFeedbackSurvivesTranscriptFilter(t *testing.T) {
	m := testModel(100, 30)
	m.onlyTranscript = true
	m.rebuildOutput()
	before := len(m.entries)

	updated, _ := m.runCommand("listCommands")
	mm := updated.(Model)
	if len(mm.entries) <= before {
		t.Fatalf("listCommands added no entries")
	}
	if len(mm.outputLines()) == 0 {
		t.Fatalf("listCommands produced no visible output")
	}
	found := false
	for _, l := range mm.outputLines() {
		if strings.Contains(ansi.Strip(l.text), "commands:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("command help hidden by onlyTranscript")
	}

	updated, _ = mm.runCommand("status")
	mm = updated.(Model)
	found = false
	for _, l := range mm.outputLines() {
		if strings.Contains(ansi.Strip(l.text), "url:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("status output missing")
	}

	updated, _ = mm.runCommand("clear")
	mm = updated.(Model)
	if len(mm.entries) != 0 {
		t.Fatalf("clear left %d entries", len(mm.entries))
	}
}

func TestAudioToggleUpdatesState(t *testing.T) {
	m := testModel(100, 30)
	m.streaming = false
	updated, _ := m.runCommand("audio on")
	mm := updated.(Model)
	if !mm.streaming {
		t.Fatal("audio on did not start the test")
	}
	updated, _ = mm.runCommand("audio off")
	mm = updated.(Model)
	if mm.streaming {
		t.Fatal("audio off did not stop the test")
	}
}

func TestViewWidthsAcrossSizes(t *testing.T) {
	for _, sz := range [][2]int{{60, 17}, {80, 24}, {100, 30}, {110, 36}} {
		m := testModel(sz[0], sz[1])
		lines := strings.Split(m.View(), "\n")
		if len(lines) != sz[1] {
			t.Errorf("%dx%d: got %d lines", sz[0], sz[1], len(lines))
			continue
		}
		for i, line := range lines {
			if strings.TrimSpace(ansi.Strip(line)) == "" {
				continue
			}
			if got := ansi.StringWidth(line); got != sz[0] {
				t.Errorf("%dx%d line %d width=%d, want %d: %q", sz[0], sz[1], i, got, sz[0], ansi.Strip(line))
			}
		}
	}
}
