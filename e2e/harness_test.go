//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

var (
	origDisplay        string
	origWaylandDisplay string
	origXDGRuntimeDir  string
	origXAuthority     string
	origHome           string
	origPath           string

	sqliteLibPath string
	hayamiBin     string
)

func TestMain(m *testing.M) {
	origDisplay = os.Getenv("DISPLAY")
	origWaylandDisplay = os.Getenv("WAYLAND_DISPLAY")
	origXDGRuntimeDir = os.Getenv("XDG_RUNTIME_DIR")
	origXAuthority = os.Getenv("XAUTHORITY")
	origHome = os.Getenv("HOME")
	origPath = os.Getenv("PATH")

	// Discover the pre-cached SQLite native library BEFORE any test overrides
	// HOME or XDG_CACHE_HOME so mygo/plugins/sqlite never tries to download
	// from GitHub in offline tests.
	sqliteLibPath = discoverSQLiteLibrary()
	if sqliteLibPath != "" {
		_ = os.Setenv("MYGO_SQLITE_LIBRARY", sqliteLibPath)
	}

	binDir, err := os.MkdirTemp("", "hayami-e2e-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e TestMain: create temp bin dir: %v\n", err)
		os.Exit(1)
	}

	binName := "hayami"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	hayamiBin = filepath.Join(binDir, binName)

	buildCmd := exec.Command("go", "build", "-tags", "e2e", "-o", hayamiBin, "..")
	buildCmd.Env = setOrReplaceEnv(os.Environ(), "CGO_ENABLED", "0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e TestMain: go build -tags e2e failed: %v\n%s\n", err, string(out))
		_ = os.RemoveAll(binDir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(binDir)
	os.Exit(code)
}

func sqliteLibFilename() string {
	switch runtime.GOOS {
	case "windows":
		return "mygo-sqlite3.dll"
	case "darwin":
		return "libmygo-sqlite3.dylib"
	default:
		return "libmygo-sqlite3.so"
	}
}

func discoverSQLiteLibrary() string {
	if envPath := strings.TrimSpace(os.Getenv("MYGO_SQLITE_LIBRARY")); envPath != "" {
		if abs, err := filepath.Abs(envPath); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return abs
			}
		}
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		return ""
	}
	pattern := filepath.Join(cacheDir, "mygo", "natives", "*", sqliteLibFilename())
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	for _, match := range matches {
		if st, err := os.Stat(match); err == nil && !st.IsDir() {
			return match
		}
	}
	return ""
}

func setOrReplaceEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+val)
}

type sandbox struct {
	T            *testing.T
	Root         string
	HomeDir      string
	ConfigHome   string
	DataHome     string
	CacheHome    string
	TmpDir       string
	RuntimeDir   string
	UserDataDir  string
	BinDir       string
	ShimLogDir   string
	ShimStateDir string
	DBusAddr     string
	IsolatePATH  bool
	ExtraEnv     map[string]string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()

	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	cacheHome := filepath.Join(root, "cache")
	tmpDir := filepath.Join(root, "tmp")
	binDir := filepath.Join(root, "bin")
	shimLogDir := filepath.Join(root, "shim-logs")
	shimStateDir := filepath.Join(root, "shim-state")
	userDataDir := filepath.Join(configHome, "Hayami Translator")

	for _, d := range []string{
		homeDir, configHome, dataHome, cacheHome, tmpDir,
		binDir, shimLogDir, shimStateDir, userDataDir,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	rtBase := "/tmp"
	if runtime.GOOS == "windows" {
		rtBase = ""
	}
	rtDir, err := os.MkdirTemp(rtBase, "h-e2e-*")
	if err != nil {
		t.Fatalf("create short runtime dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(rtDir)
	})
	if runtime.GOOS != "windows" && len(rtDir) >= 65 {
		t.Fatalf("runtime dir %q is >= 65 bytes (%d), would exceed mygo 100-byte socket limit", rtDir, len(rtDir))
	}

	return &sandbox{
		T:            t,
		Root:         root,
		HomeDir:      homeDir,
		ConfigHome:   configHome,
		DataHome:     dataHome,
		CacheHome:    cacheHome,
		TmpDir:       tmpDir,
		RuntimeDir:   rtDir,
		UserDataDir:  userDataDir,
		BinDir:       binDir,
		ShimLogDir:   shimLogDir,
		ShimStateDir: shimStateDir,
		DBusAddr:     "",
		ExtraEnv:     make(map[string]string),
	}
}

func (s *sandbox) configPath() string {
	return filepath.Join(s.UserDataDir, "config.json")
}

func (s *sandbox) schemaPath() string {
	return filepath.Join(s.UserDataDir, "config.schema.json")
}

func (s *sandbox) historyDBPath() string {
	return filepath.Join(s.UserDataDir, "history.db")
}

func (s *sandbox) debugLogPath() string {
	return filepath.Join(s.UserDataDir, "debug.log")
}

func (s *sandbox) readDebugLog() string {
	b, err := os.ReadFile(s.debugLogPath())
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *sandbox) singleInstanceSocketPath() string {
	sum := sha256.Sum256([]byte("Hayami Translator"))
	file := "mygo-" + strconv.Itoa(os.Getuid()) + "-" + hex.EncodeToString(sum[:6]) + ".sock"
	return filepath.Join(s.RuntimeDir, file)
}

func (s *sandbox) buildEnv(headless bool, extra ...string) []string {
	skip := map[string]bool{
		"HOME":                     true,
		"USERPROFILE":              true,
		"XDG_CONFIG_HOME":          true,
		"XDG_DATA_HOME":            true,
		"XDG_CACHE_HOME":           true,
		"TMPDIR":                   true,
		"XDG_RUNTIME_DIR":          true,
		"DBUS_SESSION_BUS_ADDRESS": true,
		"DISPLAY":                  true,
		"WAYLAND_DISPLAY":          true,
		"XAUTHORITY":               true,
		"MYGO_SQLITE_LIBRARY":      true,
		"MYGO_AUTOMATION":          true,
		"PATH":                     true,
	}
	for k := range s.ExtraEnv {
		skip[k] = true
	}
	for _, kv := range extra {
		if k, _, ok := strings.Cut(kv, "="); ok {
			skip[k] = true
		}
	}

	var env []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || skip[k] {
			continue
		}
		env = append(env, kv)
	}

	pathVal := s.BinDir
	if !s.IsolatePATH && origPath != "" {
		pathVal = s.BinDir + string(os.PathListSeparator) + origPath
	}

	env = append(env,
		"HOME="+s.HomeDir,
		"USERPROFILE="+s.HomeDir,
		"XDG_CONFIG_HOME="+s.ConfigHome,
		"XDG_DATA_HOME="+s.DataHome,
		"XDG_CACHE_HOME="+s.CacheHome,
		"TMPDIR="+s.TmpDir,
		"XDG_RUNTIME_DIR="+s.RuntimeDir,
		"DBUS_SESSION_BUS_ADDRESS="+s.DBusAddr,
		"PATH="+pathVal,
		"HAYAMI_SHIM_LOG_DIR="+s.ShimLogDir,
		"HAYAMI_SHIM_STATE_DIR="+s.ShimStateDir,
	)
	if sqliteLibPath != "" {
		env = append(env, "MYGO_SQLITE_LIBRARY="+sqliteLibPath)
	}

	if headless {
		env = append(env, "DISPLAY=", "WAYLAND_DISPLAY=")
	} else {
		if origDisplay != "" {
			env = append(env, "DISPLAY="+origDisplay)
		}
		if origWaylandDisplay != "" {
			wayland := origWaylandDisplay
			if !filepath.IsAbs(wayland) && origXDGRuntimeDir != "" {
				wayland = filepath.Join(origXDGRuntimeDir, wayland)
			}
			env = append(env, "WAYLAND_DISPLAY="+wayland)
		}
		if origXAuthority != "" {
			env = append(env, "XAUTHORITY="+origXAuthority)
		} else if origHome != "" {
			candidate := filepath.Join(origHome, ".Xauthority")
			if _, err := os.Stat(candidate); err == nil {
				env = append(env, "XAUTHORITY="+candidate)
			}
		}
	}

	for k, v := range s.ExtraEnv {
		env = append(env, k+"="+v)
	}
	env = append(env, extra...)
	return env
}

type CLIResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

func (s *sandbox) runCLI(stdin string, timeout time.Duration, args ...string) CLIResult {
	s.T.Helper()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, hayamiBin, args...)
	cmd.Env = s.buildEnv(true)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	return CLIResult{
		ExitCode: code,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Err:      err,
	}
}

func (s *sandbox) runForwardCLI(timeout time.Duration, args ...string) CLIResult {
	s.T.Helper()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, hayamiBin, args...)
	cmd.Env = s.buildEnv(false)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	return CLIResult{
		ExitCode: code,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Err:      err,
	}
}

type GUIProcess struct {
	Cmd        *exec.Cmd
	SocketPath string
	Stdout     *bytes.Buffer
	Stderr     *bytes.Buffer
	Done       chan error
	exited     atomic.Bool
}

func (s *sandbox) requireDisplay() {
	s.T.Helper()
	if origDisplay == "" && origWaylandDisplay == "" {
		s.T.Skip("skipping GUI test: DISPLAY and WAYLAND_DISPLAY are both unset")
	}
}

func (s *sandbox) startGUI(args ...string) *GUIProcess {
	s.T.Helper()
	s.requireDisplay()

	sockPath := filepath.Join(s.RuntimeDir, "auto.sock")
	_ = os.Remove(sockPath)

	cmd := exec.Command(hayamiBin, args...)
	cmd.Env = s.buildEnv(false, "MYGO_AUTOMATION="+sockPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		s.T.Fatalf("startGUI %v: %v", args, err)
	}

	done := make(chan error, 1)
	proc := &GUIProcess{
		Cmd:        cmd,
		SocketPath: sockPath,
		Stdout:     &stdout,
		Stderr:     &stderr,
		Done:       done,
	}
	go func() {
		err := cmd.Wait()
		proc.exited.Store(true)
		done <- err
	}()

	s.T.Cleanup(func() {
		proc.Stop(s.T)
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			// Put result back so Stop/Wait callers don't block.
			done <- err
			s.T.Fatalf("GUI process exited early (%v)\nstdout:\n%s\nstderr:\n%s\ndebug.log:\n%s",
				err, stdout.String(), stderr.String(), s.readDebugLog())
		default:
		}
		conn, err := net.DialTimeout("unix", sockPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return proc
		}
		time.Sleep(25 * time.Millisecond)
	}

	proc.Stop(s.T)
	s.T.Fatalf("timed out waiting for MYGO_AUTOMATION socket %s\nstdout:\n%s\nstderr:\n%s\ndebug.log:\n%s",
		sockPath, stdout.String(), stderr.String(), s.readDebugLog())
	return nil
}

func (g *GUIProcess) Stop(t *testing.T) {
	t.Helper()
	if g == nil || g.Cmd == nil || g.Cmd.Process == nil {
		return
	}
	if g.exited.Load() {
		return
	}
	select {
	case err := <-g.Done:
		g.Done <- err
		return
	default:
	}

	_ = g.Cmd.Process.Signal(os.Interrupt)
	select {
	case err := <-g.Done:
		g.Done <- err
		return
	case <-time.After(2 * time.Second):
	}

	_ = g.Cmd.Process.Kill()
	select {
	case err := <-g.Done:
		g.Done <- err
	case <-time.After(2 * time.Second):
	}
}

func defaultLoopbackConfig(baseURL, model string) map[string]any {
	if model == "" {
		model = "gpt-4o-mini"
	}
	return map[string]any{
		"$schema":          "./config.schema.json",
		"baseURL":          baseURL,
		"model":            model,
		"targetLang":       "English",
		"sourceLang":       "自动检测",
		"systemPrompt":     "你是专业翻译。把用户提供的文本翻译成{{target}}。只输出译文，不要解释。",
		"hotkey":           "Alt+F",
		"screenshotHotkey": "Alt+S",
		"inputHotkey":      "Alt+D",
		"temperature":      0.3,
		"maxTokens":        2048,
		"popupPinned":      false,
		"theme":            "system",
		"activeMode":       "translate",
		"providers": []map[string]any{
			{
				"id":      "openai",
				"name":    "OpenAI Compatible",
				"kind":    "openai",
				"enabled": true,
				"baseURL": baseURL,
				"model":   model,
				"vision":  true,
			},
		},
		"providersConfigured": true,
	}
}

func (s *sandbox) writeConfigJSON(cfg any) string {
	s.T.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		s.T.Fatalf("marshal config.json: %v", err)
	}
	data = append(data, '\n')
	return s.writeRawConfig(string(data))
}

func (s *sandbox) writeRawConfig(raw string) string {
	s.T.Helper()
	p := s.configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.T.Fatalf("mkdir config dir: %v", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(raw), 0o600); err != nil {
		s.T.Fatalf("write temp config.json: %v", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		s.T.Fatalf("rename config.json: %v", err)
	}
	return p
}

type HistoryRow struct {
	ID        int64
	CreatedAt int64
	Source    string
	Result    string
	Target    string
	Mode      string
}

func (s *sandbox) openHistoryDB(readOnly bool) *sqlite.DB {
	s.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := sqlite.Open(ctx, s.historyDBPath(), sqlite.OpenOptions{
		ReadOnly:    readOnly,
		BusyTimeout: 3 * time.Second,
	})
	if err != nil {
		s.T.Fatalf("sqlite.Open(%s): %v", s.historyDBPath(), err)
	}
	return db
}

func (s *sandbox) seedLegacyHistory(rows []HistoryRow) {
	s.T.Helper()
	db := s.openHistoryDB(false)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stmts := make([]sqlite.Statement, 0, len(rows)+1)
	stmts = append(stmts, sqlite.Statement{
		SQL: `CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at INTEGER NOT NULL,
			source TEXT NOT NULL,
			result TEXT NOT NULL,
			target TEXT NOT NULL,
			mode TEXT NOT NULL
		)`,
	})
	for _, r := range rows {
		ts := r.CreatedAt
		if ts == 0 {
			ts = time.Now().Unix()
		}
		stmts = append(stmts, sqlite.Statement{
			SQL:  `INSERT INTO history (created_at, source, result, target, mode) VALUES (?, ?, ?, ?, ?)`,
			Args: []any{ts, r.Source, r.Result, r.Target, r.Mode},
		})
	}
	if _, err := db.Transaction(ctx, stmts); err != nil {
		s.T.Fatalf("seedLegacyHistory transaction: %v", err)
	}
}

func (s *sandbox) queryHistoryRows(sqlQuery string, args ...any) sqlite.Rows {
	s.T.Helper()
	db := s.openHistoryDB(true)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := db.Query(ctx, sqlQuery, args...)
	if err != nil {
		s.T.Fatalf("queryHistoryRows(%q): %v", sqlQuery, err)
	}
	return rows
}

func (s *sandbox) countHistoryRows() int64 {
	s.T.Helper()
	rows := s.queryHistoryRows(`SELECT COUNT(*) FROM history`)
	if len(rows.Values) == 0 || len(rows.Values[0]) == 0 {
		return 0
	}
	n, _ := rows.Values[0][0].(int64)
	return n
}

func (s *sandbox) enableShims() {
	s.IsolatePATH = true
}

func (s *sandbox) setShimPATHOnly() {
	s.IsolatePATH = true
}

func (s *sandbox) setShimState(key, val string) {
	s.T.Helper()
	p := filepath.Join(s.ShimStateDir, key)
	if err := os.WriteFile(p, []byte(val), 0o644); err != nil {
		s.T.Fatalf("write shim state %s: %v", key, err)
	}
	s.ExtraEnv[key] = val
}

func (s *sandbox) readShimLog(tool string) string {
	b, err := os.ReadFile(filepath.Join(s.ShimLogDir, tool+".log"))
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *sandbox) writeShim(name, body string) string {
	s.T.Helper()
	s.enableShims()
	p := filepath.Join(s.BinDir, name)
	if !strings.HasPrefix(body, "#!") {
		body = "#!/bin/sh\n" + body
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		s.T.Fatalf("write shim %s: %v", p, err)
	}
	return p
}

// installShim writes a deterministic POSIX shell shim for one of the 11 external
// tools used on Linux. Scripts invoke /bin/* utilities by absolute path so they
// work reliably when IsolatePATH sets PATH strictly to BinDir.
func (s *sandbox) installShim(tool string) string {
	s.T.Helper()
	s.enableShims()

	const helperHeader = `#!/bin/sh
LOG_FILE="${HAYAMI_SHIM_LOG_DIR}/%s.log"
STATE_DIR="${HAYAMI_SHIM_STATE_DIR}"
get_state() {
  if [ -f "$STATE_DIR/$1" ]; then
    /bin/cat "$STATE_DIR/$1"
  else
    eval "printf '%%s' \"\${$1:-}\""
  fi
}
log_call() {
  printf '%%s\t%%s\n' "%s" "$*" >> "$LOG_FILE"
}
`
	header := fmt.Sprintf(helperHeader, tool, tool)

	switch tool {
	case "wl-paste":
		return s.writeShim(tool, header+`log_call "$@"
if [ "${1:-}" = "--primary" ] && [ "${2:-}" = "--watch" ]; then
  shift 2
  crash="$(get_state wl_watch_crash)"
  if [ -n "$crash" ]; then
    exit 1
  fi
  trap 'exit 0' TERM INT
  last_seq="$(get_state wl_watch_seq)"
  while true; do
    cur_seq="$(get_state wl_watch_seq)"
    if [ "$cur_seq" != "$last_seq" ] && [ -n "$cur_seq" ]; then
      last_seq="$cur_seq"
      "$@"
    fi
    /bin/sleep 0.05
  done
fi
mode="$(get_state wl_paste_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    get_state wl_paste_text
    exit 0
    ;;
  empty)
    exit 0
    ;;
  nothing)
    printf 'Nothing is copied\n' >&2
    exit 1
    ;;
  nontext)
    printf 'Clipboard content is not available as text\n' >&2
    exit 1
    ;;
  error)
    printf 'Failed to connect to a Wayland server\n' >&2
    exit 1
    ;;
  *)
    printf 'unknown wl-paste mode: %s\n' "$mode" >&2
    exit 1
    ;;
esac
`)

	case "xclip":
		return s.writeShim(tool, header+`log_call "$@"
mode="$(get_state xclip_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    get_state xclip_text
    exit 0
    ;;
  empty)
    printf 'Error: target STRING not available\n' >&2
    exit 1
    ;;
  error)
    printf "Error: Can't open display\n" >&2
    exit 1
    ;;
  *)
    exit 1
    ;;
esac
`)

	case "xsel":
		return s.writeShim(tool, header+`log_call "$@"
mode="$(get_state xsel_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    get_state xsel_text
    exit 0
    ;;
  empty)
    exit 1
    ;;
  error)
    printf "xsel: Can't open display\n" >&2
    exit 1
    ;;
  *)
    exit 1
    ;;
esac
`)

	case "spectacle":
		return s.writeShim(tool, header+`log_call "$@"
out_path=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--output" ] || [ "$prev" = "-o" ]; then
    out_path="$arg"
    break
  fi
  prev="$arg"
done
mode="$(get_state spectacle_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    fix="$(get_state fixture_png)"
    [ -n "$out_path" ] && [ -n "$fix" ] && /bin/cp "$fix" "$out_path"
    exit 0
    ;;
  cancel)
    exit 0
    ;;
  hang)
    exec /bin/sleep 30
    ;;
  error)
    exit 42
    ;;
  *)
    exit 1
    ;;
esac
`)

	case "slurp":
		return s.writeShim(tool, header+`log_call "$@"
mode="$(get_state slurp_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    reg="$(get_state slurp_region)"
    [ -z "$reg" ] && reg="10,20 100x80"
    printf '%s\n' "$reg"
    exit 0
    ;;
  cancel)
    exit 1
    ;;
  error)
    printf 'slurp failed\n' >&2
    exit 1
    ;;
  *)
    exit 1
    ;;
esac
`)

	case "grim":
		return s.writeShim(tool, header+`log_call "$@"
out_path=""
for arg in "$@"; do
  out_path="$arg"
done
mode="$(get_state grim_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    if [ "${1:-}" = "-g" ] && [ -n "${2:-}" ] && [ -n "$out_path" ]; then
      fix="$(get_state fixture_png)"
      [ -n "$fix" ] && /bin/cp "$fix" "$out_path"
      exit 0
    fi
    exit 1
    ;;
  *)
    exit 1
    ;;
esac
`)

	case "gnome-screenshot":
		return s.writeShim(tool, header+`log_call "$@"
out_path=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-f" ]; then
    out_path="$arg"
    break
  fi
  prev="$arg"
done
mode="$(get_state gnome_screenshot_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    fix="$(get_state fixture_png)"
    [ -n "$out_path" ] && [ -n "$fix" ] && /bin/cp "$fix" "$out_path"
    exit 0
    ;;
  cancel)
    exit 0
    ;;
  *)
    exit 1
    ;;
esac
`)

	case "flameshot":
		return s.writeShim(tool, header+`log_call "$@"
mode="$(get_state flameshot_mode)"
[ -z "$mode" ] && mode="ok"
case "$mode" in
  ok)
    if [ "${1:-}" = "gui" ] && [ "${2:-}" = "--raw" ]; then
      fix="$(get_state fixture_png)"
      [ -n "$fix" ] && /bin/cat "$fix"
      exit 0
    fi
    exit 1
    ;;
  cancel)
    exit 0
    ;;
  *)
    printf 'flameshot failed\n' >&2
    exit 1
    ;;
esac
`)

	case "piper":
		return s.writeShim(tool, header+`stdin_data="$(/bin/cat)"
printf '%s\t%s\tstdin=%s\n' "piper" "$*" "$stdin_data" >> "$LOG_FILE"
out_file=""
raw_out=0
prev=""
for arg in "$@"; do
  if [ "$prev" = "--output_file" ] || [ "$prev" = "-f" ]; then
    out_file="$arg"
  fi
  if [ "$arg" = "--output_raw" ]; then
    raw_out=1
  fi
  prev="$arg"
done
mode="$(get_state piper_mode)"
[ -z "$mode" ] && mode="ok"
if [ "$mode" = "slow" ]; then
  exec /bin/sleep 5
fi
if [ "$mode" = "error" ]; then
  printf 'piper failed\n' >&2
  exit 1
fi
if [ -n "$out_file" ]; then
  fix="$(get_state fixture_wav)"
  [ -n "$fix" ] && /bin/cp "$fix" "$out_file"
elif [ "$raw_out" = "1" ]; then
  fix="$(get_state fixture_pcm)"
  [ -n "$fix" ] && /bin/cat "$fix"
fi
exit 0
`)

	case "espeak-ng":
		return s.writeShim(tool, header+`stdin_data="$(/bin/cat)"
printf '%s\t%s\tstdin=%s\n' "espeak-ng" "$*" "$stdin_data" >> "$LOG_FILE"
out_file=""
to_stdout=0
prev=""
for arg in "$@"; do
  if [ "$prev" = "-w" ]; then
    out_file="$arg"
  fi
  if [ "$arg" = "--stdout" ]; then
    to_stdout=1
  fi
  prev="$arg"
done
mode="$(get_state espeak_mode)"
[ -z "$mode" ] && mode="ok"
if [ "$mode" = "slow" ]; then
  exec /bin/sleep 5
fi
if [ "$mode" = "error" ]; then
  printf 'espeak-ng failed\n' >&2
  exit 1
fi
fix="$(get_state fixture_wav)"
if [ -n "$out_file" ] && [ -n "$fix" ]; then
  /bin/cp "$fix" "$out_file"
elif [ "$to_stdout" = "1" ] && [ -n "$fix" ]; then
  /bin/cat "$fix"
fi
exit 0
`)

	case "spd-say":
		return s.writeShim(tool, header+`stdin_data=""
if [ ! -t 0 ]; then
  stdin_data="$(/bin/cat)"
fi
printf '%s\t%s\tstdin=%s\n' "spd-say" "$*" "$stdin_data" >> "$LOG_FILE"
mode="$(get_state spd_mode)"
[ -z "$mode" ] && mode="ok"
if [ "$mode" = "slow" ]; then
  exec /bin/sleep 5
fi
if [ "$mode" = "error" ]; then
  printf 'spd-say failed\n' >&2
  exit 1
fi
exit 0
`)

	default:
		s.T.Fatalf("unknown shim tool %q", tool)
		return ""
	}
}

func (s *sandbox) installAllShims() {
	s.T.Helper()
	for _, tool := range []string{
		"wl-paste", "xclip", "xsel",
		"spectacle", "slurp", "grim", "gnome-screenshot", "flameshot",
		"piper", "espeak-ng", "spd-say",
	} {
		s.installShim(tool)
	}
}

func makePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	if width <= 0 || height <= 0 {
		t.Fatalf("makePNG invalid dimensions %dx%d", width, height)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	stepX := max(1, width/16)
	stepY := max(1, height/16)
	for y := 0; y < height; y += stepY {
		for x := 0; x < width; x += stepX {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 180, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode %dx%d: %v", width, height, err)
	}
	return buf.Bytes()
}

func makeWAV(duration time.Duration) []byte {
	const sampleRate = 22050
	const channels = 1
	const bitsPerSample = 16
	frames := max(1, int(duration*sampleRate/time.Second))
	dataBytes := frames * channels * (bitsPerSample / 8)
	out := make([]byte, 44+dataBytes)
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+dataBytes))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1)
	binary.LittleEndian.PutUint16(out[22:24], channels)
	binary.LittleEndian.PutUint32(out[24:28], sampleRate)
	binary.LittleEndian.PutUint32(out[28:32], sampleRate*channels*(bitsPerSample/8))
	binary.LittleEndian.PutUint16(out[32:34], channels*(bitsPerSample/8))
	binary.LittleEndian.PutUint16(out[34:36], bitsPerSample)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], uint32(dataBytes))
	for i := 0; i < frames; i++ {
		val := int16(-1000)
		if (i/25)%2 == 0 {
			val = 1000
		}
		binary.LittleEndian.PutUint16(out[44+i*2:46+i*2], uint16(val))
	}
	return out
}

func makeMP3(frames int) []byte {
	const frameSize = 104
	if frames < 1 {
		frames = 1
	}
	out := make([]byte, frames*frameSize)
	for i := 0; i < frames; i++ {
		off := i * frameSize
		out[off+0] = 0xff
		out[off+1] = 0xfb // MPEG-1 Layer III, no CRC
		out[off+2] = 0x10 // 32 kbps, 44100 Hz, no padding
		out[off+3] = 0xc0 // Mono
	}
	return out
}

func TestHarness_SandboxAndFixtures(t *testing.T) {
	s := newSandbox(t)

	if runtime.GOOS != "windows" {
		sock := s.singleInstanceSocketPath()
		if len(sock) >= 100 {
			t.Fatalf("singleInstanceSocketPath %q length = %d, want < 100", sock, len(sock))
		}
	}

	s.seedLegacyHistory([]HistoryRow{
		{CreatedAt: 1700000000, Source: "hello", Result: "你好", Target: "简体中文", Mode: "translate"},
		{CreatedAt: 1700000001, Source: "world", Result: "世界", Target: "简体中文", Mode: "translate"},
	})
	if got := s.countHistoryRows(); got != 2 {
		t.Fatalf("countHistoryRows = %d, want 2", got)
	}
	rows := s.queryHistoryRows(`SELECT source, result FROM history ORDER BY id ASC`)
	if len(rows.Values) != 2 || rows.Values[0][0] != "hello" || rows.Values[1][1] != "世界" {
		t.Fatalf("unexpected history query rows: %#v", rows.Values)
	}

	pngBytes := makePNG(t, 64, 32)
	cfg, fmtName, err := image.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil || fmtName != "png" || cfg.Width != 64 || cfg.Height != 32 {
		t.Fatalf("makePNG decode failed: cfg=%+v fmt=%q err=%v", cfg, fmtName, err)
	}

	wav := makeWAV(100 * time.Millisecond)
	if len(wav) <= 44 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("invalid WAV fixture header, len=%d", len(wav))
	}

	mp3 := makeMP3(4)
	if len(mp3) != 4*104 || mp3[0] != 0xff || mp3[1] != 0xfb {
		t.Fatalf("invalid MP3 fixture, len=%d", len(mp3))
	}

	if runtime.GOOS != "windows" {
		s.installAllShims()
		s.setShimState("wl_paste_mode", "ok")
		s.setShimState("wl_paste_text", "copied text")

		cmd := exec.Command(filepath.Join(s.BinDir, "wl-paste"), "--primary", "--no-newline", "--type", "text")
		cmd.Env = s.buildEnv(true)
		out, err := cmd.Output()
		if err != nil || string(out) != "copied text" {
			t.Fatalf("wl-paste shim output = %q, err = %v", string(out), err)
		}
		if !strings.Contains(s.readShimLog("wl-paste"), "--primary --no-newline --type text") {
			t.Fatalf("wl-paste shim log missing invocation: %q", s.readShimLog("wl-paste"))
		}

		// Verify PATH isolation prevents falling back to host /usr/bin/xclip when not installed.
		s2 := newSandbox(t)
		s2.installShim("wl-paste")
		if _, err := exec.LookPath("xclip"); err == nil {
			check := exec.Command("/bin/sh", "-c", "command -v xclip || true")
			check.Env = s2.buildEnv(true)
			res, err := check.Output()
			if err != nil {
				t.Fatalf("isolated PATH check failed: %v", err)
			}
			if strings.TrimSpace(string(res)) != "" {
				t.Fatalf("host xclip leaked into isolated PATH: %q", string(res))
			}
		}

		// Verify slow/hang shims exec /bin/sleep directly so CommandContext cancellation
		// terminates immediately without orphaned sleep processes holding pipes open.
		for _, tc := range []struct {
			name     string
			stateKey string
			stateVal string
			tool     string
			args     []string
		}{
			{name: "espeak-ng_slow", stateKey: "espeak_mode", stateVal: "slow", tool: "espeak-ng", args: []string{"--stdin"}},
			{name: "spectacle_hang", stateKey: "spectacle_mode", stateVal: "hang", tool: "spectacle", args: []string{"-b", "-n", "-o", "out.png"}},
		} {
			s.setShimState(tc.stateKey, tc.stateVal)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			c := exec.CommandContext(ctx, filepath.Join(s.BinDir, tc.tool), tc.args...)
			c.Env = s.buildEnv(true)
			c.Stdin = strings.NewReader("hello")
			start := time.Now()
			_, err := c.CombinedOutput()
			elapsed := time.Since(start)
			cancel()
			if err == nil {
				t.Fatalf("%s unexpectedly succeeded under 150ms cancellation", tc.name)
			}
			if elapsed >= time.Second {
				t.Fatalf("%s cancellation took %v (want < 1s; leaked sleep holding pipes)", tc.name, elapsed)
			}
		}
	}
}
