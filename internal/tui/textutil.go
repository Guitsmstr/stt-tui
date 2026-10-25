package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// padPlain right-pads/truncates a plain string to exactly w cells.
func padPlain(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = shorten(s, w)
	gap := w - runewidth.StringWidth(s)
	if gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// padANSI right-pads/truncates a string that may contain ANSI sequences to
// exactly w visible cells.
func padANSI(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	gap := w - ansi.StringWidth(s)
	if gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// shorten truncates plain text to at most w cells, adding an ellipsis when it
// actually had to cut.
func shorten(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return runewidth.Truncate(s, w, "…")
}

// wrapWithPrefix wraps text to width and prefixes the first line with prefix;
// continuation lines are indented to align with the text.
func wrapWithPrefix(prefix, text string, width int) []string {
	if width <= 0 {
		return nil
	}
	prefixW := ansi.StringWidth(prefix)
	if prefixW >= width {
		return wrapPlain(prefix+text, width)
	}
	avail := width - prefixW
	chunks := wrapPlain(text, avail)
	if len(chunks) == 0 {
		return []string{prefix}
	}
	out := make([]string, 0, len(chunks))
	out = append(out, prefix+chunks[0])
	indent := strings.Repeat(" ", prefixW)
	for _, c := range chunks[1:] {
		out = append(out, indent+c)
	}
	return out
}

// wrapPlain is a greedy word wrap that also breaks words longer than width.
func wrapPlain(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{""}
	}

	var out []string
	var line strings.Builder
	lineW := 0
	flush := func() {
		out = append(out, line.String())
		line.Reset()
		lineW = 0
	}

	words := strings.Fields(s)
	for _, word := range words {
		ww := runewidth.StringWidth(word)
		if lineW > 0 && lineW+1+ww <= width {
			line.WriteByte(' ')
			line.WriteString(word)
			lineW += 1 + ww
			continue
		}
		if lineW > 0 {
			flush()
		}
		if ww <= width {
			line.WriteString(word)
			lineW = ww
			continue
		}
		// Long word: split it hard.
		rest := word
		for runewidth.StringWidth(rest) > width {
			part := runewidth.Truncate(rest, width, "")
			// Truncate may cut on a multi-cell rune boundary; fall back to a
			// rune scan if nothing was consumed.
			if part == "" {
				rs := []rune(rest)
				part = string(rs[0])
			}
			out = append(out, part)
			rest = strings.TrimPrefix(rest, part)
		}
		if rest != "" {
			line.WriteString(rest)
			lineW = runewidth.StringWidth(rest)
		}
	}
	if lineW > 0 || len(out) == 0 {
		flush()
	}
	return out
}
