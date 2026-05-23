package main

import (
	"bufio"
	stdctx "context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

//go:embed help.txt
var helpText string

var version = "dev"

// scopeMode determines whether to use a local or global state directory.
type scopeMode int

const (
	scopeAuto   scopeMode = iota // auto-detect: local if .rodney/state.json exists in cwd, else global
	scopeLocal                   // force local (./.rodney/)
	scopeGlobal                  // force global (~/.rodney/)
)

// activeStateDir is set once at startup based on --local/--global flags.
var activeStateDir string

// extractScopeArgs scans args for --local/--global, removes them, and returns the mode.
// If both appear, the last one wins.
func extractScopeArgs(args []string) (scopeMode, []string) {
	mode := scopeAuto
	var filtered []string
	for _, arg := range args {
		switch arg {
		case "--local":
			mode = scopeLocal
		case "--global":
			mode = scopeGlobal
		default:
			filtered = append(filtered, arg)
		}
	}
	return mode, filtered
}

// resolveStateDir determines the state directory based on scope mode and working directory.
func resolveStateDir(mode scopeMode, workingDir string) string {
	switch mode {
	case scopeLocal:
		return filepath.Join(workingDir, ".rodney")
	case scopeGlobal:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	default: // scopeAuto
		localDir := filepath.Join(workingDir, ".rodney")
		if _, err := os.Stat(filepath.Join(localDir, "state.json")); err == nil {
			return localDir
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	}
}

// CallRecord tracks a single command invocation for repeated-failure detection.
type CallRecord struct {
	Cmd      string `json:"cmd"`
	Selector string `json:"sel,omitempty"`
	OK       bool   `json:"ok"`
	Error    string `json:"err,omitempty"`
	TS       int64  `json:"ts"`
}

// State persisted between CLI invocations
type State struct {
	DebugURL    string `json:"debug_url"`
	ChromePID   int    `json:"chrome_pid"`
	ActivePage  int    `json:"active_page"`  // index into pages list
	DataDir     string `json:"data_dir"`
	ProxyPID    int    `json:"proxy_pid,omitempty"`   // PID of auth proxy helper
	ProxyPort   int    `json:"proxy_port,omitempty"`  // local port of auth proxy
	Logs        bool   `json:"logs,omitempty"`        // console log capture enabled
	LoggerPID   int    `json:"logger_pid,omitempty"`  // PID of _logger subprocess

	Stealth bool `json:"stealth,omitempty"` // stealth mode: remove automation fingerprints

	// Viewport overrides (set by "rodney viewport", re-applied on each connection)
	ViewportWidth  int     `json:"viewport_width,omitempty"`
	ViewportHeight int     `json:"viewport_height,omitempty"`
	ViewportScale  float64 `json:"viewport_scale,omitempty"`
	ViewportMobile bool    `json:"viewport_mobile,omitempty"`

	// Ring buffer of recent command results for repeated-failure detection
	RecentCalls []CallRecord `json:"recent_calls,omitempty"`
}

func stateDir() string {
	if dir := os.Getenv("RODNEY_HOME"); dir != "" {
		return dir
	}
	if activeStateDir != "" {
		return activeStateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}

func statePath() string {
	return filepath.Join(stateDir(), "state.json")
}

func loadState() (*State, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return nil, fmt.Errorf("no browser session (run 'rodney start' first)")
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("corrupt state file: %w", err)
	}
	return &s, nil
}

func saveState(s *State) error {
	if err := os.MkdirAll(stateDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(), data, 0644)
}

func removeState() {
	os.Remove(statePath())
}

// connectBrowser connects to the running Chrome instance
func connectBrowser(s *State) (*rod.Browser, error) {
	browser := rod.New().ControlURL(s.DebugURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to browser (is it still running?): %w", err)
	}
	return browser, nil
}

// getActivePage returns the currently active page
func getActivePage(browser *rod.Browser, s *State) (*rod.Page, error) {
	pages, err := browser.Pages()
	if err != nil {
		return nil, fmt.Errorf("failed to list pages: %w", err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no pages open")
	}
	idx := s.ActivePage
	if idx < 0 || idx >= len(pages) {
		idx = 0
	}
	return pages[idx], nil
}

func printUsage() {
	fmt.Print(helpText)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(2)
}

func hint(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "hint: "+format+"\n", args...)
}

// isGVisor reports whether the current process is running under gVisor.
// Chrome's multi-process compositor hangs under gVisor's seccomp+ptrace
// syscall interception, so screenshots require --single-process there.
// Detection reads /proc/version, which gVisor populates with a signature
// like "Linux version 4.4.0 ... #1 SMP Sun Jan 10 15:06:54 PST 2016".
// The reliable marker is the kernel release string "gvisor" shown by
// uname -a under runsc. Returns false on non-Linux.
func isGVisor() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "gvisor")
}

// context writes a contextual hint to stderr to help agents self-correct.
func context(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "context: "+format+"\n", args...)
}

// inspectFailure runs a single JavaScript snippet on the page to gather context
// about why an element operation failed. It writes findings as context: lines to
// stderr. It must not panic or fatal if the inspection itself fails.
func inspectFailure(page *rod.Page, selector string) {
	// Budget 200ms for the entire inspection
	ctx, cancel := stdctx.WithTimeout(stdctx.Background(), 200*time.Millisecond)
	defer cancel()
	shortPage := page.Context(ctx)

	jsSnippet := fmt.Sprintf(`() => {
  var selector = %q;
  var result = {};
  result.readyState = document.readyState;
  result.url = location.href;
  result.title = document.title;

  // Check if selector matches but is hidden
  try {
    var el = document.querySelector(selector);
    if (el) {
      var style = getComputedStyle(el);
      var rect = el.getBoundingClientRect();
      result.hidden = {
        display: style.display,
        visibility: style.visibility,
        opacity: style.opacity,
        width: rect.width,
        height: rect.height
      };
    }
  } catch(e) {}

  // Find similar interactive elements
  try {
    var interactive = document.querySelectorAll(
      'button, a[href], input, select, textarea, [role="button"], [role="link"], [tabindex]'
    );
    result.available = Array.from(interactive).slice(0, 20).map(function(el) {
      return {
        tag: el.tagName.toLowerCase(),
        id: el.id || '',
        classes: el.className || '',
        text: (el.textContent || '').trim().slice(0, 50),
        type: el.type || '',
        name: el.name || ''
      };
    });
  } catch(e) { result.available = []; }

  // Check for overlay at center of viewport
  try {
    var cx = window.innerWidth / 2, cy = window.innerHeight / 2;
    var topEl = document.elementFromPoint(cx, cy);
    if (topEl) {
      var tz = getComputedStyle(topEl).zIndex;
      if (tz !== 'auto' && parseInt(tz) > 100) {
        result.overlay = {
          tag: topEl.tagName.toLowerCase(),
          id: topEl.id || '',
          classes: topEl.className || '',
          zIndex: tz
        };
      }
    }
  } catch(e) {}

  // Check for auth patterns in URL
  result.authPattern = /login|signin|sign-in|auth|sso|oauth/i.test(location.href) ||
                       /login|sign.in/i.test(document.title);

  return JSON.stringify(result);
}`, selector)

	result, err := shortPage.Eval(jsSnippet)
	if err != nil {
		return
	}

	raw := result.Value.Str()
	if raw == "" {
		return
	}

	var data struct {
		ReadyState  string `json:"readyState"`
		URL         string `json:"url"`
		Title       string `json:"title"`
		AuthPattern bool   `json:"authPattern"`
		Hidden      *struct {
			Display    string  `json:"display"`
			Visibility string  `json:"visibility"`
			Opacity    string  `json:"opacity"`
			Width      float64 `json:"width"`
			Height     float64 `json:"height"`
		} `json:"hidden"`
		Available []struct {
			Tag     string `json:"tag"`
			ID      string `json:"id"`
			Classes string `json:"classes"`
			Text    string `json:"text"`
			Type    string `json:"type"`
			Name    string `json:"name"`
		} `json:"available"`
		Overlay *struct {
			Tag     string `json:"tag"`
			ID      string `json:"id"`
			Classes string `json:"classes"`
			ZIndex  string `json:"zIndex"`
		} `json:"overlay"`
	}

	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return
	}

	// Hidden element detection
	if data.Hidden != nil {
		if data.Hidden.Display == "none" {
			context("'%s' exists but is hidden (display: none) — try 'rodney wait \"%s\"'", selector, selector)
		} else if data.Hidden.Visibility == "hidden" {
			context("'%s' exists but is hidden (visibility: hidden) — try 'rodney wait \"%s\"'", selector, selector)
		} else if data.Hidden.Opacity == "0" {
			context("'%s' exists but is hidden (opacity: 0) — try 'rodney wait \"%s\"'", selector, selector)
		} else if data.Hidden.Width == 0 && data.Hidden.Height == 0 {
			context("'%s' exists but is hidden (zero dimensions) — try 'rodney wait \"%s\"'", selector, selector)
		}
	}

	// Page still loading
	if data.ReadyState != "complete" {
		context("page is still loading (readyState: %s) — try 'rodney waitstable'", data.ReadyState)
	}

	// Auth redirect detection
	if data.AuthPattern {
		context("current URL appears to be a login page (%s)", data.URL)
	}

	// Overlay detection
	if data.Overlay != nil {
		overlayDesc := data.Overlay.Tag
		if data.Overlay.Classes != "" {
			overlayDesc += "." + strings.SplitN(data.Overlay.Classes, " ", 2)[0]
		}
		context("a modal/overlay may be blocking the page (%s, z-index: %s)", overlayDesc, data.Overlay.ZIndex)
	}

	// Fuzzy matching on available elements
	if len(data.Available) > 0 {
		matched := false
		if strings.HasPrefix(selector, "#") {
			target := strings.TrimPrefix(selector, "#")
			for _, el := range data.Available {
				if el.ID != "" && el.ID != target && strings.Contains(el.ID, target) {
					context("did you mean '#%s'?", el.ID)
					matched = true
				}
			}
		}
		if !matched {
			context("page has %d interactive elements — try 'rodney discover --interactive'", len(data.Available))
		}
	}
}

// observationCmds lists commands that don't break failure streaks.
var observationCmds = map[string]bool{
	"url": true, "title": true, "text": true, "html": true,
	"screenshot": true, "screenshot-el": true, "exists": true,
	"visible": true, "count": true, "ax-tree": true, "ax-find": true,
	"ax-node": true, "discover": true, "pages": true, "status": true,
	"logs": true, "pdf": true, "attr": true,
}

// recordCall appends a CallRecord to state and trims to the last 10 entries.
func recordCall(cmd, selector string, ok bool, errMsg string) {
	s, err := loadState()
	if err != nil {
		return
	}
	s.RecentCalls = append(s.RecentCalls, CallRecord{
		Cmd:      cmd,
		Selector: selector,
		OK:       ok,
		Error:    errMsg,
		TS:       time.Now().Unix(),
	})
	if len(s.RecentCalls) > 10 {
		s.RecentCalls = s.RecentCalls[len(s.RecentCalls)-10:]
	}
	_ = saveState(s)
}

// checkStuck walks RecentCalls backwards counting consecutive identical failures.
func checkStuck(cmd, selector string) int {
	s, err := loadState()
	if err != nil {
		return 0
	}
	count := 0
	for i := len(s.RecentCalls) - 1; i >= 0; i-- {
		r := s.RecentCalls[i]
		if observationCmds[r.Cmd] {
			continue
		}
		if r.OK {
			break
		}
		if r.Cmd == cmd && r.Selector == selector {
			count++
		} else {
			break
		}
	}
	return count
}

// reportStuck writes escalating context to stderr based on failure count.
func reportStuck(count int) {
	if count <= 1 {
		return
	}
	if count == 2 {
		context("this command has failed %d times with the same error — try a different selector", count)
		return
	}
	context("STUCK — this command has failed %d times in a row", count)
	context("stop retrying and try a different approach:")
	context("  rodney discover --interactive")
	context("  rodney ax-find --role button")
	context("  rodney waitstable")
}

// findUnknownFlag returns the first arg not registered in fs, preserving original form (e.g. --bogus).
func findUnknownFlag(args []string, fs *flag.FlagSet) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if fs.Lookup(name) == nil {
			return a
		}
	}
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	// Extract --local/--global from all args before dispatching
	mode, cleanedArgs := extractScopeArgs(os.Args[1:])
	if len(cleanedArgs) == 0 {
		printUsage()
		os.Exit(1)
	}

	wd, _ := os.Getwd()
	activeStateDir = resolveStateDir(mode, wd)

	cmd := cleanedArgs[0]
	args := cleanedArgs[1:]

	if cmd == "--version" {
		fmt.Println(version)
		os.Exit(0)
	}

	switch cmd {
	case "_proxy":
		cmdInternalProxy(args) // hidden: runs the auth proxy helper
	case "_logger":
		cmdInternalLogger(args) // hidden: runs the browser console logger
	case "start":
		cmdStart(args)
	case "connect":
		cmdConnect(args)
	case "stop":
		cmdStop(args)
	case "status":
		cmdStatus(args)
	case "open":
		cmdOpen(args)
	case "back":
		cmdBack(args)
	case "forward":
		cmdForward(args)
	case "reload":
		cmdReload(args)
	case "clear-cache":
		cmdClearCache(args)
	case "url":
		cmdURL(args)
	case "title":
		cmdTitle(args)
	case "html":
		cmdHTML(args)
	case "text":
		cmdText(args)
	case "attr":
		cmdAttr(args)
	case "pdf":
		cmdPDF(args)
	case "js":
		cmdJS(args)
	case "click":
		cmdClick(args)
	case "input":
		cmdInput(args)
	case "clear":
		cmdClear(args)
	case "select":
		cmdSelect(args)
	case "submit":
		cmdSubmit(args)
	case "hover":
		cmdHover(args)
	case "file":
		cmdFile(args)
	case "download":
		cmdDownload(args)
	case "focus":
		cmdFocus(args)
	case "wait":
		cmdWait(args)
	case "waitload":
		cmdWaitLoad(args)
	case "waitstable":
		cmdWaitStable(args)
	case "waitidle":
		cmdWaitIdle(args)
	case "sleep":
		cmdSleep(args)
	case "screenshot":
		cmdScreenshot(args)
	case "screenshot-el":
		cmdScreenshotEl(args)
	case "viewport":
		cmdViewport(args)
	case "pages":
		cmdPages(args)
	case "page":
		cmdPage(args)
	case "newpage":
		cmdNewPage(args)
	case "closepage":
		cmdClosePage(args)
	case "exists":
		cmdExists(args)
	case "count":
		cmdCount(args)
	case "visible":
		cmdVisible(args)
	case "assert":
		cmdAssert(args)
	case "logs":
		cmdLogs(args)
	case "ax-tree":
		cmdAXTree(args)
	case "ax-find":
		cmdAXFind(args)
	case "ax-node":
		cmdAXNode(args)
	case "discover":
		cmdDiscover(args)
	case "check":
		cmdCheck(args)
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(2)
	}
}

// Default timeout for element queries (seconds)
var defaultTimeout = 30 * time.Second

func init() {
	if t := os.Getenv("ROD_TIMEOUT"); t != "" {
		if secs, err := strconv.ParseFloat(t, 64); err == nil {
			defaultTimeout = time.Duration(secs * float64(time.Second))
		}
	}
}

// withPage loads state, connects, and returns the active page.
// Caller should NOT close the browser (we just disconnect).
func withPage() (*State, *rod.Browser, *rod.Page) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	page, err := getActivePage(browser, s)
	if err != nil {
		fatal("%v", err)
	}
	// Apply default timeout so element queries don't hang forever
	page = page.Timeout(defaultTimeout)

	// Re-apply viewport override if set via "rodney viewport"
	if s.ViewportWidth > 0 && s.ViewportHeight > 0 {
		scale := s.ViewportScale
		if scale == 0 {
			scale = 1
		}
		if err := (proto.EmulationSetDeviceMetricsOverride{
			Width:             s.ViewportWidth,
			Height:            s.ViewportHeight,
			DeviceScaleFactor: scale,
			Mobile:            s.ViewportMobile,
		}.Call(page)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to re-apply viewport: %v\n", err)
		}
	}

	// Inject stealth script to hide automation fingerprints
	if s.Stealth {
		_, _ = proto.PageAddScriptToEvaluateOnNewDocument{
			Source: `Object.defineProperty(navigator, 'webdriver', {get: () => false});`,
		}.Call(page)
	}

	return s, browser, page
}

// formatViewportDesc returns a human-readable description of viewport settings.
func formatViewportDesc(prefix string, w, h int, mobile bool, scale float64) string {
	desc := fmt.Sprintf("%s %dx%d", prefix, w, h)
	var extras []string
	if mobile {
		extras = append(extras, "mobile")
	}
	if scale != 0 && scale != 1 {
		extras = append(extras, fmt.Sprintf("scale %g", scale))
	}
	if len(extras) > 0 {
		desc += " (" + strings.Join(extras, ", ") + ")"
	}
	return desc
}

// --- Commands ---

type startFlags struct {
	headless         bool
	ignoreCertErrors bool
	enableLogs       bool
	fakeMedia        bool
	stealth          bool
	singleProcess    string // "auto" (default), "on", "off"
	vpWidth          int
	vpHeight         int
	vpScale          float64
	vpMobile         bool
}

// parseStartFlags parses the arguments to "rodney start" using flag.FlagSet.
func parseStartFlags(args []string) (startFlags, error) {
	// Pre-extract --viewport WxH since flag.FlagSet can't handle that format
	var vpArg string
	var filtered []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--viewport" {
			i++
			if i >= len(args) {
				return startFlags{}, fmt.Errorf("missing value for --viewport (expected WxH, e.g. 375x812)")
			}
			vpArg = args[i]
		} else {
			filtered = append(filtered, args[i])
		}
	}

	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	show := fs.Bool("show", false, "")
	insecure := fs.Bool("insecure", false, "")
	k := fs.Bool("k", false, "")
	logs := fs.Bool("logs", false, "")
	fakeMedia := fs.Bool("fake-media", false, "")
	stealth := fs.Bool("stealth", false, "")
	mobile := fs.Bool("mobile", false, "")
	scale := fs.Float64("scale", 0, "")
	singleProcess := fs.String("single-process", "auto", "")

	usage := "usage: rodney start [--show] [--insecure | -k] [--logs] [--fake-media] [--stealth] [--single-process auto|on|off] [--viewport WxH] [--mobile] [--scale N]"

	if err := fs.Parse(filtered); err != nil {
		return startFlags{}, fmt.Errorf("unknown flag: %s\n%s", findUnknownFlag(filtered, fs), usage)
	}
	if fs.NArg() > 0 {
		return startFlags{}, fmt.Errorf("unknown flag: %s\n%s", fs.Arg(0), usage)
	}

	switch *singleProcess {
	case "auto", "on", "off":
	default:
		return startFlags{}, fmt.Errorf("invalid --single-process value %q (expected auto, on, or off)", *singleProcess)
	}

	f := startFlags{
		headless:         !*show,
		ignoreCertErrors: *insecure || *k,
		enableLogs:       *logs,
		fakeMedia:        *fakeMedia,
		stealth:          *stealth,
		singleProcess:    *singleProcess,
		vpMobile:         *mobile,
		vpScale:          *scale,
	}

	if vpArg != "" {
		parts := strings.SplitN(vpArg, "x", 2)
		if len(parts) != 2 {
			return f, fmt.Errorf("invalid viewport format: %q (expected WxH, e.g. 375x812)", vpArg)
		}
		w, err := strconv.Atoi(parts[0])
		if err != nil {
			return f, fmt.Errorf("invalid viewport width: %v", err)
		}
		h, err := strconv.Atoi(parts[1])
		if err != nil {
			return f, fmt.Errorf("invalid viewport height: %v", err)
		}
		f.vpWidth, f.vpHeight = w, h
	}

	return f, nil
}

func cmdStart(args []string) {
	flags, err := parseStartFlags(args)
	if err != nil {
		fatal("%s", err)
	}
	ignoreCertErrors := flags.ignoreCertErrors
	enableLogs := flags.enableLogs
	fakeMedia := flags.fakeMedia
	stealth := flags.stealth
	headless := flags.headless
	vpWidth, vpHeight := flags.vpWidth, flags.vpHeight
	vpScale := flags.vpScale
	vpMobile := flags.vpMobile

	if (vpMobile || vpScale != 0) && vpWidth == 0 {
		fatal("--mobile and --scale require --viewport")
	}

	if vpWidth > 0 && vpScale == 0 {
		vpScale = 1
	}

	// Check if already running
	if s, err := loadState(); err == nil {
		// Try connecting
		if b, err := connectBrowser(s); err == nil {
			b.MustClose()
			// It was actually running, warn
			removeState()
		}
	}

	dataDir := filepath.Join(stateDir(), "chrome-data")
	os.MkdirAll(dataDir, 0755)

	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Leakless(false). // Keep Chrome alive after CLI exits
		UserDataDir(dataDir).
		Headless(headless)

	// --single-process is required for screenshots under gVisor (its seccomp
	// shim breaks Chrome's multi-process compositor) but causes frequent
	// crashes on desktop. Auto-detect gVisor; allow explicit override.
	useSingleProcess := false
	switch flags.singleProcess {
	case "on":
		useSingleProcess = true
	case "off":
		useSingleProcess = false
	default: // "auto"
		useSingleProcess = isGVisor()
	}
	if useSingleProcess {
		l = l.Set("single-process")
	}

	// When in non-headless mode, make sure that we show the startup window immediately
	// (instead of showing a window only after calling "rodney open")
	if !headless {
		l = l.Delete("no-startup-window")
	}

	if stealth {
		l = l.Set("disable-blink-features", "AutomationControlled")
		l = l.Delete("enable-automation")
	}

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	} else if runtime.GOOS != "darwin" {
		// On macOS, Google Chrome.app enforces single-instance per bundle, so
		// launching it while the user's regular Chrome is running causes the
		// new process to be absorbed and exit. Prefer go-rod's auto-downloaded
		// Chromium there. On other platforms, reuse system Chrome/Chromium.
		if found, ok := launcher.LookPath(); ok {
			l = l.Bin(found)
		}
	}

	// Detect authenticated proxy and launch helper if needed
	var proxyPID, proxyPort int
	if server, user, pass, needed := detectProxy(); needed {
		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

		// Find a free port for the local proxy
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal("failed to find free port for proxy: %v", err)
		}
		proxyPort = ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		// Launch ourselves as the proxy helper in the background
		exe, _ := os.Executable()
		// nosemgrep: go.lang.security.audit.dangerous-exec-command -- exe is os.Executable() (our own binary); no shell, args are a literal subcommand, a locally-bound port, and operator HTTP(S)_PROXY config — not attacker input.
		cmd := exec.Command(exe, "_proxy",
			strconv.Itoa(proxyPort), server, authHeader)
		setSysProcAttr(cmd)
		if err := cmd.Start(); err != nil {
			fatal("failed to start proxy helper: %v", err)
		}
		proxyPID = cmd.Process.Pid
		// Detach so it survives after we exit
		cmd.Process.Release()

		// Wait for the proxy to be ready
		time.Sleep(500 * time.Millisecond)

		l.Set("proxy-server", fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
		ignoreCertErrors = true // Proxy requires ignoring cert errors
		fmt.Printf("Auth proxy started (PID %d, port %d) -> %s\n", proxyPID, proxyPort, server)
	}

	if ignoreCertErrors {
		l.Set("ignore-certificate-errors")
	}

	if fakeMedia {
		l.Set("use-fake-device-for-media-stream")
		l.Set("use-fake-ui-for-media-stream")
	}

	debugURL := l.MustLaunch()

	// Get Chrome PID from the launcher
	pid := l.PID()

	// Launch logger subprocess if --logs was specified
	var loggerPID int
	if enableLogs {
		logsDir := filepath.Join(stateDir(), "logs")
		os.MkdirAll(logsDir, 0755)
		exe, _ := os.Executable()
		// nosemgrep: go.lang.security.audit.dangerous-exec-command -- exe is os.Executable() (our own binary); no shell, args are a literal subcommand, the locally-generated DevTools URL, and our own state dir — not attacker input.
		cmd := exec.Command(exe, "_logger", debugURL, logsDir)
		setSysProcAttr(cmd)
		if err := cmd.Start(); err != nil {
			fatal("failed to start logger: %v", err)
		}
		loggerPID = cmd.Process.Pid
		cmd.Process.Release()
		fmt.Printf("Logger started (PID %d)\n", loggerPID)
	}

	state := &State{
		DebugURL:       debugURL,
		ChromePID:      pid,
		ActivePage:     0,
		DataDir:        dataDir,
		ProxyPID:       proxyPID,
		ProxyPort:      proxyPort,
		Logs:           enableLogs,
		LoggerPID:      loggerPID,
		Stealth:        stealth,
		ViewportWidth:  vpWidth,
		ViewportHeight: vpHeight,
		ViewportScale:  vpScale,
		ViewportMobile: vpMobile,
	}

	if err := saveState(state); err != nil {
		fatal("failed to save state: %v", err)
	}

	fmt.Printf("Chrome started (PID %d)\n", pid)
	fmt.Printf("Debug URL: %s\n", debugURL)
	if vpWidth > 0 && vpHeight > 0 {
		fmt.Println(formatViewportDesc("Viewport:", vpWidth, vpHeight, vpMobile, vpScale))
	}
}

func cmdConnect(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney connect <host:port>")
	}
	hostport := args[0]
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		fatal("argument must be host:port (e.g. localhost:9222): %s", hostport)
	}

	// Fetch the WebSocket debugger URL from Chrome's /json/version endpoint
	resp, err := http.Get("http://" + hostport + "/json/version")
	if err != nil {
		fatal("could not reach browser at %s: %v", hostport, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("failed to read response: %v", err)
	}
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.WebSocketDebuggerURL == "" {
		fatal("unexpected response from browser at %s", hostport)
	}

	// Verify the connection works
	browser := rod.New().ControlURL(info.WebSocketDebuggerURL)
	if err := browser.Connect(); err != nil {
		fatal("could not connect to browser: %v", err)
	}

	// ChromePID=0 signals that we don't own this browser (stop won't kill it)
	state := &State{
		DebugURL:   info.WebSocketDebuggerURL,
		ChromePID:  0,
		ActivePage: 0,
	}
	if err := saveState(state); err != nil {
		fatal("failed to save state: %v", err)
	}

	fmt.Printf("Connected to browser at %s\n", hostport)
	fmt.Printf("Debug URL: %s\n", info.WebSocketDebuggerURL)
}

func cmdStop(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		// Try to kill by PID only if we launched the browser
		if s.ChromePID > 0 {
			proc, err := os.FindProcess(s.ChromePID)
			if err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}
	} else if s.ChromePID > 0 {
		// Only close (and kill) the browser if we launched it
		browser.MustClose()
	}
	// If ChromePID==0 we connected to an external browser; just clear state without closing it
	// Also kill the proxy helper if running
	if s.ProxyPID > 0 {
		if proc, err := os.FindProcess(s.ProxyPID); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
	}
	// Kill the logger subprocess if running
	if s.LoggerPID > 0 {
		if proc, err := os.FindProcess(s.LoggerPID); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
	}
	removeState()
	fmt.Println("Chrome stopped")
}

func cmdStatus(args []string) {
	s, err := loadState()
	if err != nil {
		fmt.Println("No active browser session")
		return
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fmt.Printf("Browser not responding (PID %d, state may be stale)\n", s.ChromePID)
		return
	}
	pages, _ := browser.Pages()
	fmt.Printf("Browser running (PID %d)\n", s.ChromePID)
	fmt.Printf("Debug URL: %s\n", s.DebugURL)
	fmt.Printf("Pages: %d\n", len(pages))
	fmt.Printf("Active page: %d\n", s.ActivePage)
	if page, err := getActivePage(browser, s); err == nil {
		info, _ := page.Info()
		if info != nil {
			fmt.Printf("Current: %s - %s\n", info.Title, info.URL)
		}
	}
}

func cmdOpen(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney open <url>")
	}
	url := args[0]
	// Add scheme if missing
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	// If no pages exist, create one
	pages, _ := browser.Pages()
	var page *rod.Page
	if len(pages) == 0 {
		if s.Logs {
			// Create a blank page first so _logger receives TargetTargetCreated and
			// calls RuntimeEnable before any scripts execute. RuntimeEnable persists
			// across same-target navigations, so inline scripts on the real URL are
			// captured. Poll for the log file: trackPage creates it only after
			// RuntimeEnable returns, so its existence is an exact ready signal.
			page = browser.MustPage("")
			waitForLogger(page)
			if err := page.Navigate(url); err != nil {
				fatal("navigation failed: %v", err)
			}
		} else {
			page = browser.MustPage(url)
		}
		s.ActivePage = 0
		saveState(s)
	} else {
		page, err = getActivePage(browser, s)
		if err != nil {
			fatal("%v", err)
		}
		if err := page.Navigate(url); err != nil {
			fatal("navigation failed: %v", err)
		}
	}
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.Title)
	}
}

func cmdBack(args []string) {
	_, _, page := withPage()
	page.MustNavigateBack()
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdForward(args []string) {
	_, _, page := withPage()
	page.MustNavigateForward()
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdReload(args []string) {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hard := fs.Bool("hard", false, "")
	fs.Parse(args)
	_, _, page := withPage()
	if *hard {
		// CDP Page.reload with ignoreCache (equivalent to Shift+Refresh)
		err := (proto.PageReload{IgnoreCache: true}).Call(page)
		if err != nil {
			fatal("reload failed: %v", err)
		}
	} else {
		page.MustReload()
	}
	page.MustWaitLoad()
	fmt.Println("Reloaded")
}

func cmdClearCache(args []string) {
	_, _, page := withPage()
	err := (proto.NetworkClearBrowserCache{}).Call(page)
	if err != nil {
		fatal("clear cache failed: %v", err)
	}
	fmt.Println("Browser cache cleared")
}

func cmdURL(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.URL)
}

func cmdTitle(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.Title)
}

func cmdHTML(args []string) {
	_, _, page := withPage()
	if len(args) > 0 {
		el, err := page.Element(args[0])
		if err != nil {
			hint("try 'rodney discover --interactive' to see available elements")
			fatal("element not found: %v", err)
		}
		html, err := el.HTML()
		if err != nil {
			fatal("failed to get HTML: %v", err)
		}
		fmt.Println(html)
	} else {
		html := page.MustEval(`() => document.documentElement.outerHTML`).Str()
		fmt.Println(html)
	}
}

func cmdText(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney text <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		hint("try 'rodney discover --interactive' to see available elements")
		fatal("element not found: %v", err)
	}
	text, err := el.Text()
	if err != nil {
		fatal("failed to get text: %v", err)
	}
	fmt.Println(text)
}

func cmdAttr(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney attr <selector> <attribute>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		hint("try 'rodney discover --interactive' to see available elements")
		fatal("element not found: %v", err)
	}
	val := el.MustAttribute(args[1])
	if val == nil {
		fatal("attribute %q not found", args[1])
	}
	fmt.Println(*val)
}

func cmdPDF(args []string) {
	file := "page.pdf"
	if len(args) > 0 {
		file = args[0]
	}
	_, _, page := withPage()
	req := proto.PagePrintToPDF{}
	r, err := page.PDF(&req)
	if err != nil {
		fatal("failed to generate PDF: %v", err)
	}
	buf := make([]byte, 0)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	if err := os.WriteFile(file, buf, 0644); err != nil {
		fatal("failed to write PDF: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(buf))
}

func cmdJS(args []string) {
	var expr string
	if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
		if len(args) == 0 {
			// Only read from stdin automatically if it's piped (not a terminal)
			if stat, err := os.Stdin.Stat(); err != nil || (stat.Mode()&os.ModeCharDevice) != 0 {
				fatal("usage: rodney js <expression>")
			}
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal("failed to read stdin: %v", err)
		}
		expr = strings.TrimSpace(string(data))
		if expr == "" {
			fatal("empty expression from stdin")
		}
	} else {
		expr = strings.Join(args, " ")
	}
	_, _, page := withPage()

	// Wrap bare expressions in a function
	js := fmt.Sprintf(`() => { return (%s); }`, expr)
	result, err := page.Eval(js)
	if err != nil {
		fatal("JS error: %v", err)
	}
	// Print the value based on its JSON type
	v := result.Value
	raw := v.JSON("", "")
	// For simple types, print cleanly; for objects/arrays, pretty-print
	switch {
	case raw == "null" || raw == "undefined":
		fmt.Println(raw)
	case raw == "true" || raw == "false":
		fmt.Println(raw)
	case len(raw) > 0 && raw[0] == '"':
		// String value - print unquoted
		fmt.Println(v.Str())
	case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
		// Object or array - pretty print
		fmt.Println(v.JSON("", "  "))
	default:
		// Numbers and other primitives
		fmt.Println(raw)
	}
}

// parseAXFlags scans args for --role and --name flags, removes them, and returns
// the remaining args along with the extracted role and name values.
func parseAXFlags(args []string) (role, name string, remaining []string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--role":
			i++
			if i < len(args) {
				role = args[i]
			}
		case "--name":
			i++
			if i < len(args) {
				name = args[i]
			}
		default:
			remaining = append(remaining, args[i])
		}
	}
	return
}

// resolveElement parses args for either a positional CSS selector OR --role/--name
// flags, finds the element on the page, and returns: the element, a human-readable
// selector description (for error messages), and the remaining (non-selector) args.
//
// When --role/--name flags are present, the first remaining arg is NOT consumed as
// a CSS selector; all remaining args are returned as-is for the caller to use.
// When no --role/--name flags are present, the first remaining arg is consumed as
// the CSS selector, and subsequent remaining args are returned.
//
// It calls fatal() if both a CSS selector and --role/--name are provided, or if no
// element is found.
func resolveElement(page *rod.Page, args []string) (*rod.Element, string, []string) {
	role, name, remaining := parseAXFlags(args)
	hasAX := role != "" || name != ""

	if !hasAX {
		// CSS selector mode: first remaining arg is the selector
		if len(remaining) == 0 {
			fatal("must provide either a CSS selector or --role/--name flags")
		}
		selector := remaining[0]
		el, err := page.Element(selector)
		if err != nil {
			inspectFailure(page, selector)
			hint("try 'rodney discover --interactive' to see available elements")
			fatal("element not found: %v", err)
		}
		return el, selector, remaining[1:]
	}

	// Accessibility selector path — remaining args are passed through to the caller
	desc := ""
	if role != "" && name != "" {
		desc = fmt.Sprintf("--role %s --name %q", role, name)
	} else if role != "" {
		desc = fmt.Sprintf("--role %s", role)
	} else {
		desc = fmt.Sprintf("--name %q", name)
	}

	nodes, err := queryAXNodes(page, name, role)
	if err != nil {
		fatal("accessibility query failed: %v", err)
	}
	if len(nodes) == 0 {
		inspectFailure(page, desc)
		hint("try 'rodney ax-tree' to see all available accessibility nodes")
		fatal("no element found matching %s", desc)
	}

	node := nodes[0]
	if node.BackendDOMNodeID == 0 {
		fatal("matched accessibility node has no DOM backing for %s", desc)
	}

	result, err := proto.DOMResolveNode{BackendNodeID: node.BackendDOMNodeID}.Call(page)
	if err != nil {
		fatal("failed to resolve DOM node for %s: %v", desc, err)
	}
	el, err := page.ElementFromObject(result.Object)
	if err != nil {
		fatal("failed to create element from object for %s: %v", desc, err)
	}
	return el, desc, remaining
}

func cmdClick(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney click <selector> | --role <role> [--name <name>]")
	}
	selector := args[0]
	_, _, page := withPage()
	el, _, remaining := resolveElement(page, args)
	if len(remaining) > 0 {
		fatal("unexpected arguments after selector: %v", remaining)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		reportStuck(checkStuck("click", selector))
		recordCall("click", selector, false, fmt.Sprintf("%v", err))
		hint("element may not be interactive — try 'rodney js \"document.querySelector(\\\"%s\\\").click()\"'", selector)
		fatal("click failed: %v", err)
	}
	recordCall("click", selector, true, "")
	// Brief pause for click handlers to execute
	time.Sleep(100 * time.Millisecond)
	fmt.Println("Clicked")
}

func cmdInput(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney input <selector> <text> | --role <role> [--name <name>] <text>")
	}
	_, _, page := withPage()
	el, _, remaining := resolveElement(page, args)
	if len(remaining) == 0 {
		fatal("usage: rodney input <selector> <text> | --role <role> [--name <name>] <text>")
	}
	text := strings.Join(remaining, " ")
	el.MustSelectAllText().MustInput(text)
	recordCall("input", args[0], true, "")
	fmt.Printf("Typed: %s\n", text)
}

func cmdClear(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney clear <selector> | --role <role> [--name <name>]")
	}
	_, _, page := withPage()
	el, _, remaining := resolveElement(page, args)
	if len(remaining) > 0 {
		fatal("unexpected arguments after selector: %v", remaining)
	}
	el.MustSelectAllText().MustInput("")
	recordCall("clear", args[0], true, "")
	fmt.Println("Cleared")
}

func cmdFile(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney file <selector> <path|->")
	}
	selector := args[0]
	filePath := args[1]

	_, _, page := withPage()
	el, err := page.Element(selector)
	if err != nil {
		hint("try 'rodney discover --interactive' to see available elements")
		fatal("element not found: %v", err)
	}

	if filePath == "-" {
		// Read from stdin to a temp file
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal("failed to read stdin: %v", err)
		}
		tmp, err := os.CreateTemp("", "rodney-upload-*")
		if err != nil {
			fatal("failed to create temp file: %v", err)
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			fatal("failed to write temp file: %v", err)
		}
		tmp.Close()
		filePath = tmp.Name()
	} else {
		if _, err := os.Stat(filePath); err != nil {
			fatal("file not found: %v", err)
		}
	}

	if err := el.SetFiles([]string{filePath}); err != nil {
		fatal("failed to set file: %v", err)
	}
	fmt.Printf("Set file: %s\n", args[1])
}

func cmdDownload(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney download <selector> [file|-]")
	}
	selector := args[0]
	outFile := ""
	if len(args) > 1 {
		outFile = args[1]
	}

	_, _, page := withPage()
	el, err := page.Element(selector)
	if err != nil {
		hint("try 'rodney discover --interactive' to see available elements")
		fatal("element not found: %v", err)
	}

	// Get the URL from the element's href or src attribute
	urlStr := ""
	if v := el.MustAttribute("href"); v != nil {
		urlStr = *v
	} else if v := el.MustAttribute("src"); v != nil {
		urlStr = *v
	} else {
		fatal("element has no href or src attribute")
	}

	var data []byte

	if strings.HasPrefix(urlStr, "data:") {
		data, err = decodeDataURL(urlStr)
		if err != nil {
			fatal("failed to decode data URL: %v", err)
		}
	} else {
		// Use fetch() in the page context so it has cookies/session
		// Also resolves relative URLs automatically
		js := fmt.Sprintf(`async () => {
			const resp = await fetch(%q);
			if (!resp.ok) throw new Error('HTTP ' + resp.status);
			const buf = await resp.arrayBuffer();
			const bytes = new Uint8Array(buf);
			let binary = '';
			for (let i = 0; i < bytes.length; i++) {
				binary += String.fromCharCode(bytes[i]);
			}
			return btoa(binary);
		}`, urlStr)
		result, err := page.Eval(js)
		if err != nil {
			fatal("download failed: %v", err)
		}
		data, err = base64.StdEncoding.DecodeString(result.Value.Str())
		if err != nil {
			fatal("failed to decode response: %v", err)
		}
	}

	if outFile == "-" {
		os.Stdout.Write(data)
		return
	}

	if outFile == "" {
		outFile = inferDownloadFilename(urlStr)
	}

	if err := os.WriteFile(outFile, data, 0644); err != nil {
		fatal("failed to write file: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", outFile, len(data))
}

// decodeDataURL decodes a data:[<mediatype>][;base64],<data> URL.
func decodeDataURL(dataURL string) ([]byte, error) {
	// Find the comma separating metadata from data
	commaIdx := strings.Index(dataURL, ",")
	if commaIdx < 0 {
		return nil, fmt.Errorf("invalid data URL: no comma found")
	}
	meta := dataURL[5:commaIdx] // skip "data:"
	encoded := dataURL[commaIdx+1:]

	if strings.HasSuffix(meta, ";base64") {
		return base64.StdEncoding.DecodeString(encoded)
	}
	// URL-encoded text
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return nil, err
	}
	return []byte(decoded), nil
}

// inferDownloadFilename tries to extract a reasonable filename from a URL.
func inferDownloadFilename(urlStr string) string {
	if strings.HasPrefix(urlStr, "data:") {
		// Extract MIME type for extension
		commaIdx := strings.Index(urlStr, ",")
		if commaIdx > 0 {
			meta := urlStr[5:commaIdx]
			meta = strings.TrimSuffix(meta, ";base64")
			ext := mimeToExt(meta)
			return nextAvailableFile("download", ext)
		}
		return nextAvailableFile("download", "")
	}

	parsed, err := url.Parse(urlStr)
	if err == nil && parsed.Path != "" && parsed.Path != "/" {
		base := filepath.Base(parsed.Path)
		if base != "." && base != "/" {
			return nextAvailableFile(
				strings.TrimSuffix(base, filepath.Ext(base)),
				filepath.Ext(base),
			)
		}
	}
	return nextAvailableFile("download", "")
}

// mimeToExt returns a file extension for common MIME types.
func mimeToExt(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/css":
		return ".css"
	case "application/json":
		return ".json"
	case "application/javascript":
		return ".js"
	case "application/octet-stream":
		return ".bin"
	default:
		return ""
	}
}

func cmdSelect(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney select <selector> <value> | --role <role> [--name <name>] <value>")
	}
	_, _, page := withPage()
	el, _, remaining := resolveElement(page, args)
	if len(remaining) == 0 {
		fatal("usage: rodney select <selector> <value> | --role <role> [--name <name>] <value>")
	}
	value := remaining[0]
	// Use JavaScript on the resolved element to set value and dispatch change event
	result, err := el.Eval(`(val) => { this.value = val; this.dispatchEvent(new Event('change', {bubbles: true})); return this.value; }`, value)
	if err != nil {
		inspectFailure(page, args[0])
		hint("try 'rodney discover --interactive' to see available elements")

		fatal("select failed: %v", err)
	}
	fmt.Printf("Selected: %s\n", result.Value.Str())
}

func cmdSubmit(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney submit <selector> | --role <role> [--name <name>]")
	}
	_, _, page := withPage()
	el, _, remaining := resolveElement(page, args)
	if len(remaining) > 0 {
		fatal("unexpected arguments after selector: %v", remaining)
	}
	_, err := el.Eval(`() => { this.submit(); }`)
	if err != nil {
		fatal("submit failed: %v", err)
	}
	fmt.Println("Submitted")
}

func cmdHover(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney hover <selector> | --role <role> [--name <name>]")
	}
	_, _, page := withPage()
	el, _, remaining := resolveElement(page, args)
	if len(remaining) > 0 {
		fatal("unexpected arguments after selector: %v", remaining)
	}
	el.MustHover()
	recordCall("hover", args[0], true, "")
	fmt.Println("Hovered")
}

func cmdFocus(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney focus <selector> | --role <role> [--name <name>]")
	}
	_, _, page := withPage()
	el, _, remaining := resolveElement(page, args)
	if len(remaining) > 0 {
		fatal("unexpected arguments after selector: %v", remaining)
	}
	el.MustFocus()
	recordCall("focus", args[0], true, "")
	fmt.Println("Focused")
}

// parseWaitArgs extracts --text <value> and --gone flags from args,
// returning the selector and option values.
func parseWaitArgs(args []string) (selector string, textMatch string, gone bool) {
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--text":
			if i+1 >= len(args) {
				fatal("--text requires a value")
			}
			i++
			textMatch = args[i]
		case "--gone":
			gone = true
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) != 1 {
		fatal("usage: rodney wait <selector> [--text <value>] [--gone]")
	}
	selector = positional[0]
	return
}

func cmdWait(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney wait <selector> [--text <value>] [--gone] | --role <role> [--name <name>]")
	}

	selector, textMatch, gone := parseWaitArgs(args)

	if textMatch != "" && gone {
		fatal("--text and --gone are mutually exclusive")
	}

	_, _, page := withPage()

	if gone {
		// Wait for element to disappear from DOM or become hidden
		deadline := time.Now().Add(defaultTimeout)
		for time.Now().Before(deadline) {
			els, err := page.Elements(selector)
			if err != nil || len(els) == 0 {
				fmt.Println("Element gone")
				return
			}
			allHidden := true
			for _, el := range els {
				visible, err := el.Visible()
				if err == nil && visible {
					allHidden = false
					break
				}
			}
			if allHidden {
				fmt.Println("Element gone")
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		fatal("timeout waiting for element to disappear: %s", selector)
		return
	}

	if textMatch != "" {
		// Wait for element to exist and contain the specified text
		deadline := time.Now().Add(defaultTimeout)
		for time.Now().Before(deadline) {
			el, err := page.Element(selector)
			if err == nil {
				text, err := el.Text()
				if err == nil && strings.Contains(text, textMatch) {
					fmt.Println("Found")
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		hint("page may still be loading — try 'rodney waitstable' before this command")
		fatal("timeout waiting for text %q in %s", textMatch, selector)
		return
	}

	// Default: wait for element to exist and be visible (supports --role/--name)
	el, _, _ := resolveElement(page, args)
	el.MustWaitVisible()
	fmt.Println("Found")
}

func cmdWaitLoad(args []string) {
	_, _, page := withPage()
	page.MustWaitLoad()
	fmt.Println("Page loaded")
}

func cmdWaitStable(args []string) {
	_, _, page := withPage()
	page.MustWaitStable()
	fmt.Println("DOM stable")
}

func cmdWaitIdle(args []string) {
	_, _, page := withPage()
	page.MustWaitIdle()
	fmt.Println("Network idle")
}

func cmdSleep(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney sleep <seconds>")
	}
	secs, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		fatal("invalid seconds: %v", err)
	}
	time.Sleep(time.Duration(secs * float64(time.Second)))
}

// nextAvailableFile returns "base+ext" if it doesn't exist,
// otherwise "base-2+ext", "base-3+ext", etc.
func nextAvailableFile(base, ext string) string {
	name := base + ext
	if _, err := os.Stat(name); os.IsNotExist(err) {
		return name
	}
	for i := 2; ; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name
		}
	}
}

func cmdViewport(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney viewport <width> <height> [--scale N] [--mobile]\n       rodney viewport --reset")
	}

	// Handle --reset: clear viewport override and restore browser defaults
	if args[0] == "--reset" {
		s, _, page := withPage()

		if err := (proto.EmulationClearDeviceMetricsOverride{}.Call(page)); err != nil {
			fatal("failed to clear viewport override: %v", err)
		}

		s.ViewportWidth = 0
		s.ViewportHeight = 0
		s.ViewportScale = 0
		s.ViewportMobile = false
		if err := saveState(s); err != nil {
			fatal("failed to save state: %v", err)
		}

		fmt.Println("Viewport reset to browser default")
		return
	}

	if len(args) < 2 {
		fatal("usage: rodney viewport <width> <height> [--scale N] [--mobile]\n       rodney viewport --reset")
	}

	w, err := strconv.Atoi(args[0])
	if err != nil {
		fatal("invalid width: %v", err)
	}
	h, err := strconv.Atoi(args[1])
	if err != nil {
		fatal("invalid height: %v", err)
	}

	scale := 1.0
	mobile := false

	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--scale":
			i++
			if i >= len(args) {
				fatal("missing value for --scale")
			}
			v, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				fatal("invalid scale: %v", err)
			}
			scale = v
		case "--mobile":
			mobile = true
		default:
			fatal("unknown flag: %s", args[i])
		}
	}

	s, _, page := withPage()

	err = proto.EmulationSetDeviceMetricsOverride{
		Width:             w,
		Height:            h,
		DeviceScaleFactor: scale,
		Mobile:            mobile,
	}.Call(page)
	if err != nil {
		fatal("failed to set viewport: %v", err)
	}

	// Persist viewport settings so they are re-applied on each subsequent command
	s.ViewportWidth = w
	s.ViewportHeight = h
	s.ViewportScale = scale
	s.ViewportMobile = mobile
	if err := saveState(s); err != nil {
		fatal("failed to save state: %v", err)
	}

	fmt.Println(formatViewportDesc("Viewport set to", w, h, mobile, scale))
}

func cmdScreenshot(args []string) {
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	width := fs.Int("width", 0, "")
	fs.IntVar(width, "w", 0, "")
	height := fs.Int("height", 0, "")
	fs.IntVar(height, "h", 0, "")

	if err := fs.Parse(args); err != nil {
		fatal("%v", err)
	}

	fullPage := true
	sizeExplicit := false
	fs.Visit(func(f *flag.Flag) {
		sizeExplicit = true
		if f.Name == "height" || f.Name == "h" {
			fullPage = false
		}
	})

	var file string
	if fs.NArg() > 0 {
		file = fs.Arg(0)
	} else {
		file = nextAvailableFile("screenshot", ".png")
	}

	s, _, page := withPage()

	// Only override viewport if -w/-h were explicitly passed, or if no
	// viewport has been set via "rodney viewport"
	if sizeExplicit || s.ViewportWidth == 0 {
		w := *width
		if w == 0 {
			w = 1280
		}
		viewportHeight := *height
		if viewportHeight == 0 {
			viewportHeight = 720
		}
		err := proto.EmulationSetDeviceMetricsOverride{
			Width:             w,
			Height:            viewportHeight,
			DeviceScaleFactor: 1,
		}.Call(page)
		if err != nil {
			fatal("failed to set viewport: %v", err)
		}
	}

	data, err := page.Screenshot(fullPage, nil)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Println(file)
}

func cmdScreenshotEl(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney screenshot-el <selector> [file]")
	}
	file := "element.png"
	if len(args) > 1 {
		file = args[1]
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		hint("try 'rodney discover --interactive' to see available elements")
		fatal("element not found: %v", err)
	}
	data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(data))
}

func cmdPages(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	for i, p := range pages {
		marker := " "
		if i == s.ActivePage {
			marker = "*"
		}
		info, _ := p.Info()
		if info != nil {
			fmt.Printf("%s [%d] %s - %s\n", marker, i, info.Title, info.URL)
		} else {
			fmt.Printf("%s [%d] (unknown)\n", marker, i)
		}
	}
}

func cmdPage(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney page <index>")
	}
	idx, err := strconv.Atoi(args[0])
	if err != nil {
		fatal("invalid index: %v", err)
	}
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range (0-%d)", idx, len(pages)-1)
	}
	s.ActivePage = idx
	if err := saveState(s); err != nil {
		fatal("failed to save state: %v", err)
	}
	info, _ := pages[idx].Info()
	if info != nil {
		fmt.Printf("Switched to [%d] %s - %s\n", idx, info.Title, info.URL)
	}
}

func cmdNewPage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	url := ""
	if len(args) > 0 {
		url = args[0]
		if !strings.Contains(url, "://") {
			url = "http://" + url
		}
	}

	var page *rod.Page
	if url != "" {
		if s.Logs {
			// Same blank-page-first strategy as cmdOpen.
			page = browser.MustPage("")
			waitForLogger(page)
			if err := page.Navigate(url); err != nil {
				fatal("navigation failed: %v", err)
			}
		} else {
			page = browser.MustPage(url)
		}
		page.MustWaitLoad()
	} else {
		page = browser.MustPage("")
	}

	// Switch active to the new page
	pages, _ := browser.Pages()
	for i, p := range pages {
		if p.TargetID == page.TargetID {
			s.ActivePage = i
			break
		}
	}
	saveState(s)

	info, _ := page.Info()
	if info != nil {
		fmt.Printf("Opened [%d] %s\n", s.ActivePage, info.URL)
	}
}

func cmdClosePage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	if len(pages) <= 1 {
		fatal("cannot close the last page")
	}

	idx := s.ActivePage
	if len(args) > 0 {
		idx, err = strconv.Atoi(args[0])
		if err != nil {
			fatal("invalid index: %v", err)
		}
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range", idx)
	}

	pages[idx].MustClose()

	// Adjust active page
	if s.ActivePage >= len(pages)-1 {
		s.ActivePage = len(pages) - 2
	}
	if s.ActivePage < 0 {
		s.ActivePage = 0
	}
	saveState(s)
	fmt.Printf("Closed page %d\n", idx)
}

func cmdExists(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney exists <selector> | --role <role> [--name <name>]")
	}
	_, _, page := withPage()

	role, name, remaining := parseAXFlags(args)
	hasAX := role != "" || name != ""
	hasCSS := len(remaining) > 0

	if hasAX && hasCSS {
		fatal("cannot use both a CSS selector and --role/--name flags")
	}

	if hasAX {
		nodes, err := queryAXNodes(page, name, role)
		if err != nil {
			fatal("query failed: %v", err)
		}
		if len(nodes) > 0 {
			fmt.Println("true")
			os.Exit(0)
		} else {
			fmt.Println("false")
			os.Exit(1)
		}
	} else {
		has, _, err := page.Has(remaining[0])
		if err != nil {
			fatal("query failed: %v", err)
		}
		if has {
			fmt.Println("true")
			os.Exit(0)
		} else {
			fmt.Println("false")
			os.Exit(1)
		}
	}
}

func cmdCount(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney count <selector>")
	}
	_, _, page := withPage()
	els, err := page.Elements(args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}
	fmt.Println(len(els))
}

func cmdVisible(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney visible <selector> | --role <role> [--name <name>]")
	}
	_, _, page := withPage()

	role, name, remaining := parseAXFlags(args)
	hasAX := role != "" || name != ""
	hasCSS := len(remaining) > 0

	if hasAX && hasCSS {
		fatal("cannot use both a CSS selector and --role/--name flags")
	}

	var el *rod.Element
	if hasAX {
		nodes, err := queryAXNodes(page, name, role)
		if err != nil || len(nodes) == 0 {
			fmt.Println("false")
			os.Exit(1)
		}
		node := nodes[0]
		if node.BackendDOMNodeID == 0 {
			fmt.Println("false")
			os.Exit(1)
		}
		result, err := proto.DOMResolveNode{BackendNodeID: node.BackendDOMNodeID}.Call(page)
		if err != nil {
			fmt.Println("false")
			os.Exit(1)
		}
		el, err = page.ElementFromObject(result.Object)
		if err != nil {
			fmt.Println("false")
			os.Exit(1)
		}
	} else {
		var err error
		el, err = page.Element(remaining[0])
		if err != nil {
			fmt.Println("false")
			os.Exit(1)
		}
	}

	visible, err := el.Visible()
	if err != nil {
		fmt.Println("false")
		os.Exit(1)
	}
	if visible {
		fmt.Println("true")
		os.Exit(0)
	} else {
		fmt.Println("false")
		os.Exit(1)
	}
}

// parseAssertArgs separates flags (--message/-m) from positional args.
// Returns (expression, expected, message). expected is nil for truthy mode.
func parseAssertArgs(args []string) (expr string, expected *string, message string) {
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--message", "-m":
			i++
			if i < len(args) {
				message = args[i]
			}
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) >= 1 {
		expr = positional[0]
	}
	if len(positional) >= 2 {
		expected = &positional[1]
	}
	return
}

// formatAssertFail builds the failure output line.
// For truthy failures expected is nil; for equality failures it points to the expected string.
func formatAssertFail(actual string, expected *string, message string) string {
	if expected != nil {
		// Equality mode
		detail := fmt.Sprintf("got %q, expected %q", actual, *expected)
		if message != "" {
			return fmt.Sprintf("fail: %s (%s)", message, detail)
		}
		return fmt.Sprintf("fail: %s", detail)
	}
	// Truthy mode
	if message != "" {
		return fmt.Sprintf("fail: %s (got %s)", message, actual)
	}
	return fmt.Sprintf("fail: got %s", actual)
}

// resolveAssertArgs resolves stdin for `rodney assert`, returning a normalized args
// slice where the first positional is always the JS expression string (never "-").
// It mirrors the stdin-detection logic from cmdJS: "-" reads stdin explicitly;
// no positional args auto-reads stdin when piped; otherwise falls through unchanged.
func resolveAssertArgs(args []string) []string {
	// Find index of first positional arg (skipping -m/--message pairs).
	firstPosIdx := -1
	for i := 0; i < len(args); i++ {
		if args[i] == "--message" || args[i] == "-m" {
			i++ // skip flag value
			continue
		}
		firstPosIdx = i
		break
	}

	if firstPosIdx >= 0 && args[firstPosIdx] == "-" {
		// Explicit stdin: replace "-" with expression read from stdin.
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal("failed to read stdin: %v", err)
		}
		expr := strings.TrimSpace(string(data))
		if expr == "" {
			fatal("empty expression from stdin")
		}
		newArgs := make([]string, len(args))
		copy(newArgs, args)
		newArgs[firstPosIdx] = expr
		return newArgs
	} else if firstPosIdx == -1 {
		// No positional args — auto-read from stdin only if it's piped.
		if stat, err := os.Stdin.Stat(); err != nil || (stat.Mode()&os.ModeCharDevice) != 0 {
			fatal("usage: rodney assert <js-expression> [expected] [--message msg]")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal("failed to read stdin: %v", err)
		}
		expr := strings.TrimSpace(string(data))
		if expr == "" {
			fatal("empty expression from stdin")
		}
		return append([]string{expr}, args...)
	}
	return args
}

func cmdAssert(args []string) {
	args = resolveAssertArgs(args)

	expr, expected, message := parseAssertArgs(args)
	if expr == "" {
		fatal("usage: rodney assert <js-expression> [expected] [--message msg]")
	}

	_, _, page := withPage()

	js := fmt.Sprintf(`() => { return (%s); }`, expr)
	result, err := page.Eval(js)
	if err != nil {
		fatal("JS error: %v", err)
	}

	// Format the result value as a string, matching the js command's output
	v := result.Value
	raw := v.JSON("", "")
	var actual string
	switch {
	case raw == "null" || raw == "undefined":
		actual = raw
	case raw == "true" || raw == "false":
		actual = raw
	case len(raw) > 0 && raw[0] == '"':
		actual = v.Str()
	case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
		actual = v.JSON("", "  ")
	default:
		actual = raw
	}

	if expected != nil {
		// Equality mode: compare string representation to expected
		if actual == *expected {
			fmt.Println("pass")
			os.Exit(0)
		} else {
			fmt.Println(formatAssertFail(actual, expected, message))
			os.Exit(1)
		}
	} else {
		// Truthy mode: check if the JS value is truthy
		switch raw {
		case "false", "0", "null", "undefined", `""`:
			fmt.Println(formatAssertFail(actual, nil, message))
			os.Exit(1)
		default:
			fmt.Println("pass")
			os.Exit(0)
		}
	}
}

// --- Composable check command ---

type checkItem struct {
	kind string // "exists", "visible", "text", "count", "assert"
	arg1 string // selector or JS expression
	arg2 string // expected value (for text, count, assert equality)
}

type checkResult struct {
	Check    string `json:"check"`
	Selector string `json:"selector,omitempty"`
	Expr     string `json:"expr,omitempty"`
	Pass     bool   `json:"pass"`
	Got      string `json:"got,omitempty"`
	Expected string `json:"expected,omitempty"`
}

// parseCheckArgs walks the argument list and builds a slice of checkItems.
// Returns the checks, whether --json was specified, and any parse error.
func parseCheckArgs(args []string) ([]checkItem, bool, error) {
	var checks []checkItem
	jsonOutput := false
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--json":
			jsonOutput = true
			i++
		case "--exists":
			i++
			if i >= len(args) {
				return nil, false, fmt.Errorf("--exists requires a selector argument")
			}
			checks = append(checks, checkItem{kind: "exists", arg1: args[i]})
			i++
		case "--visible":
			i++
			if i >= len(args) {
				return nil, false, fmt.Errorf("--visible requires a selector argument")
			}
			checks = append(checks, checkItem{kind: "visible", arg1: args[i]})
			i++
		case "--text":
			i++
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("--text requires <selector> <expected> arguments")
			}
			checks = append(checks, checkItem{kind: "text", arg1: args[i], arg2: args[i+1]})
			i += 2
		case "--count":
			i++
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("--count requires <selector> <expected> arguments")
			}
			checks = append(checks, checkItem{kind: "count", arg1: args[i], arg2: args[i+1]})
			i += 2
		case "--assert":
			i++
			if i >= len(args) {
				return nil, false, fmt.Errorf("--assert requires an expression argument")
			}
			expr := args[i]
			i++
			// Optionally consume next arg as expected value if it doesn't start with "--"
			expected := ""
			if i < len(args) && !strings.HasPrefix(args[i], "--") {
				expected = args[i]
				i++
			}
			checks = append(checks, checkItem{kind: "assert", arg1: expr, arg2: expected})
		default:
			return nil, false, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	return checks, jsonOutput, nil
}

// runCheck executes a single check against the given page and returns the result.
func runCheck(page *rod.Page, c checkItem) checkResult {
	switch c.kind {
	case "exists":
		has, _, err := page.Has(c.arg1)
		if err != nil {
			return checkResult{Check: "exists", Selector: c.arg1, Pass: false, Got: fmt.Sprintf("error: %v", err)}
		}
		return checkResult{Check: "exists", Selector: c.arg1, Pass: has, Got: fmt.Sprintf("%v", has)}

	case "visible":
		el, err := page.Element(c.arg1)
		if err != nil {
			return checkResult{Check: "visible", Selector: c.arg1, Pass: false, Got: "not found"}
		}
		vis, err := el.Visible()
		if err != nil {
			return checkResult{Check: "visible", Selector: c.arg1, Pass: false, Got: fmt.Sprintf("error: %v", err)}
		}
		return checkResult{Check: "visible", Selector: c.arg1, Pass: vis, Got: fmt.Sprintf("%v", vis)}

	case "text":
		el, err := page.Element(c.arg1)
		if err != nil {
			return checkResult{Check: "text", Selector: c.arg1, Pass: false, Expected: c.arg2, Got: "element not found"}
		}
		text, err := el.Text()
		if err != nil {
			return checkResult{Check: "text", Selector: c.arg1, Pass: false, Expected: c.arg2, Got: fmt.Sprintf("error: %v", err)}
		}
		pass := text == c.arg2
		return checkResult{Check: "text", Selector: c.arg1, Pass: pass, Got: text, Expected: c.arg2}

	case "count":
		els, err := page.Elements(c.arg1)
		if err != nil {
			return checkResult{Check: "count", Selector: c.arg1, Pass: false, Expected: c.arg2, Got: fmt.Sprintf("error: %v", err)}
		}
		got := strconv.Itoa(len(els))
		pass := got == c.arg2
		return checkResult{Check: "count", Selector: c.arg1, Pass: pass, Got: got, Expected: c.arg2}

	case "assert":
		js := fmt.Sprintf(`() => { return (%s); }`, c.arg1)
		result, err := page.Eval(js)
		if err != nil {
			return checkResult{Check: "assert", Expr: c.arg1, Pass: false, Got: fmt.Sprintf("error: %v", err), Expected: c.arg2}
		}
		v := result.Value
		raw := v.JSON("", "")
		var actual string
		switch {
		case raw == "null" || raw == "undefined":
			actual = raw
		case raw == "true" || raw == "false":
			actual = raw
		case len(raw) > 0 && raw[0] == '"':
			actual = v.Str()
		case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
			actual = v.JSON("", "  ")
		default:
			actual = raw
		}

		if c.arg2 != "" {
			// Equality mode
			pass := actual == c.arg2
			return checkResult{Check: "assert", Expr: c.arg1, Pass: pass, Got: actual, Expected: c.arg2}
		}
		// Truthy mode
		truthy := true
		switch raw {
		case "false", "0", "null", "undefined", `""`:
			truthy = false
		}
		return checkResult{Check: "assert", Expr: c.arg1, Pass: truthy, Got: actual}

	default:
		return checkResult{Check: c.kind, Pass: false, Got: "unknown check type"}
	}
}

// formatCheckLine formats a single check result as a human-readable line.
func formatCheckLine(r checkResult) string {
	status := "PASS"
	if !r.Pass {
		status = "FAIL"
	}

	label := r.Check
	target := r.Selector
	if target == "" {
		target = r.Expr
	}

	line := fmt.Sprintf("%s  %s %s", status, label, target)

	if !r.Pass {
		if r.Expected != "" {
			line += fmt.Sprintf(" -- got %q, expected %q", r.Got, r.Expected)
		} else if r.Check != "exists" && r.Check != "visible" {
			line += fmt.Sprintf(" -- got %q", r.Got)
		}
	} else if r.Expected != "" {
		line += fmt.Sprintf(" = %s", r.Expected)
	}

	return line
}

func cmdCheck(args []string) {
	checks, jsonOutput, err := parseCheckArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(2)
	}
	if len(checks) == 0 {
		fmt.Fprintln(os.Stderr, "error: no checks specified")
		os.Exit(2)
	}

	_, _, page := withPage()

	var results []checkResult
	for _, c := range checks {
		results = append(results, runCheck(page, c))
	}

	passed := 0
	for _, r := range results {
		if r.Pass {
			passed++
		}
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(data))
	} else {
		for _, r := range results {
			fmt.Println(formatCheckLine(r))
		}
		fmt.Println("----")
		fmt.Printf("%d/%d passed\n", passed, len(results))
	}

	if passed < len(results) {
		os.Exit(1)
	}
	os.Exit(0)
}

// Ignore SIGPIPE for piped output
func init() {
	signal.Ignore(syscall.SIGPIPE)
}

// --- Console log commands ---

type consoleEntry struct {
	level     string
	source    string
	text      string
	timestamp float64 // Unix milliseconds
	url       string
	line      *int
}

// formatLogLevel formats a proto.LogLogEntryLevel value as a string.
// Kept for compatibility and unit-testing.
func formatLogLevel(level proto.LogLogEntryLevel) string {
	switch level {
	case proto.LogLogEntryLevelVerbose:
		return "verbose"
	case proto.LogLogEntryLevelInfo:
		return "info"
	case proto.LogLogEntryLevelWarning:
		return "warning"
	case proto.LogLogEntryLevelError:
		return "error"
	default:
		return string(level)
	}
}

// consoleTypeToLevel maps a Runtime.consoleAPICalled type to a log level string.
func consoleTypeToLevel(t proto.RuntimeConsoleAPICalledType) string {
	switch t {
	case proto.RuntimeConsoleAPICalledTypeDebug:
		return "verbose"
	case proto.RuntimeConsoleAPICalledTypeLog, proto.RuntimeConsoleAPICalledTypeInfo,
		proto.RuntimeConsoleAPICalledTypeDir, proto.RuntimeConsoleAPICalledTypeDirxml,
		proto.RuntimeConsoleAPICalledTypeTable, proto.RuntimeConsoleAPICalledTypeTrace,
		proto.RuntimeConsoleAPICalledTypeStartGroup, proto.RuntimeConsoleAPICalledTypeStartGroupCollapsed,
		proto.RuntimeConsoleAPICalledTypeEndGroup, proto.RuntimeConsoleAPICalledTypeClear,
		proto.RuntimeConsoleAPICalledTypeCount, proto.RuntimeConsoleAPICalledTypeTimeEnd,
		proto.RuntimeConsoleAPICalledTypeProfile, proto.RuntimeConsoleAPICalledTypeProfileEnd:
		return "info"
	case proto.RuntimeConsoleAPICalledTypeWarning:
		return "warning"
	case proto.RuntimeConsoleAPICalledTypeError, proto.RuntimeConsoleAPICalledTypeAssert:
		return "error"
	default:
		return string(t)
	}
}

// formatConsoleArgs converts Runtime RemoteObjects to a human-readable string.
func formatConsoleArgs(args []*proto.RuntimeRemoteObject) string {
	var parts []string
	for _, arg := range args {
		switch string(arg.Type) {
		case "string":
			parts = append(parts, arg.Value.Str())
		case "number", "boolean":
			parts = append(parts, arg.Value.JSON("", ""))
		case "undefined":
			parts = append(parts, "undefined")
		case "null":
			parts = append(parts, "null")
		default:
			if arg.Description != "" {
				parts = append(parts, arg.Description)
			} else {
				parts = append(parts, arg.Value.JSON("", ""))
			}
		}
	}
	return strings.Join(parts, " ")
}

func cmdLogs(args []string) {
	followMode := false
	jsonOutput := false
	limitN := -1

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--follow":
			followMode = true
		case "--json":
			jsonOutput = true
		case "-n":
			i++
			if i >= len(args) {
				fatal("missing value for -n")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				fatal("invalid value for -n: %s", args[i])
			}
			limitN = n
		default:
			fatal("unknown flag: %s\nusage: rodney logs [-f] [-n N] [--json]", args[i])
		}
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	if !s.Logs {
		fmt.Fprintln(os.Stderr, "logs not enabled (run: rodney start --logs)")
		os.Exit(1)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	page, err := getActivePage(browser, s)
	if err != nil {
		fatal("%v", err)
	}

	logFile := filepath.Join(stateDir(), "logs", string(page.TargetID)+".ndjson")

	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "no console log recorded for this page yet")
		os.Exit(0)
	}

	if followMode {
		fmt.Fprintln(os.Stderr, "Streaming console logs (Ctrl+C to stop)...")
		tailLogFile(logFile, limitN, jsonOutput)
		return
	}

	// Snapshot mode: stream the file to avoid loading it all into memory.
	if limitN > 0 {
		// Ring buffer: O(limitN) memory regardless of file size.
		ring := make([]string, limitN)
		count := 0
		scanLogFile(logFile, func(line string) {
			ring[count%limitN] = line
			count++
		})
		start, n := 0, count
		if count > limitN {
			start = count % limitN
			n = limitN
		}
		for i := 0; i < n; i++ {
			printNDJSONLine(ring[(start+i)%limitN], jsonOutput)
		}
	} else {
		scanLogFile(logFile, func(line string) {
			printNDJSONLine(line, jsonOutput)
		})
	}
}

// scanLogFile opens logFile and calls fn for each non-empty line using a
// streaming bufio.Scanner — no whole-file read into memory.
func scanLogFile(logFile string, fn func(string)) {
	f, err := os.Open(logFile)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			fn(line)
		}
	}
}

// printNDJSONLine prints a single NDJSON log line.
// In JSON mode it prints verbatim; otherwise it formats as "[level] text".
func printNDJSONLine(line string, jsonOutput bool) {
	if jsonOutput {
		fmt.Println(line)
		return
	}
	var obj struct {
		Level string `json:"level"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal([]byte(line), &obj); err == nil {
		fmt.Printf("[%s] %s\n", obj.Level, obj.Text)
	}
}

// tailLogFile follows a log file, printing new lines as they are appended.
// If limitN > 0, prints the last N existing lines first, then follows new content.
// If limitN <= 0, seeks to the end immediately and only shows new entries.
func tailLogFile(logFile string, limitN int, jsonOutput bool) {
	f, err := os.Open(logFile)
	if err != nil {
		fatal("failed to open log file: %v", err)
	}
	defer f.Close()

	if limitN > 0 {
		// Ring buffer: stream last N lines without loading the whole file.
		ring := make([]string, limitN)
		count := 0
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if line := scanner.Text(); line != "" {
				ring[count%limitN] = line
				count++
			}
		}
		start, n := 0, count
		if count > limitN {
			start = count % limitN
			n = limitN
		}
		for i := 0; i < n; i++ {
			printNDJSONLine(ring[(start+i)%limitN], jsonOutput)
		}
	}
	// Seek to end to tail only new content (scanner may have over-read into
	// a bufio buffer, but explicit SeekEnd corrects the OS file position).
	f.Seek(0, io.SeekEnd)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	buf := make([]byte, 4096)
	var partial string
	for {
		select {
		case <-sigCh:
			return
		default:
		}
		n, _ := f.Read(buf)
		if n > 0 {
			partial += string(buf[:n])
			for {
				idx := strings.Index(partial, "\n")
				if idx < 0 {
					break
				}
				line := partial[:idx]
				partial = partial[idx+1:]
				if line != "" {
					printNDJSONLine(line, jsonOutput)
				}
			}
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func makeConsoleEntry(e *proto.RuntimeConsoleAPICalled) consoleEntry {
	entry := consoleEntry{
		level:     consoleTypeToLevel(e.Type),
		source:    "javascript",
		text:      formatConsoleArgs(e.Args),
		timestamp: float64(e.Timestamp),
	}
	if e.StackTrace != nil && len(e.StackTrace.CallFrames) > 0 {
		frame := e.StackTrace.CallFrames[0]
		entry.url = frame.URL
		line := frame.LineNumber
		entry.line = &line
	}
	return entry
}

// marshalConsoleEntry serializes a consoleEntry to a JSON line for the NDJSON log file.
func marshalConsoleEntry(entry consoleEntry) string {
	ts := time.UnixMilli(int64(entry.timestamp)).UTC()
	obj := map[string]interface{}{
		"level":     entry.level,
		"source":    entry.source,
		"text":      entry.text,
		"timestamp": ts.Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if entry.url != "" {
		obj["url"] = entry.url
	}
	if entry.line != nil {
		obj["line"] = *entry.line
	}
	data, _ := json.Marshal(obj)
	return string(data)
}

// --- Accessibility commands ---

func cmdAXTree(args []string) {
	fs := flag.NewFlagSet("ax-tree", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	depthVal := fs.Int("depth", 0, "")
	jsonOutput := fs.Bool("json", false, "")

	if err := fs.Parse(args); err != nil {
		fatal("unknown flag: %s\nusage: rodney ax-tree [--depth N] [--json]", findUnknownFlag(args, fs))
	}
	if fs.NArg() > 0 {
		fatal("unknown flag: %s\nusage: rodney ax-tree [--depth N] [--json]", fs.Arg(0))
	}

	var depth *int
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "depth" {
			depth = depthVal
		}
	})

	_, _, page := withPage()
	result, err := proto.AccessibilityGetFullAXTree{Depth: depth}.Call(page)
	if err != nil {
		fatal("failed to get accessibility tree: %v", err)
	}

	if *jsonOutput {
		fmt.Println(formatAXTreeJSON(result.Nodes))
	} else {
		fmt.Print(formatAXTree(result.Nodes))
	}
}

func cmdAXFind(args []string) {
	fs := flag.NewFlagSet("ax-find", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")
	role := fs.String("role", "", "")
	jsonOutput := fs.Bool("json", false, "")

	if err := fs.Parse(args); err != nil {
		fatal("unknown flag: %s\nusage: rodney ax-find [--name N] [--role R] [--json]", findUnknownFlag(args, fs))
	}
	if fs.NArg() > 0 {
		fatal("unknown flag: %s\nusage: rodney ax-find [--name N] [--role R] [--json]", fs.Arg(0))
	}

	_, _, page := withPage()
	nodes, err := queryAXNodes(page, *name, *role)
	if err != nil {
		fatal("query failed: %v", err)
	}

	if len(nodes) == 0 {
		hint("try broader criteria — 'rodney ax-tree' shows all available nodes")
		fmt.Fprintln(os.Stderr, "No matching nodes")
		os.Exit(1)
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(nodes, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(formatAXNodeList(nodes))
	}
}

func cmdAXNode(args []string) {
	// Pre-extract --json since it may appear after the positional selector
	jsonOutput := false
	var filtered []string
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		} else {
			filtered = append(filtered, a)
		}
	}

	fs := flag.NewFlagSet("ax-node", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Parse(filtered)

	if fs.NArg() < 1 {
		fatal("usage: rodney ax-node <selector> [--json]")
	}
	selector := fs.Arg(0)

	_, _, page := withPage()
	node, err := getAXNode(page, selector)
	if err != nil {
		fatal("%v", err)
	}

	if jsonOutput {
		fmt.Println(formatAXNodeDetailJSON(node))
	} else {
		fmt.Print(formatAXNodeDetail(node))
	}
}

type discoverEntry struct {
	ID      string `json:"id"`
	Tag     string `json:"tag"`
	Action  string `json:"action"`
	Text    string `json:"text"`
	Visible bool   `json:"visible"`
}

// queryDiscoverEntries finds all elements with the given attribute and returns structured entries.
func queryDiscoverEntries(page *rod.Page, attrName string) ([]discoverEntry, error) {
	js := fmt.Sprintf(`() => {
		var results = [];
		var els = document.querySelectorAll('[%s]');
		for (var i = 0; i < els.length; i++) {
			var el = els[i];
			var id = el.getAttribute('%s');
			var tag = el.tagName.toLowerCase();
			var type = el.getAttribute('type') || '';
			var text = '';
			var visible = el.offsetParent !== null || el.style.display !== 'none';
			var action = 'text';

			if (tag === 'input' || tag === 'textarea') {
				action = 'input';
				text = el.placeholder || el.value || '';
			} else if (tag === 'select') {
				action = 'select';
				var opts = [];
				for (var j = 0; j < el.options.length; j++) opts.push(el.options[j].text);
				text = opts.join(', ');
			} else if (tag === 'button' || type === 'submit') {
				action = 'click';
				text = el.textContent.trim().substring(0, 60);
			} else if (tag === 'a') {
				action = 'click';
				text = el.textContent.trim().substring(0, 40);
				var href = el.getAttribute('href');
				if (href) text = text + ' -> ' + href;
			} else if (tag === 'table') {
				action = 'text';
				var headers = [];
				el.querySelectorAll('th').forEach(function(th) { headers.push(th.textContent.trim()); });
				var rows = el.querySelectorAll('tbody tr').length;
				text = headers.join(', ') + ' (' + rows + ' rows)';
			} else {
				text = el.textContent.trim().substring(0, 60);
			}

			results.push({
				id: id,
				tag: tag,
				action: action,
				text: text,
				visible: visible
			});
		}
		return results;
	}`, attrName, attrName)

	result, err := page.Eval(js)
	if err != nil {
		return nil, fmt.Errorf("discover eval failed: %w", err)
	}

	raw := result.Value.JSON("", "")
	var entries []discoverEntry
	if jsonErr := json.Unmarshal([]byte(raw), &entries); jsonErr != nil {
		return nil, fmt.Errorf("failed to parse discover results: %w", jsonErr)
	}
	return entries, nil
}

// formatDiscoverText formats discover entries as human-readable grouped output.
func formatDiscoverText(entries []discoverEntry, attrName, pageURL string) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "Page: %s\n\n", pageURL)

	type group struct {
		label   string
		entries []discoverEntry
	}
	groups := []group{
		{"Readable", nil},
		{"Interactive", nil},
		{"Hidden", nil},
	}
	for _, e := range entries {
		if !e.Visible {
			groups[2].entries = append(groups[2].entries, e)
		} else if e.Action == "text" {
			groups[0].entries = append(groups[0].entries, e)
		} else {
			groups[1].entries = append(groups[1].entries, e)
		}
	}

	sel := func(id string) string {
		return fmt.Sprintf(`[%s="%s"]`, attrName, id)
	}

	for _, g := range groups {
		if len(g.entries) == 0 {
			continue
		}
		fmt.Fprintf(&buf, "%s:\n", g.label)
		for _, e := range g.entries {
			display := e.Text
			if len(display) > 40 {
				display = display[:37] + "..."
			}
			cmd := ""
			switch e.Action {
			case "text":
				cmd = fmt.Sprintf("rodney text '%s'", sel(e.ID))
			case "input":
				cmd = fmt.Sprintf("rodney input '%s' \"<text>\"", sel(e.ID))
			case "click":
				cmd = fmt.Sprintf("rodney click '%s'", sel(e.ID))
			case "select":
				cmd = fmt.Sprintf("rodney select '%s' \"<value>\"", sel(e.ID))
			}
			fmt.Fprintf(&buf, "  %-22s %-42s %s\n", e.ID, display, cmd)
		}
		fmt.Fprintln(&buf)
	}
	return buf.String()
}

// discoverFormEntry represents a single form field found by --forms mode.
type discoverFormEntry struct {
	FormSelector string `json:"form_selector"`
	FormAction   string `json:"form_action,omitempty"`
	Selector     string `json:"selector"`
	Tag          string `json:"tag"`
	Type         string `json:"type,omitempty"`
	Name         string `json:"name,omitempty"`
	Label        string `json:"label,omitempty"`
	Command      string `json:"command"`
}

// queryDiscoverForms finds all forms and their fields, returning structured entries.
func queryDiscoverForms(page *rod.Page) ([]discoverFormEntry, error) {
	js := `() => {
		var results = [];
		var forms = document.querySelectorAll('form');
		forms.forEach(function(form, fi) {
			var formSel = '';
			if (form.id) formSel = 'form#' + form.id;
			else if (form.name) formSel = 'form[name="' + form.name + '"]';
			else if (fi === 0 && forms.length === 1) formSel = 'form';
			else formSel = 'form:nth-of-type(' + (fi+1) + ')';
			var formAction = form.getAttribute('action') || '';

			var fields = form.querySelectorAll('input, select, textarea, button');
			fields.forEach(function(el) {
				var tag = el.tagName.toLowerCase();
				var type = el.getAttribute('type') || '';
				var name = el.getAttribute('name') || '';
				var id = el.id || '';
				var label = '';

				// Try to find associated label
				if (id) {
					var lbl = document.querySelector('label[for="' + id + '"]');
					if (lbl) label = lbl.textContent.trim();
				}
				if (!label && el.getAttribute('aria-label')) {
					label = el.getAttribute('aria-label');
				}
				if (!label && el.placeholder) {
					label = el.placeholder;
				}
				if (!label && tag === 'button') {
					label = el.textContent.trim();
				}

				// Build best selector
				var sel = '';
				if (id) sel = '#' + id;
				else if (name) sel = tag + '[name="' + name + '"]';
				else if (type === 'submit' || tag === 'button') sel = tag + '[type="submit"]';
				else sel = tag;

				// Determine command
				var cmd = '';
				if (tag === 'select') {
					cmd = 'rodney select "' + sel + '" "TODO"';
				} else if (tag === 'textarea' || (tag === 'input' && type !== 'submit' && type !== 'button' && type !== 'file')) {
					cmd = 'rodney input "' + sel + '" "TODO"';
				} else if (type === 'file') {
					cmd = 'rodney file "' + sel + '" "TODO"';
				} else if (tag === 'button' || type === 'submit' || type === 'button') {
					cmd = 'rodney click "' + sel + '"';
				}

				results.push({
					form_selector: formSel,
					form_action: formAction,
					selector: sel,
					tag: tag,
					type: type,
					name: name,
					label: label,
					command: cmd
				});
			});
		});
		return results;
	}`

	result, err := page.Eval(js)
	if err != nil {
		return nil, fmt.Errorf("discover forms eval failed: %w", err)
	}

	raw := result.Value.JSON("", "")
	var entries []discoverFormEntry
	if jsonErr := json.Unmarshal([]byte(raw), &entries); jsonErr != nil {
		return nil, fmt.Errorf("failed to parse form results: %w", jsonErr)
	}
	return entries, nil
}

// formatDiscoverFormsText formats form entries as human-readable output.
func formatDiscoverFormsText(entries []discoverFormEntry) string {
	var buf strings.Builder
	currentForm := ""
	for _, e := range entries {
		if e.FormSelector != currentForm {
			if currentForm != "" {
				fmt.Fprintln(&buf)
			}
			currentForm = e.FormSelector
			header := fmt.Sprintf("Form: %s", e.FormSelector)
			if e.FormAction != "" {
				header += fmt.Sprintf(" (action=%q)", e.FormAction)
			}
			fmt.Fprintln(&buf, header)
		}
		comment := ""
		if e.Label != "" {
			comment = "# " + e.Label
		}
		if e.Type != "" && comment == "" {
			comment = "# (type=" + e.Type + ")"
		} else if e.Type != "" {
			comment += " (type=" + e.Type + ")"
		}
		if comment != "" {
			fmt.Fprintf(&buf, "  %-50s %s\n", e.Command, comment)
		} else {
			fmt.Fprintf(&buf, "  %s\n", e.Command)
		}
	}
	return buf.String()
}

// discoverLinkEntry represents a single link found by --links mode.
type discoverLinkEntry struct {
	Selector string `json:"selector"`
	Href     string `json:"href"`
	Text     string `json:"text"`
	Command  string `json:"command"`
}

// queryDiscoverLinks finds all anchor elements with href attributes.
func queryDiscoverLinks(page *rod.Page) ([]discoverLinkEntry, error) {
	js := `() => {
		var results = [];
		var links = document.querySelectorAll('a[href]');
		links.forEach(function(el) {
			var href = el.getAttribute('href') || '';
			var text = el.textContent.trim().substring(0, 60);
			var id = el.id || '';

			// Build best selector
			var sel = '';
			if (id) sel = 'a#' + id;
			else if (href) sel = 'a[href="' + href.replace(/"/g, '\\"') + '"]';
			else sel = 'a';

			results.push({
				selector: sel,
				href: href,
				text: text,
				command: 'rodney click "' + sel + '"'
			});
		});
		return results;
	}`

	result, err := page.Eval(js)
	if err != nil {
		return nil, fmt.Errorf("discover links eval failed: %w", err)
	}

	raw := result.Value.JSON("", "")
	var entries []discoverLinkEntry
	if jsonErr := json.Unmarshal([]byte(raw), &entries); jsonErr != nil {
		return nil, fmt.Errorf("failed to parse link results: %w", jsonErr)
	}
	return entries, nil
}

// formatDiscoverLinksText formats link entries as human-readable output.
func formatDiscoverLinksText(entries []discoverLinkEntry, pageURL string) string {
	var buf strings.Builder
	if pageURL != "" {
		fmt.Fprintf(&buf, "Links on %s:\n", pageURL)
	} else {
		fmt.Fprintln(&buf, "Links:")
	}
	for _, e := range entries {
		comment := ""
		if e.Text != "" {
			comment = "# " + e.Text
		}
		if comment != "" {
			fmt.Fprintf(&buf, "  %-50s %s\n", e.Command, comment)
		} else {
			fmt.Fprintf(&buf, "  %s\n", e.Command)
		}
	}
	return buf.String()
}

// discoverInteractiveEntry represents an interactive element found by --interactive mode.
type discoverInteractiveEntry struct {
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	Type     string `json:"type,omitempty"`
	Role     string `json:"role,omitempty"`
	Text     string `json:"text"`
	Command  string `json:"command"`
}

// queryDiscoverInteractive finds all interactive/focusable elements on the page.
func queryDiscoverInteractive(page *rod.Page) ([]discoverInteractiveEntry, error) {
	js := `() => {
		var results = [];
		var seen = new Set();
		var els = document.querySelectorAll('button, a[href], input, select, textarea, [role="button"], [tabindex]');
		els.forEach(function(el) {
			// Deduplicate by element reference
			if (seen.has(el)) return;
			seen.add(el);

			var tag = el.tagName.toLowerCase();
			var type = el.getAttribute('type') || '';
			var role = el.getAttribute('role') || '';
			var id = el.id || '';
			var name = el.getAttribute('name') || '';
			var text = '';

			// Get descriptive text
			if (el.getAttribute('aria-label')) {
				text = el.getAttribute('aria-label');
			} else if (tag === 'input' || tag === 'textarea') {
				// Find associated label
				if (id) {
					var lbl = document.querySelector('label[for="' + id + '"]');
					if (lbl) text = lbl.textContent.trim();
				}
				if (!text) text = el.placeholder || '';
			} else {
				text = el.textContent.trim().substring(0, 60);
			}

			// Build best selector
			var sel = '';
			if (id) sel = tag + '#' + id;
			else if (name) sel = tag + '[name="' + name + '"]';
			else if (tag === 'a') {
				var href = el.getAttribute('href');
				if (href) sel = 'a[href="' + href.replace(/"/g, '\\"') + '"]';
				else sel = 'a';
			} else if (role) {
				sel = '[role="' + role + '"]';
			} else {
				sel = tag;
			}

			// Determine command
			var cmd = '';
			if (tag === 'select') {
				cmd = 'rodney select "' + sel + '" "TODO"';
			} else if (tag === 'input' && type === 'file') {
				cmd = 'rodney file "' + sel + '" "TODO"';
			} else if (tag === 'input' || tag === 'textarea') {
				cmd = 'rodney input "' + sel + '" "TODO"';
			} else {
				cmd = 'rodney click "' + sel + '"';
			}

			// Determine effective role for display
			var effectiveRole = role;
			if (!effectiveRole) {
				if (tag === 'button') effectiveRole = 'button';
				else if (tag === 'a') effectiveRole = 'link';
				else if (tag === 'input' && (type === 'text' || type === '' || type === 'email' || type === 'password' || type === 'search' || type === 'tel' || type === 'url' || type === 'number')) effectiveRole = 'textbox';
				else if (tag === 'input' && type === 'checkbox') effectiveRole = 'checkbox';
				else if (tag === 'input' && type === 'radio') effectiveRole = 'radio';
				else if (tag === 'input' && type === 'file') effectiveRole = 'file';
				else if (tag === 'textarea') effectiveRole = 'textbox';
				else if (tag === 'select') effectiveRole = 'combobox';
			}

			results.push({
				selector: sel,
				tag: tag,
				type: type,
				role: effectiveRole,
				text: text,
				command: cmd
			});
		});
		return results;
	}`

	result, err := page.Eval(js)
	if err != nil {
		return nil, fmt.Errorf("discover interactive eval failed: %w", err)
	}

	raw := result.Value.JSON("", "")
	var entries []discoverInteractiveEntry
	if jsonErr := json.Unmarshal([]byte(raw), &entries); jsonErr != nil {
		return nil, fmt.Errorf("failed to parse interactive results: %w", jsonErr)
	}
	return entries, nil
}

// formatDiscoverInteractiveText formats interactive entries as human-readable output.
func formatDiscoverInteractiveText(entries []discoverInteractiveEntry) string {
	var buf strings.Builder
	fmt.Fprintln(&buf, "Interactive elements:")
	for _, e := range entries {
		comment := ""
		if e.Text != "" {
			comment = "# " + e.Text
		}
		if e.Role != "" {
			if comment != "" {
				comment += " (" + e.Role + ")"
			} else {
				comment = "# (" + e.Role + ")"
			}
		}
		if comment != "" {
			fmt.Fprintf(&buf, "  %-50s %s\n", e.Command, comment)
		} else {
			fmt.Fprintf(&buf, "  %s\n", e.Command)
		}
	}
	return buf.String()
}

func cmdDiscover(args []string) {
	jsonOutput := false
	attrName := "data-testid"
	modeForms := false
	modeLinks := false
	modeInteractive := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--attr":
			if i+1 >= len(args) {
				fatal("--attr requires a value")
			}
			i++
			attrName = args[i]
		case "--forms":
			modeForms = true
		case "--links":
			modeLinks = true
		case "--interactive":
			modeInteractive = true
		}
	}

	// Check mutual exclusivity of modes
	modeCount := 0
	if modeForms {
		modeCount++
	}
	if modeLinks {
		modeCount++
	}
	if modeInteractive {
		modeCount++
	}
	if modeCount > 1 {
		fatal("--forms, --links, and --interactive are mutually exclusive")
	}

	_, _, page := withPage()
	info, _ := page.Info()
	pageURL := ""
	if info != nil {
		pageURL = info.URL
	}

	switch {
	case modeForms:
		entries, err := queryDiscoverForms(page)
		if err != nil {
			fatal("%v", err)
		}
		if jsonOutput {
			out, _ := json.MarshalIndent(entries, "", "  ")
			fmt.Println(string(out))
			return
		}
		fmt.Print(formatDiscoverFormsText(entries))

	case modeLinks:
		entries, err := queryDiscoverLinks(page)
		if err != nil {
			fatal("%v", err)
		}
		if jsonOutput {
			out, _ := json.MarshalIndent(entries, "", "  ")
			fmt.Println(string(out))
			return
		}
		fmt.Print(formatDiscoverLinksText(entries, pageURL))

	case modeInteractive:
		entries, err := queryDiscoverInteractive(page)
		if err != nil {
			fatal("%v", err)
		}
		if jsonOutput {
			out, _ := json.MarshalIndent(entries, "", "  ")
			fmt.Println(string(out))
			return
		}
		fmt.Print(formatDiscoverInteractiveText(entries))

	default:
		entries, err := queryDiscoverEntries(page, attrName)
		if err != nil {
			fatal("%v", err)
		}
		if jsonOutput {
			out, _ := json.MarshalIndent(entries, "", "  ")
			fmt.Println(string(out))
			return
		}
		fmt.Print(formatDiscoverText(entries, attrName, pageURL))
	}
}

// queryAXNodes uses Accessibility.queryAXTree to find nodes by name and/or role.
func queryAXNodes(page *rod.Page, name, role string) ([]*proto.AccessibilityAXNode, error) {
	// Get the document node to use as query root
	zero := 0
	doc, err := proto.DOMGetDocument{Depth: &zero}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}

	result, err := proto.AccessibilityQueryAXTree{
		BackendNodeID: doc.Root.BackendNodeID,
		AccessibleName: name,
		Role:           role,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("accessibility query failed: %w", err)
	}

	return result.Nodes, nil
}

// getAXNode gets the accessibility node for a DOM element identified by CSS selector.
func getAXNode(page *rod.Page, selector string) (*proto.AccessibilityAXNode, error) {
	el, err := page.Element(selector)
	if err != nil {
		return nil, fmt.Errorf("element not found: %w", err)
	}

	// Describe the DOM node to get its backend node ID
	node, err := proto.DOMDescribeNode{ObjectID: el.Object.ObjectID}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to describe DOM node: %w", err)
	}

	result, err := proto.AccessibilityGetPartialAXTree{
		BackendNodeID:  node.Node.BackendNodeID,
		FetchRelatives: false,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get accessibility info: %w", err)
	}

	// Find the non-ignored node (the first non-ignored node is typically our target)
	for _, n := range result.Nodes {
		if !n.Ignored {
			return n, nil
		}
	}

	// Fall back to first node if all are ignored
	if len(result.Nodes) > 0 {
		return result.Nodes[0], nil
	}

	return nil, fmt.Errorf("no accessibility node found for selector %q", selector)
}

// axValueStr extracts a printable string from an AccessibilityAXValue.
func axValueStr(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	raw := v.Value.JSON("", "")
	// Unquote JSON strings
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			return s
		}
	}
	return raw
}

// formatAXTree formats a flat list of AX nodes as an indented text tree.
// Ignored nodes are skipped.
func formatAXTree(nodes []*proto.AccessibilityAXNode) string {
	if len(nodes) == 0 {
		return ""
	}

	// Build lookup maps
	nodeByID := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode)
	for _, n := range nodes {
		nodeByID[n.NodeID] = n
	}

	// Find root (node with no parent or first node)
	var rootID proto.AccessibilityAXNodeID
	for _, n := range nodes {
		if n.ParentID == "" {
			rootID = n.NodeID
			break
		}
	}
	if rootID == "" && len(nodes) > 0 {
		rootID = nodes[0].NodeID
	}

	var sb strings.Builder
	var walk func(id proto.AccessibilityAXNodeID, depth int)
	walk = func(id proto.AccessibilityAXNodeID, depth int) {
		node, ok := nodeByID[id]
		if !ok {
			return
		}
		// Skip ignored nodes but still recurse into their children
		if !node.Ignored {
			indent := strings.Repeat("  ", depth)
			role := axValueStr(node.Role)
			name := axValueStr(node.Name)

			line := fmt.Sprintf("%s[%s]", indent, role)
			if name != "" {
				line += fmt.Sprintf(" %q", name)
			}

			// Append interesting properties
			props := formatProperties(node.Properties)
			if props != "" {
				line += " (" + props + ")"
			}

			sb.WriteString(line + "\n")
			// Children at depth+1
			for _, childID := range node.ChildIDs {
				walk(childID, depth+1)
			}
		} else {
			// Ignored node: pass through to children at same depth
			for _, childID := range node.ChildIDs {
				walk(childID, depth)
			}
		}
	}

	walk(rootID, 0)
	return sb.String()
}

// formatProperties formats the interesting AX properties into a comma-separated string.
func formatProperties(props []*proto.AccessibilityAXProperty) string {
	if len(props) == 0 {
		return ""
	}
	var parts []string
	for _, p := range props {
		val := axValueStr(p.Value)
		switch string(p.Name) {
		case "focusable", "disabled", "editable", "hidden", "required",
			"checked", "expanded", "selected", "modal", "multiline",
			"multiselectable", "readonly", "focused", "settable":
			// Boolean-ish properties: only show if true
			if val == "true" {
				parts = append(parts, string(p.Name))
			}
		case "level":
			parts = append(parts, fmt.Sprintf("level=%s", val))
		case "autocomplete", "hasPopup", "orientation", "live",
			"relevant", "valuemin", "valuemax", "valuetext",
			"roledescription", "keyshortcuts":
			if val != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", p.Name, val))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// formatAXTreeJSON formats nodes as a JSON array.
func formatAXTreeJSON(nodes []*proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(data)
}

// formatAXNodeList formats a list of nodes as single-line summaries.
func formatAXNodeList(nodes []*proto.AccessibilityAXNode) string {
	var sb strings.Builder
	for _, node := range nodes {
		role := axValueStr(node.Role)
		name := axValueStr(node.Name)
		line := fmt.Sprintf("[%s]", role)
		if name != "" {
			line += fmt.Sprintf(" %q", name)
		}
		if node.BackendDOMNodeID != 0 {
			line += fmt.Sprintf(" backendNodeId=%d", node.BackendDOMNodeID)
		}
		props := formatProperties(node.Properties)
		if props != "" {
			line += " (" + props + ")"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// formatAXNodeDetail formats a single node with all its properties in key: value format.
func formatAXNodeDetail(node *proto.AccessibilityAXNode) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("role: %s\n", axValueStr(node.Role)))
	if name := axValueStr(node.Name); name != "" {
		sb.WriteString(fmt.Sprintf("name: %s\n", name))
	}
	if desc := axValueStr(node.Description); desc != "" {
		sb.WriteString(fmt.Sprintf("description: %s\n", desc))
	}
	if val := axValueStr(node.Value); val != "" {
		sb.WriteString(fmt.Sprintf("value: %s\n", val))
	}
	for _, p := range node.Properties {
		val := axValueStr(p.Value)
		sb.WriteString(fmt.Sprintf("%s: %s\n", p.Name, val))
	}
	return sb.String()
}

// formatAXNodeDetailJSON formats a single node as JSON.
func formatAXNodeDetailJSON(node *proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(node, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// --- Console logger subprocess ---

func cmdInternalLogger(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney _logger <debugURL> <logsDir>")
	}
	debugURL := args[0]
	logsDir := args[1]

	browser := rod.New().ControlURL(debugURL).MustConnect()
	os.MkdirAll(logsDir, 0755)

	var mu sync.Mutex
	tracking := map[proto.TargetTargetID]bool{}

	// subscribeToPage marks the target as tracked and starts trackPage in a
	// goroutine. It looks up the *rod.Page by target ID; retries briefly in
	// case GetTargets lags slightly behind the TargetCreated event.
	subscribeToPage := func(targetID proto.TargetTargetID) {
		mu.Lock()
		already := tracking[targetID]
		if !already {
			tracking[targetID] = true
		}
		mu.Unlock()
		if already {
			return
		}
		go func() {
			for i := 0; i < 10; i++ {
				pages, _ := browser.Pages()
				for _, p := range pages {
					if p.TargetID == targetID {
						trackPage(p, logsDir)
						return
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	// TargetSetDiscoverTargets causes Chrome to fire TargetTargetCreated for
	// all existing targets immediately, and for every new target thereafter.
	// Set up the listener first so we don't miss events.
	wait := browser.EachEvent(func(e *proto.TargetTargetCreated) bool {
		if e.TargetInfo.Type == "page" {
			subscribeToPage(e.TargetInfo.TargetID)
		}
		return false
	})
	proto.TargetSetDiscoverTargets{Discover: true}.Call(browser)
	go wait()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

// trackPage subscribes to console events for a single page and writes them to
// a per-page NDJSON file. Blocks until the page is closed or context cancelled.
//
// The log file is opened *after* RuntimeEnable returns (which blocks until
// Chrome acks the command). This means the file's creation on disk is an exact
// signal that Chrome is ready to send events — waitForLogger relies on this.
func trackPage(page *rod.Page, logsDir string) {
	logFile := filepath.Join(logsDir, string(page.TargetID)+".ndjson")

	// Register the listener before enabling so no events are missed.
	// f starts nil; callback skips writes until f is set below.
	var f *os.File
	wait := page.EachEvent(func(e *proto.RuntimeConsoleAPICalled) bool {
		if f != nil {
			fmt.Fprintln(f, marshalConsoleEntry(makeConsoleEntry(e)))
			f.Sync()
		}
		return false
	})

	// Enable runtime; blocks until Chrome acknowledges.
	if err := (proto.RuntimeEnable{}).Call(page); err != nil {
		return
	}

	// Open the log file now. Its appearance on disk is the ready signal
	// consumed by waitForLogger in cmdOpen/cmdNewPage.
	var err error
	f, err = os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	wait() // blocks until page closed or context cancelled; f is non-nil
}

// waitForLogger polls until _logger has subscribed to page and called
// RuntimeEnable (signalled by the log file appearing on disk), or until a
// 500ms timeout expires. Called before navigating a freshly-created blank page.
func waitForLogger(page *rod.Page) {
	logFile := filepath.Join(stateDir(), "logs", string(page.TargetID)+".ndjson")
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(logFile); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- Auth proxy for environments with authenticated HTTP proxies ---

// detectProxy checks for HTTPS_PROXY/HTTP_PROXY with credentials.
// Returns (proxyServer, username, password, true) if auth proxy is needed.
func detectProxy() (server, user, pass string, needed bool) {
	proxyEnv := os.Getenv("HTTPS_PROXY")
	if proxyEnv == "" {
		proxyEnv = os.Getenv("https_proxy")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("HTTP_PROXY")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("http_proxy")
	}
	if proxyEnv == "" {
		return "", "", "", false
	}
	parsed, err := url.Parse(proxyEnv)
	if err != nil || parsed.User == nil {
		return "", "", "", false
	}
	user = parsed.User.Username()
	pass, _ = parsed.User.Password()
	if user == "" {
		return "", "", "", false
	}
	server = parsed.Hostname() + ":" + parsed.Port()
	return server, user, pass, true
}

// cmdInternalProxy is a hidden subcommand: rodney _proxy <port> <upstream> <authHeader>
// It runs a local auth proxy that forwards to the upstream proxy with credentials.
func cmdInternalProxy(args []string) {
	if len(args) < 3 {
		fatal("usage: rodney _proxy <port> <upstream> <authHeader>")
	}
	port := args[0]
	upstream := args[1]
	authHeader := args[2]

	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fatal("proxy listen failed: %v", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				proxyConnect(w, r, upstream, authHeader)
			} else {
				proxyHTTP(w, r, upstream, authHeader)
			}
		}),
	}
	server.Serve(listener) // blocks forever
}

func proxyConnect(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	upstreamConn, err := net.DialTimeout("tcp", upstream, 30*time.Second)
	if err != nil {
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		r.Host, r.Host, authHeader)
	if _, err := upstreamConn.Write([]byte(connectReq)); err != nil {
		upstreamConn.Close()
		http.Error(w, "upstream write failed", http.StatusBadGateway)
		return
	}

	buf := make([]byte, 4096)
	n, err := upstreamConn.Read(buf)
	if err != nil {
		upstreamConn.Close()
		http.Error(w, "upstream read failed", http.StatusBadGateway)
		return
	}
	response := string(buf[:n])
	if len(response) < 12 || response[9:12] != "200" {
		upstreamConn.Close()
		http.Error(w, "upstream rejected CONNECT", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		return
	}

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go func() {
		io.Copy(upstreamConn, clientConn)
		upstreamConn.Close()
	}()
	go func() {
		io.Copy(clientConn, upstreamConn)
		clientConn.Close()
	}()
}

func proxyHTTP(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	proxyURL, _ := url.Parse("http://" + upstream)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		ProxyConnectHeader: http.Header{
			"Proxy-Authorization": {authHeader},
		},
	}
	r.Header.Set("Proxy-Authorization", authHeader)

	resp, err := transport.RoundTrip(r)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
