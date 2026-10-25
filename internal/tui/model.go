package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"

	"stt-tui/internal/remote"
	"stt-tui/internal/settings"
)

const (
	artHeight   = 3
	bottomSpace = 2
	minWidth    = 60
	minHeight   = 17
	maxEntries  = 3000
)

type focusArea int

const (
	focusInput focusArea = iota
	focusMenu
)

type connState int

const (
	stateDiscovering connState = iota
	stateIdle
	stateConnecting
	stateConnected
	stateReconnecting
	stateAuthFailed
	stateError
)

type action int

const (
	actHealth action = iota
	actAudio
	actTranscript
	actClear
)

// entry is one line-producing item in the output column.
type entry struct {
	at     time.Time
	app    bool // app/system message, not server output
	always bool // command feedback stays visible in onlyTranscript mode
	typ    string
	text   string
	raw    string
	event  remote.Event
}

type Model struct {
	opts settings.Options

	width  int
	height int

	leftW    int
	rightW   int
	contentH int
	artGap   int

	input  textinput.Model
	output viewport.Model

	focus     focusArea
	menuIndex int

	settings settings.Settings

	conn           *websocket.Conn
	gen            int
	wantConn       bool
	connecting     bool
	reconnectAt    time.Time
	reconnectArmed bool
	backoff        time.Duration
	state          connState
	lastConnErr    string

	health     remote.Health
	healthAt   time.Time
	healthErr  string
	healthSeen bool

	sessionID     string
	engine        string
	partials      bool
	sessionActive bool
	sessionStart  string

	streaming      bool
	onlyTranscript bool
	rawMode        bool
	follow         bool
	unseen         int

	entries     []entry
	entryLines  int
	eventCount  int
	lastEventAt time.Time
}

func New(opts settings.Options) Model {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.Placeholder = "type a command… (listCommands)"
	ti.CharLimit = 4096
	ti.Focus()

	vp := viewport.New(48, 12)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3

	m := Model{
		opts:      opts,
		width:     80,
		height:    24,
		input:     ti,
		output:    vp,
		focus:     focusInput,
		menuIndex: 0,
		gen:       1,
		backoff:   time.Second,
		follow:    true,
		state:     stateDiscovering,
		settings: settings.Settings{
			WSURL:   settings.DefaultWSURL,
			HTTPURL: settings.DefaultHTTPURL,
			Source:  "default",
		},
	}
	m.resize()
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.discoverCmd(),
	)
}

// ---- messages -------------------------------------------------------------

type discoveredMsg struct{ res settings.Result }

type healthMsg struct {
	h      remote.Health
	err    error
	at     time.Time
	manual bool
	url    string
}

type healthTickMsg struct{}

type wsConnectedMsg struct {
	conn *websocket.Conn
	gen  int
	at   time.Time
}

type wsConnectErrMsg struct {
	err    error
	status int
	gen    int
	at     time.Time
}

type wsEventMsg struct {
	conn *websocket.Conn
	gen  int
	at   time.Time
	data []byte
}

type wsClosedMsg struct {
	conn   *websocket.Conn
	gen    int
	err    error
	code   int
	reason string
	at     time.Time
}

type reconnectTickMsg struct{ gen int }

type saveConfigMsg struct {
	path string
	err  error
}

// ---- update ---------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case discoveredMsg:
		return m.onDiscovered(msg)

	case healthMsg:
		return m.onHealth(msg)

	case healthTickMsg:
		return m, tea.Batch(m.healthCmd(false), m.healthTickCmd())

	case wsConnectedMsg:
		if msg.gen != m.gen || !m.wantConn {
			if msg.conn != nil {
				_ = msg.conn.Close()
			}
			return m, nil
		}
		m.conn = msg.conn
		m.connecting = false
		m.reconnectArmed = false
		m.reconnectAt = time.Time{}
		m.state = stateConnected
		m.backoff = time.Second
		m.lastConnErr = ""
		m.appendApp("feed connected: " + m.settings.Endpoint())
		return m, waitWS(msg.conn, msg.gen)

	case wsConnectErrMsg:
		if msg.gen != m.gen || !m.wantConn {
			return m, nil
		}
		m.connecting = false
		m.lastConnErr = msg.err.Error()
		if msg.status == 401 {
			m.state = stateAuthFailed
			m.appendAppAlways("WebSocket 401 unauthorized — token missing or wrong")
			m.appendAppAlways("hint: use `token <token>` or set STT_TOKEN, then `reconnect`")
			return m, nil
		}
		m.state = stateError
		if msg.status == 503 {
			m.appendAppAlways("WebSocket 503: server is at max_clients; will retry")
		} else {
			m.appendAppAlways("feed error: " + msg.err.Error())
		}
		cmd := m.scheduleReconnect()
		return m, cmd

	case wsEventMsg:
		if msg.conn != m.conn || msg.gen != m.gen {
			return m, nil
		}
		m.onWSEvent(msg.data, msg.at)
		return m, waitWS(msg.conn, msg.gen)

	case wsClosedMsg:
		if msg.conn != m.conn || msg.gen != m.gen {
			return m, nil
		}
		m.conn = nil
		m.connecting = false
		m.sessionActive = false
		if m.wantConn {
			m.state = stateReconnecting
		} else {
			m.state = stateIdle
		}
		if msg.code == 1000 {
			m.appendApp("feed closed by server (session ended); reconnecting…")
		} else if msg.err != nil {
			m.appendApp(fmt.Sprintf("feed closed: %v", msg.err))
		} else {
			m.appendApp(fmt.Sprintf("feed closed (code %d)", msg.code))
		}
		cmd := m.scheduleReconnect()
		return m, cmd

	case reconnectTickMsg:
		if msg.gen != m.gen || !m.wantConn || m.conn != nil || m.connecting {
			return m, nil
		}
		m.reconnectArmed = false
		m.reconnectAt = time.Time{}
		cmd := m.startConnect()
		return m, cmd

	case saveConfigMsg:
		if msg.err != nil {
			m.appendAppAlways("save failed: " + msg.err.Error())
		} else {
			m.appendAppAlways("config saved to " + msg.path)
		}
		return m, nil
	}

	if m.focus == focusInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) onDiscovered(msg discoveredMsg) (tea.Model, tea.Cmd) {
	m.settings = msg.res.Settings
	for _, n := range msg.res.Notes {
		m.appendApp(n)
	}
	m.wantConn = true
	m.state = stateIdle
	m.resize()

	cmds := []tea.Cmd{m.healthCmd(false), m.healthTickCmd()}
	if c := m.startConnect(); c != nil {
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

func (m Model) onHealth(msg healthMsg) (tea.Model, tea.Cmd) {
	if msg.url != "" && msg.url != m.settings.HTTPURL {
		return m, nil // stale response from a previous endpoint
	}
	wasErr := m.healthErr
	m.health = msg.h
	m.healthAt = msg.at
	m.healthSeen = true
	if msg.err != nil {
		m.healthErr = msg.err.Error()
	} else {
		m.healthErr = ""
		m.sessionActive = msg.h.Session
	}
	if msg.manual {
		if msg.err != nil {
			m.appendAppAlways("health error: " + msg.err.Error())
		} else {
			m.appendAppAlways(fmt.Sprintf("health ok · %s · session=%v", msg.h.Protocol, msg.h.Session))
		}
	} else if msg.err != nil && wasErr == "" {
		m.appendApp("health check failed: " + msg.err.Error())
	}
	return m, nil
}

func (m *Model) onWSEvent(data []byte, at time.Time) {
	raw := string(data)
	ev, err := remote.ParseEvent(data)
	if err != nil {
		m.appendEntry(entry{
			at:   at,
			typ:  "raw",
			text: "unparseable frame",
			raw:  raw,
		})
		return
	}

	transcript := ev.Type == "final" || ev.Type == "partial"
	if transcript && !m.streaming {
		// The feed stays connected (keepalive/reconnect), but the transcript
		// view is paused. Do not log or count the event.
		return
	}
	m.lastEventAt = at
	m.eventCount++

	switch ev.Type {
	case "hello":
		if ev.Session != nil {
			m.sessionID = ev.Session.ID
			m.engine = ev.Session.Engine
			m.partials = ev.Session.Partials
			m.sessionStart = ev.Session.StartedAt
			m.sessionActive = true
		} else {
			m.sessionID = ""
			m.engine = ""
			m.partials = false
			m.sessionStart = ""
			m.sessionActive = false
		}
	case "start":
		m.sessionActive = true
	case "eof":
		m.sessionActive = false
	case "error":
		if ev.Fatal != nil && *ev.Fatal {
			m.sessionActive = false
		}
	}
	m.appendServer(ev, raw, at)
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.output.ScrollUp(m.output.MouseWheelDelta)
		m.follow = m.output.AtBottom()
		if m.follow {
			m.unseen = 0
		}
	case tea.MouseButtonWheelDown:
		m.output.ScrollDown(m.output.MouseWheelDelta)
		m.follow = m.output.AtBottom()
		if m.follow {
			m.unseen = 0
		}
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "ctrl+c":
		m.closeConn()
		return m, tea.Quit
	case "tab", "shift+tab":
		if m.focus == focusInput {
			m.focus = focusMenu
			m.input.Blur()
		} else {
			m.focus = focusInput
			return m, m.input.Focus()
		}
		return m, nil
	case "esc":
		m.focus = focusInput
		return m, m.input.Focus()
	case "pgup":
		m.output.PageUp()
		m.follow = m.output.AtBottom()
		if m.follow {
			m.unseen = 0
		}
		return m, nil
	case "pgdown":
		m.output.PageDown()
		m.follow = m.output.AtBottom()
		if m.follow {
			m.unseen = 0
		}
		return m, nil
	}

	if m.focus == focusMenu {
		switch key {
		case "up", "k":
			if m.menuIndex > 0 {
				m.menuIndex--
			}
			return m, nil
		case "down", "j":
			if m.menuIndex < len(menuItems)-1 {
				m.menuIndex++
			}
			return m, nil
		case "enter":
			return m.runAction(menuItems[m.menuIndex].action)
		case "h":
			return m.runAction(actHealth)
		case "a":
			return m.runAction(actAudio)
		case "t":
			return m.runAction(actTranscript)
		case "c":
			return m.runAction(actClear)
		case "r":
			return m.forceReconnect()
		case "?":
			return m.runCommand("listCommands")
		}
		// Typing while the menu is focused jumps to the command bar and
		// inserts the rune, so the UI never feels modal.
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.focus = focusInput
			cmd := m.input.Focus()
			var ucmd tea.Cmd
			m.input, ucmd = m.input.Update(msg)
			return m, tea.Batch(cmd, ucmd)
		}
		return m, nil
	}

	switch key {
	case "enter":
		value := strings.TrimSpace(m.input.Value())
		m.input.Reset()
		if value == "" {
			return m, nil
		}
		return m.runCommand(value)
	case "ctrl+r":
		return m.runAction(actHealth)
	case "ctrl+s":
		return m.runAction(actAudio)
	case "ctrl+t":
		return m.runAction(actTranscript)
	case "ctrl+l":
		return m.runAction(actClear)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) runAction(a action) (tea.Model, tea.Cmd) {
	switch a {
	case actHealth:
		m.appendAppAlways("health check requested")
		return m, m.healthCmd(true)
	case actAudio:
		m.streaming = !m.streaming
		if m.streaming {
			m.appendApp("audio test ON — server transcript events will appear here")
			if m.conn == nil {
				m.wantConn = true
				if m.state == stateAuthFailed {
					m.state = stateIdle
				}
				cmd := m.startConnect()
				return m, cmd
			}
		} else {
			m.appendApp("audio test OFF")
		}
		return m, nil
	case actTranscript:
		m.onlyTranscript = !m.onlyTranscript
		m.unseen = 0
		m.rebuildOutput()
		return m, nil
	case actClear:
		m.clearOutput()
		return m, nil
	}
	return m, nil
}

func (m Model) runCommand(line string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return m, nil
	}
	cmd := strings.ToLower(fields[0])
	args := fields[1:]

	switch cmd {
	case "listcommands", "commands", "help", "?":
		m.appendBlockAlways(commandHelp())
		return m, nil

	case "clear":
		m.clearOutput()
		return m, nil

	case "onlytranscript", "only-transcript", "transcriptonly":
		if len(args) == 0 {
			m.onlyTranscript = !m.onlyTranscript
		} else {
			v, ok := parseOnOff(args[0])
			if !ok {
				m.appendAppAlways("usage: onlyTranscript [on|off]")
				return m, nil
			}
			m.onlyTranscript = v
		}
		m.unseen = 0
		m.rebuildOutput()
		return m, nil

	case "raw":
		if len(args) == 0 {
			m.rawMode = !m.rawMode
		} else {
			v, ok := parseOnOff(args[0])
			if !ok {
				m.appendAppAlways("usage: raw [on|off]")
				return m, nil
			}
			m.rawMode = v
		}
		m.rebuildOutput()
		return m, nil

	case "audio", "testaudio", "listen":
		if len(args) == 0 {
			return m.runAction(actAudio)
		}
		v, ok := parseOnOff(args[0])
		if !ok {
			m.appendAppAlways("usage: audio [on|off]")
			return m, nil
		}
		if v != m.streaming {
			return m.runAction(actAudio)
		}
		return m, nil

	case "health", "check":
		m.appendAppAlways("health check requested")
		return m, m.healthCmd(true)

	case "connect":
		m.wantConn = true
		if m.conn != nil {
			m.appendApp("feed is already connected")
			return m, nil
		}
		if m.state == stateAuthFailed {
			m.state = stateIdle
		}
		cmd := m.startConnect()
		return m, cmd

	case "disconnect":
		m.wantConn = false
		m.gen++
		m.closeConn()
		m.connecting = false
		m.lastConnErr = ""
		m.state = stateIdle
		m.reconnectArmed = false
		m.reconnectAt = time.Time{}
		m.appendApp("feed disconnected by user")
		return m, nil

	case "reconnect":
		return m.forceReconnect()

	case "url":
		if len(args) == 0 {
			m.appendApp("url: " + settings.RedactURL(m.settings.WSURL))
			return m, nil
		}
		m.settings.WSURL = settings.NormalizeWSURL(args[0])
		m.settings.HTTPURL = settings.DeriveHealthURL(m.settings.WSURL)
		m.settings.Source = "input"
		m.gen++
		m.closeConn()
		m.connecting = false
		m.lastConnErr = ""
		m.reconnectArmed = false
		m.wantConn = true
		m.state = stateIdle
		m.backoff = time.Second
		m.appendApp("endpoint set: " + settings.RedactURL(m.settings.WSURL))
		cmd := m.startConnect()
		return m, tea.Batch(cmd, m.healthCmd(false))

	case "token":
		if len(args) == 0 {
			m.appendApp("token: " + m.settings.TokenLabel())
			return m, nil
		}
		m.settings.Token = strings.TrimSpace(args[0])
		m.settings.TokenFrom = "input"
		m.gen++
		m.closeConn()
		m.connecting = false
		m.lastConnErr = ""
		m.reconnectArmed = false
		m.wantConn = true
		m.state = stateIdle
		m.backoff = time.Second
		m.appendApp(fmt.Sprintf("token updated (%d chars)", len(m.settings.Token)))
		cmd := m.startConnect()
		return m, cmd

	case "save":
		return m, saveCmd(m.settings)

	case "status":
		m.appendStatus()
		return m, nil

	case "quit", "exit", "q":
		m.closeConn()
		return m, tea.Quit

	default:
		m.appendAppAlways(fmt.Sprintf("unknown command %q — type listCommands", fields[0]))
		return m, nil
	}
}

func (m Model) forceReconnect() (tea.Model, tea.Cmd) {
	m.gen++
	m.closeConn()
	m.connecting = false
	m.lastConnErr = ""
	m.reconnectArmed = false
	m.reconnectAt = time.Time{}
	m.wantConn = true
	m.state = stateIdle
	m.backoff = time.Second
	cmd := m.startConnect()
	return m, cmd
}

// ---- connection commands --------------------------------------------------

func (m Model) discoverCmd() tea.Cmd {
	opts := m.opts
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		return discoveredMsg{res: settings.Discover(ctx, opts)}
	}
}

func (m Model) healthCmd(manual bool) tea.Cmd {
	url := m.settings.HTTPURL
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		h, err := remote.CheckHealth(ctx, url)
		return healthMsg{h: h, err: err, at: time.Now(), manual: manual, url: url}
	}
}

func (m Model) healthTickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(time.Time) tea.Msg { return healthTickMsg{} })
}

func (m *Model) startConnect() tea.Cmd {
	if m.conn != nil || m.connecting {
		return nil
	}
	m.connecting = true
	m.state = stateConnecting
	m.reconnectArmed = false
	m.reconnectAt = time.Time{}
	m.appendApp("connecting to " + settings.RedactURL(m.settings.WSURL))
	return m.connectCmd()
}

func (m Model) connectCmd() tea.Cmd {
	gen := m.gen
	s := m.settings
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		conn, err := remote.Dial(ctx, s.WSURL, s.Token)
		if err != nil {
			status := 0
			if e, ok := err.(*remote.DialError); ok {
				status = e.Status
			}
			return wsConnectErrMsg{err: err, status: status, gen: gen, at: time.Now()}
		}
		return wsConnectedMsg{conn: conn, gen: gen, at: time.Now()}
	}
}

func (m *Model) scheduleReconnect() tea.Cmd {
	if !m.wantConn || m.conn != nil || m.connecting || m.reconnectArmed {
		return nil
	}
	if m.state == stateAuthFailed {
		return nil
	}
	delay := m.backoff
	if delay < time.Second {
		delay = time.Second
	}
	m.reconnectArmed = true
	m.reconnectAt = time.Now().Add(delay)
	m.state = stateReconnecting
	m.backoff *= 2
	if m.backoff > 10*time.Second {
		m.backoff = 10 * time.Second
	}
	gen := m.gen
	return tea.Tick(delay, func(time.Time) tea.Msg { return reconnectTickMsg{gen: gen} })
}

func waitWS(conn *websocket.Conn, gen int) tea.Cmd {
	return func() tea.Msg {
		_, data, err := conn.ReadMessage()
		if err != nil {
			code := 0
			reason := ""
			if ce, ok := err.(*websocket.CloseError); ok {
				code = int(ce.Code)
				reason = ce.Text
			}
			return wsClosedMsg{
				conn: conn, gen: gen, err: err,
				code: code, reason: reason, at: time.Now(),
			}
		}
		return wsEventMsg{conn: conn, gen: gen, at: time.Now(), data: data}
	}
}

func (m *Model) closeConn() {
	if m.conn != nil {
		_ = m.conn.Close()
		m.conn = nil
	}
}

func saveCmd(s settings.Settings) tea.Cmd {
	return func() tea.Msg {
		err := s.SaveConfig(".stt-tui.json")
		return saveConfigMsg{path: ".stt-tui.json", err: err}
	}
}

// ---- entries --------------------------------------------------------------

func (m *Model) appendApp(text string) {
	m.appendEntry(entry{at: time.Now(), app: true, typ: "app", text: text})
}

func (m *Model) appendAppAlways(text string) {
	m.appendEntry(entry{at: time.Now(), app: true, always: true, typ: "app", text: text})
}

func (m *Model) appendServer(ev remote.Event, raw string, at time.Time) {
	m.appendEntry(entry{at: at, typ: ev.Type, text: ev.Text, raw: raw, event: ev})
}

func (m *Model) appendEntry(e entry) {
	if !m.follow && m.entryVisible(e) {
		m.unseen++
	}
	m.pushEntry(e)
	m.rebuildOutput()
}

func (m Model) entryVisible(e entry) bool {
	if m.onlyTranscript && !isTranscript(e) && !e.always {
		return false
	}
	return true
}

func (m *Model) pushEntry(e entry) {
	m.entries = append(m.entries, e)
	if len(m.entries) > maxEntries {
		m.entries = m.entries[len(m.entries)-maxEntries:]
	}
}

// appendBlockAlways appends command feedback that must stay visible in
// onlyTranscript mode, then positions the viewport at the beginning of the
// block. This keeps help/status output readable even when it is taller than
// the output column.
func (m *Model) appendBlockAlways(lines []string) {
	if len(lines) == 0 {
		return
	}
	before := m.entryLines
	now := time.Now()
	for _, l := range lines {
		m.pushEntry(entry{at: now, app: true, always: true, typ: "app", text: l})
	}
	m.rebuildOutput()
	added := m.entryLines - before
	if added <= 0 {
		return
	}
	if added >= m.output.Height {
		m.output.SetYOffset(before)
		m.follow = false
		m.unseen = 0
		return
	}
	m.output.GotoBottom()
	m.follow = true
	m.unseen = 0
}

func (m *Model) clearOutput() {
	m.entries = nil
	m.unseen = 0
	m.follow = true
	m.rebuildOutput()
}

func (m *Model) appendStatus() {
	lines := []string{
		"status:",
		"  url:      " + settings.RedactURL(m.settings.WSURL),
		"  health:   " + settings.RedactURL(m.settings.HTTPURL),
		"  token:    " + m.settings.TokenLabel(),
		"  device:   " + display(m.settings.Device) + " " + display(m.settings.DeviceDesc),
		"  forward:  " + display(m.settings.Forward),
		"  session:  " + m.sessionLabel(),
		"  engine:   " + m.engineLabel(),
		"  audio:    " + onOff(m.streaming) + " · events=" + strconv.Itoa(m.eventCount),
		"  filter:   " + map[bool]string{true: "onlyTranscript", false: "all output"}[m.onlyTranscript],
		"  raw view: " + onOff(m.rawMode),
		"  started:  " + display(m.sessionStart),
		"  last ev:  " + m.lastEventLabel(),
		"  last err: " + display(m.lastConnErr),
	}
	m.appendBlockAlways(lines)
}

func (m Model) lastEventLabel() string {
	if m.lastEventAt.IsZero() {
		return "—"
	}
	return m.lastEventAt.Format("15:04:05")
}

func (m Model) sessionStateLabel() string {
	switch {
	case m.sessionActive:
		return "active"
	case m.sessionID != "" || m.engine != "":
		return "idle"
	default:
		return "—"
	}
}

func (m Model) sessionLabel() string {
	switch {
	case m.sessionActive && m.sessionID != "":
		return m.sessionID + " (active)"
	case m.sessionActive:
		return "active"
	default:
		return "idle"
	}
}

func (m Model) engineLabel() string {
	if m.engine == "" {
		return "—"
	}
	if m.partials {
		return m.engine + " · partials"
	}
	return m.engine
}

func commandHelp() []string {
	return []string{
		"commands:",
		"  listCommands | help | ?     show this list",
		"  clear                       clear the output column",
		"  onlyTranscript [on|off]     show only final/partial server text",
		"  raw [on|off]                show raw JSON instead of summaries",
		"  audio [on|off]              start/stop the audio feed test",
		"  health                      GET /healthz now",
		"  connect | disconnect        manage the WebSocket feed",
		"  reconnect                   force a fresh connection",
		"  url <ws-url>                change endpoint (ws://host:4719/v1/feed)",
		"  token <token>               set bearer token",
		"  save                        save url/token to .stt-tui.json",
		"  status                      show resolved connection details",
		"  quit | exit                 leave the app",
		"",
		"keys: tab menu/input · a audio · h health · t transcript-only",
		"      c clear · r reconnect · pgup/pgdn or wheel scroll output",
	}
}

func parseOnOff(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "y":
		return true, true
	case "off", "false", "0", "no", "n":
		return false, true
	default:
		return false, false
	}
}

func display(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func onOff(v bool) string {
	if v {
		return "ON"
	}
	return "OFF"
}

// ---- small helpers used by the view --------------------------------------

func (m Model) connSymbol() (string, string) {
	switch m.state {
	case stateDiscovering:
		return "◌", "discovering"
	case stateIdle:
		return "○", "disconnected"
	case stateConnecting:
		return "◌", "connecting"
	case stateConnected:
		return "●", "connected"
	case stateReconnecting:
		if !m.reconnectAt.IsZero() {
			d := time.Until(m.reconnectAt).Round(time.Second)
			if d > 0 {
				return "◌", fmt.Sprintf("retry in %s", d)
			}
		}
		return "◌", "reconnecting"
	case stateAuthFailed:
		return "✖", "auth failed"
	case stateError:
		return "✖", "error"
	default:
		return "○", "unknown"
	}
}

func (m Model) rightStatus() string {
	ev := strconv.Itoa(m.eventCount) + " ev"
	switch {
	case m.state == stateAuthFailed:
		return "✖ auth"
	case m.state == stateConnected && m.streaming:
		s := "● LIVE · " + ev
		if m.unseen > 0 {
			s += " · ↓" + strconv.Itoa(m.unseen)
		} else if !m.follow {
			s += " · ↓more"
		}
		return s
	case m.state == stateConnected && !m.streaming:
		s := "○ paused · " + ev
		if m.unseen > 0 {
			s += " · ↓" + strconv.Itoa(m.unseen)
		} else if !m.follow {
			s += " · ↓more"
		}
		return s
	case m.state == stateConnecting || m.state == stateReconnecting:
		return "◌ connecting"
	case m.state == stateError:
		return "✖ offline"
	case m.state == stateDiscovering:
		return "◌ discovering"
	default:
		return "○ offline"
	}
}
