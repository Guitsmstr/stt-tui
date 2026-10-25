package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"stt-tui/internal/remote"
	"stt-tui/internal/settings"
	"stt-tui/internal/tui"
)

const version = "0.1.0"

func main() {
	os.Exit(run())
}

func run() int {
	var (
		urlFlag     = flag.String("url", "", "WebSocket URL (default discovered; ws://host:4719/v1/feed)")
		tokenFlag   = flag.String("token", "", "bearer token (default: config/env/adb autodiscovery)")
		httpFlag    = flag.String("http-url", "", "health endpoint URL (default derived from --url)")
		deviceFlag  = flag.String("device", "", "adb serial (default: first connected device)")
		configFlag  = flag.String("config", "", "JSON config file (default ./.stt-tui.json or ~/.config/stt-tui/config.json)")
		noADBFlag   = flag.Bool("no-adb", false, "disable adb autodiscovery")
		printConfig = flag.Bool("print-config", false, "resolve connection settings, print them and exit")
		probeFlag   = flag.Bool("probe", false, "resolve settings, check health/feed once and exit")
		versionFlag = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `stt-tui %s — terminal probe for the stt-remote/1 feed

Usage:
  stt-tui [options]

Options:
`, version)
		flag.PrintDefaults()
		fmt.Fprint(flag.CommandLine.Output(), `
Environment:
  STT_WS_URL, STT_TOKEN, STT_HTTP_URL, STT_DEVICE, STT_TOKEN_FILE, STT_NO_ADB

Discovery:
  1. config file  2. environment  3. flags  4. adb (forward + token via su)
  The token is read from the phone's Alpine rootfs and is never printed.
`)
	}
	flag.Parse()

	if *versionFlag {
		fmt.Printf("stt-tui %s\n", version)
		return 0
	}

	opts := settings.Options{
		URL:        *urlFlag,
		Token:      *tokenFlag,
		HTTPURL:    *httpFlag,
		Device:     *deviceFlag,
		ConfigPath: *configFlag,
		NoADB:      *noADBFlag,
	}

	if *printConfig || *probeFlag {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		res := settings.Discover(ctx, opts)
		printSettings(res)
		if *printConfig {
			if res.Err != nil {
				return 1
			}
			return 0
		}
		return probe(ctx, res.Settings)
	}

	model := tui.New(opts)
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "stt-tui: %v\n", err)
		return 1
	}
	return 0
}

func printSettings(res settings.Result) {
	r := res.Settings.Redacted()
	fmt.Println("stt-tui settings")
	for _, k := range []string{"url", "health", "token", "source", "device", "model", "forward"} {
		if v := r[k]; v != "" {
			fmt.Printf("  %-8s %s\n", k+":", v)
		}
	}
	if len(res.Notes) > 0 {
		fmt.Println("discovery notes")
		for _, n := range res.Notes {
			fmt.Println("  - " + n)
		}
	}
	if res.Err != nil {
		fmt.Printf("discovery error: %v\n", res.Err)
	}
}

func probe(ctx context.Context, s settings.Settings) int {
	fmt.Println("probe")
	rc := 0
	hctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	h, err := remote.CheckHealth(hctx, s.HTTPURL)
	cancel()
	if err != nil {
		fmt.Printf("  health: ERROR %v\n", err)
		rc = 1
	} else {
		fmt.Printf("  health: %s · protocol=%s · session=%v\n", h.Status, h.Protocol, h.Session)
	}

	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := remote.Dial(dctx, s.WSURL, s.Token)
	cancel()
	if err != nil {
		fmt.Printf("  feed: ERROR %v\n", err)
		return 1
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		fmt.Printf("  feed: connected, but no hello: %v\n", err)
		return 1
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 220 {
		msg = msg[:220] + "…"
	}
	fmt.Printf("  feed: connected · first message: %s\n", msg)
	return rc
}
