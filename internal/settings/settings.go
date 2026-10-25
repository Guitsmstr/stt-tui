// Package settings resolves how to reach the STT server: WebSocket URL,
// bearer token and (when needed) an adb port forward to the phone.
//
// Resolution order, lowest to highest priority:
//
//  1. built-in defaults (ws://127.0.0.1:4719/v1/feed)
//  2. JSON config file (--config, ./.stt-tui.json, ~/.config/stt-tui/config.json)
//  3. environment (STT_WS_URL, STT_TOKEN, STT_HTTP_URL, STT_DEVICE, STT_NO_ADB)
//  4. command-line overrides passed in Options
//  5. adb autodiscovery for anything still missing (device, local forward, token)
//
// The token is read from the phone through adb+su (the sttd token file lives in
// the Alpine proot-distro rootfs) or from a local token file. It is never
// logged.
package settings

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultWSURL   = "ws://127.0.0.1:4719/v1/feed"
	DefaultHTTPURL = "http://127.0.0.1:4719/healthz"

	// Android-side path of the token inside the Termux proot-distro Alpine
	// rootfs (see docs/CONTRATOS.md §9.2).
	deviceTokenPath = "/data/data/com.termux/files/usr/var/lib/proot-distro/containers/alpine/rootfs/root/Documents/tools/stt/var/etc/stt-token"
	deviceSTTRoot   = "/data/data/com.termux/files/usr/var/lib/proot-distro/containers/alpine/rootfs/root/Documents/tools/stt"

	deviceForwardPort = "4719"
)

var tokenRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// Options are explicit overrides (flags) plus discovery switches.
type Options struct {
	URL        string
	Token      string
	HTTPURL    string
	Device     string
	ConfigPath string
	NoADB      bool
}

// Settings is the resolved connection configuration.
type Settings struct {
	WSURL      string `json:"url"`
	HTTPURL    string `json:"http_url,omitempty"`
	Token      string `json:"token"`
	Source     string `json:"-"` // where the endpoint came from: default/env/config/adb
	TokenFrom  string `json:"-"` // where the token came from
	Device     string `json:"device,omitempty"`
	DeviceDesc string `json:"-"`
	Forward    string `json:"-"` // local forward description, e.g. tcp:4719 -> tcp:4719
}

// Result carries the settings plus human-readable discovery notes and any
// non-fatal problem.
type Result struct {
	Settings Settings
	Notes    []string
	Err      error
}

// fileConfig is the on-disk JSON shape.
type fileConfig struct {
	URL     string `json:"url"`
	HTTPURL string `json:"http_url"`
	Token   string `json:"token"`
	Device  string `json:"device"`
}

// Discover resolves settings. It always returns a usable Settings value (with
// defaults) even when discovery fails.
func Discover(ctx context.Context, opts Options) Result {
	res := Result{
		Settings: Settings{
			WSURL:   DefaultWSURL,
			HTTPURL: DefaultHTTPURL,
			Source:  "default",
		},
	}
	httpExplicit := false

	// 1. Config file.
	cfgPath := opts.ConfigPath
	explicitCfg := cfgPath != ""
	if cfgPath == "" {
		for _, p := range configCandidates() {
			if _, err := os.Stat(p); err == nil {
				cfgPath = p
				break
			}
		}
	}
	if cfgPath != "" {
		cfg, err := loadFile(cfgPath)
		if err != nil {
			res.Err = err
			res.Notes = append(res.Notes, "config: "+err.Error())
		} else {
			if cfg.URL != "" {
				res.Settings.WSURL = cfg.URL
				res.Settings.Source = "config"
			}
			if cfg.HTTPURL != "" {
				res.Settings.HTTPURL = cfg.HTTPURL
				httpExplicit = true
			}
			if cfg.Token != "" {
				res.Settings.Token = strings.TrimSpace(cfg.Token)
				res.Settings.TokenFrom = "config"
			}
			if cfg.Device != "" {
				res.Settings.Device = cfg.Device
			}
			res.Notes = append(res.Notes, "config loaded: "+cfgPath)
		}
		if explicitCfg && res.Err != nil {
			// An explicitly requested file that cannot be read is fatal enough
			// to report, but discovery can still continue with defaults.
			res.Notes = append(res.Notes, "config not loaded; continuing with defaults/env")
		}
	}

	// 2. Environment.
	if v := os.Getenv("STT_WS_URL"); v != "" {
		res.Settings.WSURL = v
		res.Settings.Source = "env"
		if os.Getenv("STT_HTTP_URL") == "" {
			httpExplicit = false // derive health from the overriding URL
		}
	}
	if v := os.Getenv("STT_HTTP_URL"); v != "" {
		res.Settings.HTTPURL = v
		httpExplicit = true
	}
	if v := os.Getenv("STT_TOKEN"); v != "" {
		res.Settings.Token = strings.TrimSpace(v)
		res.Settings.TokenFrom = "env"
	}
	if v := os.Getenv("STT_DEVICE"); v != "" {
		res.Settings.Device = v
	}
	if v := os.Getenv("STT_TOKEN_FILE"); v != "" {
		if tok, err := readTokenFile(v); err == nil {
			res.Settings.Token = tok
			res.Settings.TokenFrom = "token file"
		} else {
			res.Notes = append(res.Notes, "token file: "+err.Error())
		}
	}

	// 3. Explicit flag overrides.
	if opts.URL != "" {
		res.Settings.WSURL = opts.URL
		res.Settings.Source = "flag"
		if opts.HTTPURL == "" {
			httpExplicit = false // derive health from the overriding URL
		}
	}
	if opts.HTTPURL != "" {
		res.Settings.HTTPURL = opts.HTTPURL
		httpExplicit = true
	}
	if opts.Token != "" {
		res.Settings.Token = strings.TrimSpace(opts.Token)
		res.Settings.TokenFrom = "flag"
	}
	if opts.Device != "" {
		res.Settings.Device = opts.Device
	}

	res.Settings.WSURL = NormalizeWSURL(res.Settings.WSURL)
	if !httpExplicit || res.Settings.HTTPURL == "" {
		res.Settings.HTTPURL = DeriveHealthURL(res.Settings.WSURL)
	}
	explicitHTTPURL := res.Settings.HTTPURL

	// 4. Local token files if still missing.
	if res.Settings.Token == "" {
		for _, p := range tokenFileCandidates() {
			if tok, err := readTokenFile(p); err == nil {
				res.Settings.Token = tok
				res.Settings.TokenFrom = "token file"
				break
			}
		}
	}

	// 5. adb autodiscovery. We use it when the endpoint is not explicit or
	// the token is missing; it also fills the device label.
	explicitEndpoint := res.Settings.Source == "env" || res.Settings.Source == "config" || res.Settings.Source == "flag"
	if !opts.NoADB && os.Getenv("STT_NO_ADB") == "" && (!explicitEndpoint || res.Settings.Token == "" || isLoopbackURL(res.Settings.WSURL)) {
		discoverADB(ctx, &res, !explicitEndpoint)
	}
	if httpExplicit {
		res.Settings.HTTPURL = explicitHTTPURL
	}

	if res.Settings.Token != "" && !validToken(res.Settings.Token) {
		res.Notes = append(res.Notes, fmt.Sprintf("token: unusual length (%d chars)", len(res.Settings.Token)))
	}
	if res.Settings.Token == "" {
		res.Notes = append(res.Notes, "token: not found (use `token <token>` or set STT_TOKEN)")
	}
	if res.Err != nil {
		res.Notes = append(res.Notes, "discovery: "+res.Err.Error())
	}
	return res
}

func discoverADB(ctx context.Context, res *Result, setEndpoint bool) {
	adb, err := findADB()
	if err != nil {
		res.Err = err
		res.Notes = append(res.Notes, "adb: "+err.Error())
		return
	}

	serial, err := pickDevice(ctx, adb, res.Settings.Device)
	if err != nil {
		res.Err = err
		res.Notes = append(res.Notes, "adb: "+err.Error())
		return
	}
	res.Settings.Device = serial
	res.Notes = append(res.Notes, "adb device: "+serial)

	if desc := adbDeviceDesc(ctx, adb, serial); desc != "" {
		res.Settings.DeviceDesc = desc
	}

	// Port forward: try the port already present in an explicit loopback URL
	// first, then the usual range. 4719 may be taken by an unrelated process.
	ports := []int{}
	if !setEndpoint {
		if u, err := url.Parse(res.Settings.WSURL); err == nil && isLoopbackURL(res.Settings.WSURL) {
			if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 && p < 65536 {
				ports = append(ports, p)
			}
		}
	}
	for _, p := range []int{4719, 4720, 4721, 4722, 4723, 4724} {
		seen := false
		for _, q := range ports {
			if p == q {
				seen = true
				break
			}
		}
		if !seen {
			ports = append(ports, p)
		}
	}
	for _, port := range ports {
		local := fmt.Sprintf("tcp:%d", port)
		cmd := exec.CommandContext(ctx, adb, "-s", serial, "forward", local, "tcp:"+deviceForwardPort)
		if _, err := cmd.CombinedOutput(); err != nil {
			continue
		}
		if !portReachable(ctx, port) {
			// The forward was created but nothing is listening on the phone
			// side (or the server is down). Keep the mapping: it may start
			// later. A reachability probe is best-effort only.
			if setEndpoint {
				res.Notes = append(res.Notes, fmt.Sprintf("adb forward %s -> tcp:%s (server not answering yet)", local, deviceForwardPort))
			}
		} else {
			res.Notes = append(res.Notes, fmt.Sprintf("adb forward %s -> tcp:%s", local, deviceForwardPort))
		}
		res.Settings.Forward = fmt.Sprintf("%s -> tcp:%s", local, deviceForwardPort)
		if setEndpoint {
			res.Settings.WSURL = fmt.Sprintf("ws://127.0.0.1:%d/v1/feed", port)
			res.Settings.HTTPURL = fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
			res.Settings.Source = "adb"
		}
		break
	}

	if res.Settings.Token == "" {
		if tok, from, err := adbToken(ctx, adb, serial); err != nil {
			res.Notes = append(res.Notes, "adb token: "+err.Error())
		} else {
			res.Settings.Token = tok
			res.Settings.TokenFrom = from
			res.Notes = append(res.Notes, "token: loaded via "+from)
		}
	}
}

func findADB() (string, error) {
	if p, err := exec.LookPath("adb"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "Library/Android/sdk/platform-tools/adb"),
		filepath.Join(home, "Library/Android/sdk/sdk/platform-tools/adb"),
		filepath.Join(home, "Android/Sdk/platform-tools/adb"),
		"/opt/homebrew/bin/adb",
		"/usr/local/bin/adb",
	}
	if sdk := os.Getenv("ANDROID_HOME"); sdk != "" {
		candidates = append([]string{filepath.Join(sdk, "platform-tools/adb")}, candidates...)
	}
	if sdk := os.Getenv("ANDROID_SDK_ROOT"); sdk != "" {
		candidates = append([]string{filepath.Join(sdk, "platform-tools/adb")}, candidates...)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("adb not found in PATH or common SDK locations")
}

func pickDevice(ctx context.Context, adb, want string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, adb, "devices").Output()
	if err != nil {
		return "", fmt.Errorf("adb devices: %w", err)
	}
	var devices []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "device" {
			devices = append(devices, fields[0])
		}
	}
	if want != "" {
		for _, d := range devices {
			if d == want {
				return d, nil
			}
		}
		return "", fmt.Errorf("device %q not connected", want)
	}
	if len(devices) == 0 {
		return "", errors.New("no connected device (check USB debugging)")
	}
	return devices[0], nil
}

func adbDeviceDesc(ctx context.Context, adb, serial string) string {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, adb, "-s", serial, "shell", "getprop", "ro.product.model").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func adbToken(ctx context.Context, adb, serial string) (string, string, error) {
	paths := []string{
		deviceTokenPath,
		deviceSTTRoot + "/var/etc/stt-token",
		"/data/data/com.termux/files/usr/var/lib/proot-distro/containers/alpine/rootfs/root/documents/tools/stt/var/etc/stt-token",
		"/data/local/tmp/stt-token",
	}
	for _, p := range paths {
		if tok, err := adbCatToken(ctx, adb, serial, p); err == nil {
			return tok, "adb su", nil
		}
	}

	// Fallback: locate the token inside the Alpine rootfs. This keeps working
	// if the project moves inside the chroot.
	findCmd := "su -c 'find " + shellQuote(deviceSTTRoot) + " -maxdepth 5 -name stt-token -type f 2>/dev/null | head -n 1'"
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, adb, "-s", serial, "shell", findCmd).Output()
	if err == nil {
		p := strings.TrimSpace(string(out))
		if strings.HasPrefix(p, "/") {
			if tok, err := adbCatToken(ctx, adb, serial, p); err == nil {
				return tok, "adb su (find)", nil
			}
		}
	}
	return "", "", errors.New("could not read token through adb su (is Magisk root available?)")
}

func adbCatToken(ctx context.Context, adb, serial, path string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	// su -c needs one shell string; adb shell joins argv with spaces, so pass
	// the whole command as a single argument.
	cmdline := "su -c 'cat " + shellQuote(path) + "'"
	out, err := exec.CommandContext(cctx, adb, "-s", serial, "shell", cmdline).Output()
	if err != nil {
		return "", err
	}
	tok := firstToken(string(out))
	if tok == "" {
		return "", errors.New("empty token")
	}
	return tok, nil
}

func shellQuote(s string) string {
	// Paths we pass are fixed or come from find; strip the only dangerous
	// character for a single-quoted shell word.
	return strings.ReplaceAll(s, "'", "")
}

func firstToken(s string) string {
	for _, f := range strings.Fields(s) {
		f = strings.TrimSpace(f)
		if f != "" {
			return f
		}
	}
	return ""
}

func portReachable(ctx context.Context, port int) bool {
	cctx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/healthz", port), nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func configCandidates() []string {
	var out []string
	if wd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(wd, ".stt-tui.json"), filepath.Join(wd, "stt-tui.json"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".config/stt-tui/config.json"), filepath.Join(home, ".stt-tui.json"))
	}
	return out
}

func tokenFileCandidates() []string {
	var out []string
	if wd, err := os.Getwd(); err == nil {
		out = append(out,
			filepath.Join(wd, ".stt-token"),
			filepath.Join(wd, "stt-token"),
			filepath.Join(wd, "var/etc/stt-token"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out,
			filepath.Join(home, ".config/stt-tui/token"),
			filepath.Join(home, ".stt-token"),
		)
	}
	return out
}

func loadFile(path string) (fileConfig, error) {
	var cfg fileConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func readTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := firstToken(string(b))
	if tok == "" {
		return "", errors.New("empty token file")
	}
	return tok, nil
}

func validToken(tok string) bool {
	return tokenRE.MatchString(tok)
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// NormalizeWSURL accepts host:port, http(s)://... or ws(s)://... and returns a
// ws(s) URL pointing at /v1/feed.
func NormalizeWSURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultWSURL
	}
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return DefaultWSURL
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		u.Scheme = "ws"
	}
	if u.Host == "" {
		return DefaultWSURL
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/feed"
	}
	return u.String()
}

// DeriveHealthURL turns a ws URL into its http /healthz counterpart.
func DeriveHealthURL(wsURL string) string {
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" {
		return DefaultHTTPURL
	}
	switch strings.ToLower(u.Scheme) {
	case "wss", "https":
		u.Scheme = "https"
	default:
		u.Scheme = "http"
	}
	u.Path = "/healthz"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// Endpoint returns host:port of the feed URL.
func (s Settings) Endpoint() string {
	u, err := url.Parse(s.WSURL)
	if err != nil || u.Host == "" {
		return s.WSURL
	}
	return u.Host
}

// TokenLabel never reveals the token.
func (s Settings) TokenLabel() string {
	if s.Token == "" {
		return "missing"
	}
	state := "set"
	if validToken(s.Token) {
		state = "64 hex"
	}
	if s.TokenFrom != "" {
		return fmt.Sprintf("%s (%s)", state, s.TokenFrom)
	}
	return state
}

// RedactURL removes a token query parameter from a URL for display/logging.
// The normal path sends the token in the Authorization header, but the
// ?token= fallback exists for browsers, so do not echo it.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if _, ok := u.Query()["token"]; ok {
		q := u.Query()
		q.Set("token", "REDACTED")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Redacted returns settings safe for logs.
func (s Settings) Redacted() map[string]string {
	return map[string]string{
		"url":     RedactURL(s.WSURL),
		"health":  RedactURL(s.HTTPURL),
		"token":   s.TokenLabel(),
		"source":  s.Source,
		"device":  s.Device,
		"model":   s.DeviceDesc,
		"forward": s.Forward,
	}
}

// SaveConfig writes a 0600 JSON config with the current URL and token.
func (s Settings) SaveConfig(path string) error {
	if path == "" {
		path = ".stt-tui.json"
	}
	cfg := fileConfig{
		URL:     s.WSURL,
		HTTPURL: s.HTTPURL,
		Token:   s.Token,
		Device:  s.Device,
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
