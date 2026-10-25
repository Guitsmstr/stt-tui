package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"stt-tui/internal/remote"
)

// A deliberately restrained aquamarine palette: borders are diluted, the
// title is the only saturated element.
var (
	colBorder   = lipgloss.Color("#6FB7AA")
	colAqua     = lipgloss.Color("#7FD8C9")
	colAquaHi   = lipgloss.Color("#B9F5EA")
	colText     = lipgloss.Color("#D8FFF7")
	colInfo     = lipgloss.Color("#9CCFC5")
	colDim      = lipgloss.Color("#5E8F88")
	colError    = lipgloss.Color("#FF7A9C")
	colWarn     = lipgloss.Color("#FFC66D")
	colSelectBg = lipgloss.Color("#173B35")

	borderStyle   = lipgloss.NewStyle().Foreground(colBorder)
	headerStyle   = lipgloss.NewStyle().Foreground(colAqua).Bold(true)
	sectionStyle  = lipgloss.NewStyle().Foreground(colAqua).Bold(true)
	infoStyle     = lipgloss.NewStyle().Foreground(colInfo)
	dimStyle      = lipgloss.NewStyle().Foreground(colDim)
	finalStyle    = lipgloss.NewStyle().Foreground(colText).Bold(true)
	partialStyle  = lipgloss.NewStyle().Foreground(colAqua)
	protoStyle    = lipgloss.NewStyle().Foreground(colDim)
	appStyle      = lipgloss.NewStyle().Foreground(colDim)
	errStyle      = lipgloss.NewStyle().Foreground(colError)
	warnStyle     = lipgloss.NewStyle().Foreground(colWarn)
	selectedStyle = lipgloss.NewStyle().
			Background(colSelectBg).
			Foreground(colAquaHi).
			Bold(true)
	selectedIdleStyle = lipgloss.NewStyle().Foreground(colAquaHi).Bold(true)
)

type menuItem struct {
	action action
	key    string
}

var menuItems = []menuItem{
	{actHealth, "h"},
	{actAudio, "a"},
	{actTranscript, "t"},
	{actClear, "c"},
}

type paneLine struct {
	text  string
	style lipgloss.Style
}

type styledLine struct {
	text  string
	style lipgloss.Style
}

// art is a compact 3-line STT wordmark.
var artLines = []string{
	"█▀▀ ▀█▀ ▀█▀",
	"▀▀█  █   █ ",
	"▀▀▀  ▀   ▀ ",
}

const artWidth = 12

func (m *Model) resize() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.artGap = 1
	if m.height < 23 {
		m.artGap = 0
	}
	boxHeight := m.height - artHeight - m.artGap - bottomSpace
	m.contentH = boxHeight - 4
	if m.contentH < 1 {
		m.contentH = 1
	}

	innerW := m.width - 2
	panes := innerW - 1
	if panes < 2 {
		panes = 2
	}
	m.leftW = panes * 2 / 5
	minLeft := 10
	if panes-minLeft < 10 {
		minLeft = panes / 3
	}
	if m.leftW < minLeft {
		m.leftW = minLeft
	}
	if m.leftW > panes-8 {
		m.leftW = panes - 8
	}
	if m.leftW < 2 {
		m.leftW = 2
	}
	m.rightW = panes - m.leftW
	if m.rightW < 1 {
		m.rightW = 1
	}

	m.output.Width = max(1, m.rightW-2)
	m.output.Height = max(1, m.contentH-1)
	m.input.Width = max(1, innerW-4)
	m.rebuildOutput()
}

// View satisfies tea.Model.
func (m Model) View() string {
	if m.width < minWidth || m.height < minHeight {
		return tooSmallView(m.width, m.height)
	}

	lines := make([]string, 0, m.height)
	lines = append(lines, m.renderArt()...)
	if m.artGap > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, strings.Split(m.renderBox(), "\n")...)
	for range bottomSpace {
		lines = append(lines, "")
	}

	for len(lines) < m.height {
		lines = append(lines, "")
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func tooSmallView(w, h int) string {
	msg := fmt.Sprintf("terminal too small\n\n%d×%d — need at least %d×%d", w, h, minWidth, minHeight)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, warnStyle.Render(msg))
}

func (m Model) renderArt() []string {
	out := make([]string, 0, len(artLines))
	for _, line := range artLines {
		styled := artLine(line)
		out = append(out, lipgloss.PlaceHorizontal(m.width, lipgloss.Center, styled))
	}
	return out
}

func artLine(line string) string {
	start := [3]float64{0x2B, 0xE3, 0xC8} // aquamarine
	end := [3]float64{0xFF, 0x4F, 0xD8}   // fuchsia
	var b strings.Builder
	x := 0
	for _, r := range line {
		t := 0.0
		if artWidth > 1 {
			t = float64(x) / float64(artWidth-1)
		}
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
		cr := uint8(start[0] + (end[0]-start[0])*t)
		cg := uint8(start[1] + (end[1]-start[1])*t)
		cb := uint8(start[2] + (end[2]-start[2])*t)
		style := lipgloss.NewStyle().
			Foreground(lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", cr, cg, cb))).
			Bold(true)
		b.WriteString(style.Render(string(r)))
		x += runewidth.RuneWidth(r)
	}
	return b.String()
}

func (m Model) renderBox() string {
	innerW := m.width - 2
	left := m.leftRendered()
	right := m.rightRendered()

	var b strings.Builder
	b.WriteString(borderStyle.Render("╭" + strings.Repeat("─", innerW) + "╮"))
	b.WriteByte('\n')
	for i := 0; i < m.contentH; i++ {
		b.WriteString(borderStyle.Render("│"))
		b.WriteString(left[i])
		b.WriteString(borderStyle.Render("│"))
		b.WriteString(right[i])
		b.WriteString(borderStyle.Render("│"))
		b.WriteByte('\n')
	}
	divider := "├" + strings.Repeat("─", m.leftW) + "┴" + strings.Repeat("─", m.rightW) + "┤"
	b.WriteString(borderStyle.Render(divider))
	b.WriteByte('\n')
	b.WriteString(borderStyle.Render("│"))
	b.WriteString(m.renderInput(innerW))
	b.WriteString(borderStyle.Render("│"))
	b.WriteByte('\n')
	b.WriteString(borderStyle.Render("╰" + strings.Repeat("─", innerW) + "╯"))
	return b.String()
}

func (m Model) renderInput(innerW int) string {
	contentW := innerW - 2
	in := m.input
	if m.focus == focusMenu {
		in.PromptStyle = dimStyle
		in.PlaceholderStyle = dimStyle
		in.TextStyle = dimStyle
	}
	view := in.View()
	if contentW <= 0 {
		return ""
	}
	view = ansi.Truncate(view, contentW, "")
	view = padANSI(view, contentW)
	return " " + view + " "
}

func (m Model) leftRendered() []string {
	lines := m.leftLines()
	w := m.leftW - 2
	out := make([]string, 0, m.contentH)
	for i := 0; i < m.contentH; i++ {
		var pl paneLine
		if i < len(lines) {
			pl = lines[i]
		}
		out = append(out, renderPaneLine(pl, w))
	}
	return out
}

func (m Model) rightRendered() []string {
	w := m.rightW - 2
	out := make([]string, 0, m.contentH)

	label := "OUTPUT · LIVE FEED"
	if w < 38 {
		label = "OUTPUT"
	}
	header := paneLine{
		text:  headerBlock(label, m.rightStatus(), w),
		style: headerStyle,
	}
	out = append(out, renderPaneLine(header, w))

	vpHeight := m.contentH - 1
	if vpHeight < 0 {
		vpHeight = 0
	}
	vpLines := strings.Split(m.output.View(), "\n")
	for i := 0; i < vpHeight; i++ {
		var line string
		if i < len(vpLines) {
			line = vpLines[i]
		}
		line = padANSI(ansi.Truncate(line, w, ""), w)
		out = append(out, " "+line+" ")
	}
	return out
}

func renderPaneLine(pl paneLine, w int) string {
	text := padPlain(pl.text, w)
	return pl.style.Render(" " + text + " ")
}

func (m Model) leftLines() []paneLine {
	w := m.leftW - 2
	if w < 1 {
		return nil
	}
	lines := []paneLine{{
		text:  headerBlock("INFO & OPTIONS", "", w),
		style: headerStyle,
	}}

	info := m.infoLines()
	fixed := 1 + 1 + len(menuItems) // actions header + menu
	avail := m.contentH - fixed
	if avail < 0 {
		avail = 0
	}

	showInfo := len(info)
	withBlank := false
	if avail >= len(info)+1 {
		showInfo = len(info)
		withBlank = true
	} else if avail < len(info) {
		showInfo = avail
	}
	for i := 0; i < showInfo; i++ {
		lines = append(lines, info[i])
	}
	if withBlank {
		lines = append(lines, paneLine{})
	}
	lines = append(lines, paneLine{text: "ACTIONS", style: sectionStyle})
	lines = append(lines, m.menuLines()...)
	return lines
}

func (m Model) infoLines() []paneLine {
	sym, word := m.connSymbol()
	statusStyle := infoStyle
	switch m.state {
	case stateConnected:
		statusStyle = lipgloss.NewStyle().Foreground(colAqua).Bold(true)
	case stateAuthFailed, stateError:
		statusStyle = errStyle
	case stateConnecting, stateReconnecting, stateDiscovering:
		statusStyle = warnStyle
	}

	endpoint := m.settings.Endpoint()
	if m.settings.Source != "" {
		endpoint += " · " + m.settings.Source
	}
	device := display(m.settings.Device)
	if m.settings.DeviceDesc != "" {
		device += " · " + m.settings.DeviceDesc
	}
	session := "session " + m.sessionStateLabel()
	engine := "engine " + m.engineLabel()

	healthStyle := infoStyle
	var health string
	switch {
	case !m.healthSeen && m.healthErr == "":
		health = "health —"
		healthStyle = dimStyle
	case m.healthErr != "":
		health = "health error"
		healthStyle = errStyle
	default:
		health = "health ok · " + m.healthAt.Format("15:04:05")
	}
	feed := "audio " + onOff(m.streaming) + " · " + strconv.Itoa(m.eventCount) + " events"
	if m.rawMode {
		feed += " · raw"
	}

	return []paneLine{
		{text: sym + " " + word, style: statusStyle},
		{text: endpoint, style: infoStyle},
		{text: device, style: infoStyle},
		{text: session, style: infoStyle},
		{text: engine, style: dimStyle},
		{text: health, style: healthStyle},
		{text: feed, style: infoStyle},
	}
}

func (m Model) menuLines() []paneLine {
	w := m.leftW - 2
	out := make([]paneLine, 0, len(menuItems))
	for i, it := range menuItems {
		prefix := "  "
		if i == m.menuIndex {
			prefix = "▸ "
		}
		label := m.menuLabel(it.action)
		text := prefix + label
		gap := w - runewidth.StringWidth(text) - 1
		if gap < 1 {
			text = shorten(text, max(0, w-2)) + " "
			gap = w - runewidth.StringWidth(text) - 1
			if gap < 0 {
				gap = 0
			}
		}
		text += strings.Repeat(" ", gap) + it.key
		text = shorten(text, w)
		style := infoStyle
		if i == m.menuIndex {
			if m.focus == focusMenu {
				style = selectedStyle
			} else {
				style = selectedIdleStyle
			}
		}
		out = append(out, paneLine{text: text, style: style})
	}
	return out
}

func (m Model) menuLabel(a action) string {
	switch a {
	case actHealth:
		return "Health check"
	case actAudio:
		if m.streaming {
			return "Stop audio test"
		}
		return "Start audio test"
	case actTranscript:
		if m.onlyTranscript {
			return "Show all output"
		}
		return "Only transcript"
	case actClear:
		return "Clear output"
	}
	return "?"
}

func headerBlock(label, right string, w int) string {
	if w <= 0 {
		return ""
	}
	left := " " + label + " "
	if right != "" {
		lw := runewidth.StringWidth(left)
		rw := runewidth.StringWidth(right)
		if lw+rw+1 <= w {
			gap := w - lw - rw
			if gap < 1 {
				gap = 1
			}
			return left + strings.Repeat("─", gap-1) + " " + right
		}
	}
	left = shorten(left, w)
	gap := w - runewidth.StringWidth(left)
	if gap > 0 {
		left += strings.Repeat("─", gap)
	}
	return left
}

// ---- output column --------------------------------------------------------

func (m *Model) rebuildOutput() {
	if m.width == 0 {
		return
	}
	lines, entryLines := m.outputLinesCount()
	m.entryLines = entryLines
	content := make([]string, 0, len(lines))
	for _, l := range lines {
		content = append(content, l.style.Render(l.text))
	}
	m.output.SetContent(strings.Join(content, "\n"))
	if m.follow {
		m.output.GotoBottom()
		m.unseen = 0
	}
}

func (m Model) outputLines() []styledLine {
	lines, _ := m.outputLinesCount()
	return lines
}

func (m Model) outputLinesCount() ([]styledLine, int) {
	cw := m.output.Width
	if cw <= 0 {
		cw = m.rightW - 2
	}
	if cw <= 0 {
		return nil, 0
	}

	var out []styledLine
	visible := 0
	for _, e := range m.entries {
		if m.onlyTranscript && !isTranscript(e) && !e.always {
			continue
		}
		visible++
		lines := m.formatEntry(e, cw)
		out = append(out, lines...)
	}

	entryLines := len(out)
	if len(out) == 0 {
		var msg []string
		switch {
		case m.state == stateDiscovering:
			msg = []string{"discovering the phone via adb…", "", "resolving endpoint, forward and token"}
		case m.onlyTranscript && visible == 0 && len(m.entries) > 0:
			msg = []string{"no transcript yet", "", "speak near the phone; toggle onlyTranscript to see protocol events"}
		case !m.streaming:
			msg = []string{"audio test is OFF", "", "press [a] or type `audio` to stream the phone's events"}
		case m.state == stateAuthFailed:
			msg = []string{"authentication failed", "", "use `token <token>` then `reconnect`"}
		case m.state == stateIdle:
			msg = []string{"feed disconnected", "", "type `connect` to reconnect"}
		case m.state == stateError:
			msg = []string{"feed offline", "", "press [r] or type `reconnect`"}
		case m.conn == nil:
			msg = []string{"audio test is ON", "", "waiting for the feed to reconnect…"}
		default:
			msg = []string{"waiting for events from the phone…", "", "session idle: the server will send start when capture begins"}
		}
		msg = append(msg, "", "tab: actions · listCommands: help")
		for _, raw := range msg {
			for _, l := range wrapPlain(raw, cw) {
				out = append(out, styledLine{text: shortCenter(l, cw), style: dimStyle})
			}
		}
	}
	return out, entryLines
}

func shortCenter(s string, w int) string {
	if s == "" || runewidth.StringWidth(s) >= w {
		return shorten(s, w)
	}
	gap := w - runewidth.StringWidth(s)
	left := gap / 2
	return strings.Repeat(" ", left) + s
}

func isTranscript(e entry) bool {
	return !e.app && (e.typ == "final" || e.typ == "partial")
}

func (m Model) formatEntry(e entry, cw int) []styledLine {
	ts := e.at.Format("15:04:05")

	if m.rawMode && !e.app {
		label := padLabel(e.typ)
		prefix := ts + " " + label
		return styleWrapped(prefix, display(e.raw), cw, dimStyle)
	}

	if e.app {
		if strings.TrimSpace(e.text) == "" {
			return []styledLine{{text: "", style: appStyle}}
		}
		prefix := ts + " · "
		return styleWrapped(prefix, e.text, cw, appStyle)
	}

	prefix := ts + " " + padLabel(e.typ)
	text := eventSummary(e)
	style := eventStyle(e.typ)
	return styleWrapped(prefix, text, cw, style)
}

func styleWrapped(prefix, text string, cw int, style lipgloss.Style) []styledLine {
	lines := wrapWithPrefix(prefix, text, cw)
	out := make([]styledLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, styledLine{text: l, style: style})
	}
	return out
}

func padLabel(typ string) string {
	label := strings.ToUpper(typ)
	switch typ {
	case "final":
		label = "FINAL"
	case "partial":
		label = "PARTIAL"
	case "hello":
		label = "HELLO"
	case "start":
		label = "START"
	case "eof":
		label = "EOF"
	case "error":
		label = "ERROR"
	case "raw":
		label = "RAW"
	}
	if runewidth.StringWidth(label) > 7 {
		label = shorten(label, 7)
	}
	gap := 8 - runewidth.StringWidth(label)
	if gap > 0 {
		label += strings.Repeat(" ", gap)
	}
	return label
}

func eventStyle(typ string) lipgloss.Style {
	switch typ {
	case "final":
		return finalStyle
	case "partial":
		return partialStyle
	case "error":
		return errStyle
	case "hello", "start", "eof":
		return protoStyle
	default:
		return dimStyle
	}
}

func eventSummary(e entry) string {
	ev := e.event
	switch e.typ {
	case "final":
		txt := e.text
		if txt == "" {
			txt = "∅"
		}
		if meta := timingMeta(ev); meta != "" {
			txt += "  · " + meta
		}
		return txt
	case "partial":
		txt := e.text
		if txt == "" {
			txt = "…"
		}
		if meta := timingMeta(ev); meta != "" {
			txt += " · " + meta
		}
		return txt
	case "hello":
		s := "server " + display(ev.Server) + " · " + display(ev.Protocol)
		if ev.Session == nil {
			return s + " · no active session (waiting for start)"
		}
		s += " · session " + ev.Session.ID + " · " + ev.Session.Engine
		if ev.Session.Partials {
			s += " · partials"
		}
		return s
	case "start":
		if ev.Audio != nil {
			return fmt.Sprintf("PCM %d Hz · %d ch · %s", ev.Audio.Rate, ev.Audio.Channels, ev.Audio.Format)
		}
		return "capture started"
	case "eof":
		var parts []string
		if ev.Frames != nil {
			parts = append(parts, "frames="+strconv.Itoa(*ev.Frames))
		}
		if ev.AudioS != nil {
			parts = append(parts, fmt.Sprintf("audio=%.2fs", *ev.AudioS))
		}
		if ev.SkippedS != nil {
			parts = append(parts, fmt.Sprintf("skipped=%.2fs", *ev.SkippedS))
		}
		if ev.RSSKB != nil {
			parts = append(parts, fmt.Sprintf("rss=%dKB", *ev.RSSKB))
		}
		if len(parts) == 0 {
			return "capture ended"
		}
		return strings.Join(parts, " · ")
	case "error":
		s := "code=" + display(ev.Code) + " · " + display(ev.Message)
		if ev.Fatal != nil && *ev.Fatal {
			s += " · fatal"
		}
		return s
	default:
		if e.raw != "" {
			return e.raw
		}
		return display(e.text)
	}
}

func timingMeta(ev remote.Event) string {
	var parts []string
	if ev.T0 != nil && ev.T1 != nil {
		parts = append(parts, fmt.Sprintf("%.2f–%.2fs", *ev.T0, *ev.T1))
	} else if ev.T1 != nil {
		parts = append(parts, fmt.Sprintf("t1=%.2fs", *ev.T1))
	}
	if ev.RTF != nil {
		parts = append(parts, fmt.Sprintf("rtf=%.2f", *ev.RTF))
	}
	if ev.Frame != nil {
		parts = append(parts, "frame="+strconv.Itoa(*ev.Frame))
	}
	if ev.Model != "" {
		parts = append(parts, ev.Model)
	}
	return strings.Join(parts, " · ")
}
