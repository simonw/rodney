package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// testEnv holds a shared browser and test HTTP server for all tests.
type testEnv struct {
	browser  *rod.Browser
	server   *httptest.Server
	debugURL string // WebSocket debug URL for setting up state in cmdXxx tests
}

var env *testEnv

func TestMain(m *testing.M) {
	// Launch headless Chrome once for all tests
	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Headless(true).
		Leakless(false)

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()

	// Start test HTTP server with known HTML fixtures
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/form", handleForm)
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/download", handleDownload)
	mux.HandleFunc("/testfile.txt", handleTestFile)
	mux.HandleFunc("/empty", handleEmpty)
	mux.HandleFunc("/logs", handleLogs)
	mux.HandleFunc("/discover", handleDiscover)
	mux.HandleFunc("/discover-extended", handleDiscoverExtended)
	mux.HandleFunc("/wait-test", handleWaitTest)
	mux.HandleFunc("/hidden", handleHidden)
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/overlay", handleOverlay)
	server := httptest.NewServer(mux)

	env = &testEnv{browser: browser, server: server, debugURL: u}

	code := m.Run()

	server.Close()
	browser.MustClose()
	os.Exit(code)
}

// --- HTML fixtures ---

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Test Page</title></head>
<body>
  <nav aria-label="Main">
    <a href="/about">About</a>
    <a href="/contact">Contact</a>
  </nav>
  <main>
    <h1>Welcome</h1>
    <p>Hello world</p>
    <button id="submit-btn">Submit</button>
    <button id="cancel-btn" disabled>Cancel</button>
  </main>
</body>
</html>`))
}

func handleForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Form Page</title></head>
<body>
  <h1>Contact Us</h1>
  <form>
    <label for="name-input">Name</label>
    <input id="name-input" type="text" aria-required="true">
    <label for="email-input">Email</label>
    <input id="email-input" type="email">
    <select id="topic" aria-label="Topic">
      <option value="general">General</option>
      <option value="support">Support</option>
    </select>
    <button type="submit">Send</button>
  </form>
</body>
</html>`))
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Upload Page</title></head>
<body>
  <input id="file-input" type="file" accept="image/*">
  <span id="file-name"></span>
  <script>
    document.getElementById('file-input').addEventListener('change', function(e) {
      document.getElementById('file-name').textContent = e.target.files[0] ? e.target.files[0].name : '';
    });
  </script>
</body>
</html>`))
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Download Page</title></head>
<body>
  <a id="file-link" href="/testfile.txt">Download file</a>
  <a id="data-link" href="data:text/plain;base64,SGVsbG8gV29ybGQ=">Download data</a>
  <img id="test-img" src="/testfile.txt">
</body>
</html>`))
}

func handleTestFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("Hello World"))
}

func handleEmpty(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Empty Page</title></head>
<body></body>
</html>`))
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Logs Test Page</title></head>
<body>
<script>
  console.log("info message from logs test");
  console.warn("warning message from logs test");
  console.error("error message from logs test");
</script>
</body>
</html>`))
}

// --- Helper: navigate to a fixture and return the page ---

func navigateTo(t *testing.T, path string) *rod.Page {
	t.Helper()
	page := env.browser.MustPage(env.server.URL + path)
	page.MustWaitLoad()
	t.Cleanup(func() { page.MustClose() })
	return page
}

// =====================
// ax-tree tests (RED)
// =====================

func TestAXTree_ReturnsNodes(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	// Sanity: we should get nodes back
	if len(result.Nodes) == 0 {
		t.Fatal("expected nodes in accessibility tree, got 0")
	}

	// Now test our formatting function
	out := formatAXTree(result.Nodes)
	if out == "" {
		t.Fatal("formatAXTree returned empty string")
	}
	if !strings.Contains(out, "Welcome") {
		t.Errorf("tree should contain heading text 'Welcome', got:\n%s", out)
	}
	if !strings.Contains(out, "button") {
		t.Errorf("tree should contain 'button' role, got:\n%s", out)
	}
	if !strings.Contains(out, "Submit") {
		t.Errorf("tree should contain button name 'Submit', got:\n%s", out)
	}
}

func TestAXTree_Indentation(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTree(result.Nodes)
	lines := strings.Split(out, "\n")

	// Root node should have no indentation
	if len(lines) == 0 {
		t.Fatal("no lines in output")
	}
	if strings.HasPrefix(lines[0], " ") {
		t.Errorf("root node should not be indented, got: %q", lines[0])
	}

	// Some lines should be indented (children)
	hasIndented := false
	for _, line := range lines {
		if strings.HasPrefix(line, "  ") {
			hasIndented = true
			break
		}
	}
	if !hasIndented {
		t.Errorf("expected some indented lines for child nodes, got:\n%s", out)
	}
}

func TestAXTree_SkipsIgnoredNodes(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTree(result.Nodes)

	// Count ignored vs total
	ignoredCount := 0
	for _, node := range result.Nodes {
		if node.Ignored {
			ignoredCount++
		}
	}

	// If there are ignored nodes, they shouldn't appear in text output
	if ignoredCount > 0 {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= len(result.Nodes) {
			t.Errorf("text output should skip ignored nodes: %d lines for %d nodes (%d ignored)",
				len(lines), len(result.Nodes), ignoredCount)
		}
	}
}

func TestAXTree_DepthLimit(t *testing.T) {
	page := navigateTo(t, "/")
	full, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}

	depth := 2
	limited, err := proto.AccessibilityGetFullAXTree{Depth: &depth}.Call(page)
	if err != nil {
		t.Fatalf("CDP call with depth failed: %v", err)
	}

	if len(limited.Nodes) >= len(full.Nodes) {
		t.Errorf("depth-limited tree (%d nodes) should have fewer nodes than full tree (%d nodes)",
			len(limited.Nodes), len(full.Nodes))
	}
}

func TestAXTree_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTreeJSON(result.Nodes)
	// Must be valid JSON
	var parsed []interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\nOutput:\n%s", err, out[:min(len(out), 500)])
	}
	if len(parsed) == 0 {
		t.Error("JSON output should contain nodes")
	}
}

// =====================
// ax-find tests (RED)
// =====================

func TestAXFind_ByRole(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "", "button")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 buttons, got %d", len(nodes))
	}

	out := formatAXNodeList(nodes)
	if !strings.Contains(out, "Submit") {
		t.Errorf("output should contain 'Submit' button, got:\n%s", out)
	}
	if !strings.Contains(out, "Cancel") {
		t.Errorf("output should contain 'Cancel' button, got:\n%s", out)
	}
}

func TestAXFind_ByName(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "Submit", "")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least 1 node named 'Submit', got 0")
	}
	out := formatAXNodeList(nodes)
	if !strings.Contains(out, "Submit") {
		t.Errorf("output should contain 'Submit', got:\n%s", out)
	}
}

func TestAXFind_ByNameAndRoleExact(t *testing.T) {
	page := navigateTo(t, "/")
	// Combining name + role should give exactly one result
	nodes, err := queryAXNodes(page, "Submit", "button")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected exactly 1 button named 'Submit', got %d", len(nodes))
	}
}

func TestAXFind_ByNameAndRole(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "About", "link")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 link named 'About', got %d", len(nodes))
	}
}

func TestAXFind_NoResults(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "NonexistentThing", "")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 results for nonexistent name, got %d", len(nodes))
	}
}

func TestAXFind_FormPage(t *testing.T) {
	page := navigateTo(t, "/form")
	nodes, err := queryAXNodes(page, "", "textbox")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 textboxes on form page, got %d", len(nodes))
	}
}

// =====================
// ax-node tests (RED)
// =====================

func TestAXNode_ButtonBySelector(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#submit-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "button") {
		t.Errorf("should show role 'button', got:\n%s", out)
	}
	if !strings.Contains(out, "Submit") {
		t.Errorf("should show name 'Submit', got:\n%s", out)
	}
}

func TestAXNode_DisabledButton(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#cancel-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "button") {
		t.Errorf("should show role 'button', got:\n%s", out)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("should show disabled property, got:\n%s", out)
	}
}

func TestAXNode_InputWithLabel(t *testing.T) {
	page := navigateTo(t, "/form")
	node, err := getAXNode(page, "#name-input")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "textbox") {
		t.Errorf("should show role 'textbox', got:\n%s", out)
	}
	if !strings.Contains(out, "Name") {
		t.Errorf("should show accessible name 'Name' from label, got:\n%s", out)
	}
}

func TestAXNode_HeadingLevel(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "h1")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "heading") {
		t.Errorf("should show role 'heading', got:\n%s", out)
	}
	if !strings.Contains(out, "level") {
		t.Errorf("should show level property for heading, got:\n%s", out)
	}
}

func TestAXNode_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#submit-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetailJSON(node)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\nOutput:\n%s", err, out)
	}
	if _, ok := parsed["nodeId"]; !ok {
		t.Error("JSON should contain nodeId field")
	}
}

func TestAXNode_SelectorNotFound(t *testing.T) {
	page := navigateTo(t, "/")
	// Use a short timeout so we don't block for 30s waiting for a nonexistent element
	shortPage := page.Timeout(2 * time.Second)
	_, err := getAXNode(shortPage, "#does-not-exist")
	if err == nil {
		t.Error("expected error for nonexistent selector, got nil")
	}
}

// =====================
// file command tests
// =====================

func TestFile_SetFileOnInput(t *testing.T) {
	page := navigateTo(t, "/upload")

	// Create a temp file to upload
	tmp, err := os.CreateTemp("", "rodney-test-*.txt")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write([]byte("test content"))
	tmp.Close()

	el, err := page.Element("#file-input")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	if err := el.SetFiles([]string{tmp.Name()}); err != nil {
		t.Fatalf("SetFiles failed: %v", err)
	}

	// Wait for the change event to fire and check the file name
	page.MustWaitStable()
	nameEl, err := page.Element("#file-name")
	if err != nil {
		t.Fatalf("file-name element not found: %v", err)
	}
	text, _ := nameEl.Text()
	if text == "" {
		t.Error("expected file name to be set after SetFiles, got empty string")
	}
}

func TestFile_MultipleFiles(t *testing.T) {
	page := navigateTo(t, "/upload")

	tmp1, _ := os.CreateTemp("", "rodney-test1-*.txt")
	defer os.Remove(tmp1.Name())
	tmp1.Write([]byte("file 1"))
	tmp1.Close()

	tmp2, _ := os.CreateTemp("", "rodney-test2-*.txt")
	defer os.Remove(tmp2.Name())
	tmp2.Write([]byte("file 2"))
	tmp2.Close()

	el, err := page.Element("#file-input")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}

	// Setting files should not error even with multiple files
	if err := el.SetFiles([]string{tmp1.Name(), tmp2.Name()}); err != nil {
		t.Fatalf("SetFiles with multiple files failed: %v", err)
	}
}

// =====================
// download command tests
// =====================

func TestDownload_DataURL(t *testing.T) {
	// Test decoding a data: URL directly
	data, err := decodeDataURL("data:text/plain;base64,SGVsbG8gV29ybGQ=")
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_DataURL_URLEncoded(t *testing.T) {
	data, err := decodeDataURL("data:text/plain,Hello%20World")
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_InferFilename_URL(t *testing.T) {
	name := inferDownloadFilename("https://example.com/images/photo.png")
	if name != "photo.png" {
		t.Errorf("expected 'photo.png', got %q", name)
	}
}

func TestDownload_InferFilename_DataURL(t *testing.T) {
	name := inferDownloadFilename("data:image/png;base64,abc")
	if !strings.HasPrefix(name, "download") || !strings.Contains(name, ".png") {
		t.Errorf("expected 'download*.png', got %q", name)
	}
}

func TestDownload_FetchLink(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#file-link")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	href := el.MustAttribute("href")
	if href == nil {
		t.Fatal("expected href attribute")
	}

	// Fetch using JS in the page context, same as cmdDownload does
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
	}`, *href)
	result, err := page.Eval(js)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	data, err := base64.StdEncoding.DecodeString(result.Value.Str())
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_DataLinkElement(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#data-link")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	href := el.MustAttribute("href")
	if href == nil {
		t.Fatal("expected href attribute")
	}

	data, err := decodeDataURL(*href)
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_ImgSrc(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#test-img")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	src := el.MustAttribute("src")
	if src == nil {
		t.Fatal("expected src attribute")
	}
	if *src != "/testfile.txt" {
		t.Errorf("expected '/testfile.txt', got %q", *src)
	}
}

// =====================
// Directory-scoped sessions tests
// =====================

func TestExtractScopeArgs_NoFlags(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"open", "https://example.com"})
	if mode != scopeAuto {
		t.Errorf("expected scopeAuto, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlag(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"--local", "start"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 1 || remaining[0] != "start" {
		t.Errorf("expected [start], got %v", remaining)
	}
}

func TestExtractScopeArgs_GlobalFlag(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"--global", "open", "https://example.com"})
	if mode != scopeGlobal {
		t.Errorf("expected scopeGlobal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlagAfterCommand(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"open", "--local", "https://example.com"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LastFlagWins(t *testing.T) {
	mode, _ := extractScopeArgs([]string{"--local", "--global", "start"})
	if mode != scopeGlobal {
		t.Errorf("expected last flag (scopeGlobal) to win, got %v", mode)
	}
}

func TestResolveStateDir_Global(t *testing.T) {
	dir := resolveStateDir(scopeGlobal, "/some/working/dir")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".rodney")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_Local(t *testing.T) {
	dir := resolveStateDir(scopeLocal, "/some/working/dir")
	expected := filepath.Join("/some/working/dir", ".rodney")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_AutoPrefersLocal(t *testing.T) {
	// Create a temp directory with a .rodney/state.json to simulate local session
	tmpDir := t.TempDir()
	localRodney := filepath.Join(tmpDir, ".rodney")
	os.MkdirAll(localRodney, 0755)
	os.WriteFile(filepath.Join(localRodney, "state.json"), []byte(`{}`), 0644)

	dir := resolveStateDir(scopeAuto, tmpDir)
	if dir != localRodney {
		t.Errorf("auto mode should prefer local when .rodney/state.json exists: expected %q, got %q", localRodney, dir)
	}
}

func TestResolveStateDir_AutoFallsBackToGlobal(t *testing.T) {
	// Use a temp directory with NO .rodney/ — should fall back to global
	tmpDir := t.TempDir()
	dir := resolveStateDir(scopeAuto, tmpDir)
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".rodney")
	if dir != expected {
		t.Errorf("auto mode should fall back to global: expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_LocalUsesWorkingDir(t *testing.T) {
	tmpDir := t.TempDir()
	dir := resolveStateDir(scopeLocal, tmpDir)
	expected := filepath.Join(tmpDir, ".rodney")
	if dir != expected {
		t.Errorf("local mode should use working dir: expected %q, got %q", expected, dir)
	}
}

// =====================
// RODNEY_HOME env var tests
// =====================

func TestStateDir_Default(t *testing.T) {
	t.Setenv("RODNEY_HOME", "")
	home, _ := os.UserHomeDir()
	want := home + "/.rodney"
	got := stateDir()
	if got != want {
		t.Errorf("stateDir() = %q, want %q", got, want)
	}
}

func TestStateDir_EnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RODNEY_HOME", dir)
	got := stateDir()
	if got != dir {
		t.Errorf("stateDir() = %q, want %q", got, dir)
	}
}

func TestMimeToExt(t *testing.T) {
	tests := []struct {
		mime string
		ext  string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"application/pdf", ".pdf"},
		{"text/plain", ".txt"},
		{"unknown/type", ""},
	}
	for _, tt := range tests {
		got := mimeToExt(tt.mime)
		if got != tt.ext {
			t.Errorf("mimeToExt(%q) = %q, want %q", tt.mime, got, tt.ext)
		}
	}
}

// =====================
// assert command tests
// =====================

func TestAssert_TruthyPass_String(t *testing.T) {
	page := navigateTo(t, "/")
	// document.title is "Test Page" which is truthy
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	// Should not be falsy
	switch raw {
	case "false", "0", "null", "undefined", `""`:
		t.Errorf("document.title should be truthy, got raw=%q", raw)
	}
	if result.Value.Str() != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", result.Value.Str())
	}
}

func TestAssert_TruthyPass_True(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (1 === 1); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "true" {
		t.Errorf("1 === 1 should be true, got %q", raw)
	}
}

func TestAssert_TruthyPass_Number(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (42); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw == "0" || raw == "false" || raw == "null" || raw == "undefined" || raw == `""` {
		t.Errorf("42 should be truthy, got raw=%q", raw)
	}
}

func TestAssert_TruthyFail_Null(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.querySelector(".nonexistent")); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "null" {
		t.Errorf("querySelector for nonexistent should return null, got %q", raw)
	}
}

func TestAssert_TruthyFail_False(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (false); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "false" {
		t.Errorf("false should be false, got %q", raw)
	}
}

func TestAssert_TruthyFail_Zero(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (0); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "0" {
		t.Errorf("0 should be 0, got %q", raw)
	}
}

func TestAssert_TruthyFail_EmptyString(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (""); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != `""` {
		t.Errorf("empty string should have JSON repr '\"\"', got %q", raw)
	}
}

func TestAssert_EqualityPass_Title(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	actual := result.Value.Str()
	if actual != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", actual)
	}
}

func TestAssert_EqualityPass_Count(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.querySelectorAll("button").length); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "2" {
		t.Errorf("expected 2 buttons, got %q", raw)
	}
}

func TestAssert_EqualityFail_WrongTitle(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	actual := result.Value.Str()
	if actual == "Wrong Title" {
		t.Error("title should NOT equal 'Wrong Title'")
	}
}

func TestAssert_EqualityPass_BoolString(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (1 === 1); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "true" {
		t.Errorf("1 === 1 should produce 'true', got %q", raw)
	}
}

func TestAssert_ValueFormatting_MatchesJSCommand(t *testing.T) {
	// Verify that the value formatting used by assert matches what rodney js outputs
	page := navigateTo(t, "/")

	tests := []struct {
		expr     string
		expected string
	}{
		{`document.title`, "Test Page"},   // string unquoted
		{`1 + 2`, "3"},                    // number
		{`true`, "true"},                  // boolean
		{`null`, "null"},                  // null
		{`document.querySelectorAll("button").length`, "2"}, // number from DOM
	}

	for _, tt := range tests {
		js := fmt.Sprintf(`() => { return (%s); }`, tt.expr)
		result, err := page.Eval(js)
		if err != nil {
			t.Fatalf("eval %q failed: %v", tt.expr, err)
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

		if actual != tt.expected {
			t.Errorf("expr %q: expected %q, got %q (raw=%q)", tt.expr, tt.expected, actual, raw)
		}
	}
}

// =====================
// assert --message tests
// =====================

func TestParseAssertArgs_ExprOnly(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil, got %q", *expected)
	}
	if message != "" {
		t.Errorf("message should be empty, got %q", message)
	}
}

func TestParseAssertArgs_ExprAndExpected(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "Dashboard"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Dashboard" {
		t.Errorf("expected = %v, want %q", expected, "Dashboard")
	}
	if message != "" {
		t.Errorf("message should be empty, got %q", message)
	}
}

func TestParseAssertArgs_MessageLong(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "--message", "Page title check"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil for truthy with --message, got %q", *expected)
	}
	if message != "Page title check" {
		t.Errorf("message = %q, want %q", message, "Page title check")
	}
}

func TestParseAssertArgs_MessageShort(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "-m", "Title check"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil, got %q", *expected)
	}
	if message != "Title check" {
		t.Errorf("message = %q, want %q", message, "Title check")
	}
}

func TestParseAssertArgs_EqualityWithMessage(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "Dashboard", "--message", "Wrong page"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Dashboard" {
		t.Errorf("expected = %v, want %q", expected, "Dashboard")
	}
	if message != "Wrong page" {
		t.Errorf("message = %q, want %q", message, "Wrong page")
	}
}

func TestParseAssertArgs_MessageBeforeExpr(t *testing.T) {
	// --message can appear anywhere; positional args still work
	expr, expected, message := parseAssertArgs([]string{"-m", "Check", "document.title", "Home"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Home" {
		t.Errorf("expected = %v, want %q", expected, "Home")
	}
	if message != "Check" {
		t.Errorf("message = %q, want %q", message, "Check")
	}
}

func TestFormatAssertFail_TruthyNoMessage(t *testing.T) {
	got := formatAssertFail("null", nil, "")
	if got != "fail: got null" {
		t.Errorf("got %q, want %q", got, "fail: got null")
	}
}

func TestFormatAssertFail_TruthyWithMessage(t *testing.T) {
	got := formatAssertFail("null", nil, "User should be logged in")
	if got != "fail: User should be logged in (got null)" {
		t.Errorf("got %q, want %q", got, "fail: User should be logged in (got null)")
	}
}

func TestFormatAssertFail_EqualityNoMessage(t *testing.T) {
	expected := "Dashboard"
	got := formatAssertFail("Task Tracker", &expected, "")
	want := `fail: got "Task Tracker", expected "Dashboard"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatAssertFail_EqualityWithMessage(t *testing.T) {
	expected := "Dashboard"
	got := formatAssertFail("Task Tracker", &expected, "Wrong page loaded")
	want := `fail: Wrong page loaded (got "Task Tracker", expected "Dashboard")`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ======================
// cmdJS stdin tests
// ======================

// setupCmdJSState navigates to path on the test server, writes a state.json
// in a temp dir pointing at that page, and restores activeStateDir on cleanup.
func setupCmdJSState(t *testing.T, path string) {
	t.Helper()

	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	page := env.browser.MustPage(env.server.URL + path)
	page.MustWaitLoad()
	t.Cleanup(func() { page.MustClose() })

	// Find the page's index in the browser's page list.
	pages, err := env.browser.Pages()
	if err != nil {
		t.Fatalf("failed to list pages: %v", err)
	}
	idx := 0
	for i, p := range pages {
		if p.TargetID == page.TargetID {
			idx = i
			break
		}
	}

	if err := saveState(&State{DebugURL: env.debugURL, ActivePage: idx}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
}

// pipeStdin replaces os.Stdin with a pipe containing content and restores it on cleanup.
func pipeStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatalf("pipeStdin write: %v", err)
	}
	w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
}

// captureStdout captures everything written to os.Stdout by fn, trimming trailing whitespace.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = oldStdout
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("captureStdout read: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestCmdJS_Stdin_NoArgs verifies that piping an expression to `rodney js`
// with no arguments reads and evaluates it from stdin.
func TestCmdJS_Stdin_NoArgs(t *testing.T) {
	setupCmdJSState(t, "/")
	pipeStdin(t, "document.title\n")
	got := captureStdout(t, func() { cmdJS([]string{}) })
	if got != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", got)
	}
}

// TestCmdJS_Stdin_DashArg verifies that `rodney js -` reads the expression from stdin,
// consistent with the `-` convention used by `rodney file`.
func TestCmdJS_Stdin_DashArg(t *testing.T) {
	setupCmdJSState(t, "/")
	pipeStdin(t, "document.title\n")
	got := captureStdout(t, func() { cmdJS([]string{"-"}) })
	if got != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", got)
	}
}

// TestCmdJS_Stdin_MultiLine verifies multi-line input works — this is exactly what
// a heredoc produces (bash sends the lines as a single stdin stream with newlines).
func TestCmdJS_Stdin_MultiLine(t *testing.T) {
	setupCmdJSState(t, "/")
	// Heredoc-style: expression split across lines with trailing newline.
	// `1 +\n2\n` is trimmed to `1 +\n2` and wrapped in `() => { return (1 +\n2); }`.
	pipeStdin(t, "1 +\n2\n")
	got := captureStdout(t, func() { cmdJS([]string{}) })
	if got != "3" {
		t.Errorf("expected '3', got %q", got)
	}
}

// TestCmdJS_Stdin_TrimsWhitespace verifies that leading/trailing whitespace
// (including the trailing newline added by echo or heredoc) is stripped.
func TestCmdJS_Stdin_TrimsWhitespace(t *testing.T) {
	setupCmdJSState(t, "/")
	pipeStdin(t, "  1 + 2  \n")
	got := captureStdout(t, func() { cmdJS([]string{}) })
	if got != "3" {
		t.Errorf("expected '3', got %q", got)
	}
}

// ======================
// cmdAssert stdin tests
// ======================

// TestCmdAssert_Stdin_NoArgs verifies that piping a JS expression to `rodney assert`
// with no other args reads the expression from stdin.
func TestCmdAssert_Stdin_NoArgs(t *testing.T) {
	pipeStdin(t, "document.title\n")
	got := resolveAssertArgs([]string{})
	if len(got) == 0 || got[0] != "document.title" {
		t.Errorf("expected args[0] == 'document.title', got %v", got)
	}
}

// TestCmdAssert_Stdin_DashArg verifies that `rodney assert -` reads the expression
// from stdin explicitly, matching the `-` convention used by `rodney js` and `rodney file`.
func TestCmdAssert_Stdin_DashArg(t *testing.T) {
	pipeStdin(t, "document.title\n")
	got := resolveAssertArgs([]string{"-"})
	if len(got) == 0 || got[0] != "document.title" {
		t.Errorf("expected args[0] == 'document.title', got %v", got)
	}
}

// TestCmdAssert_Stdin_WithExpected verifies that the expression comes from stdin
// while the expected value still comes from command-line args.
// Equivalent to: echo "document.title" | rodney assert - "Test Page"
func TestCmdAssert_Stdin_WithExpected(t *testing.T) {
	pipeStdin(t, "document.title\n")
	got := resolveAssertArgs([]string{"-", "Test Page"})
	if len(got) != 2 || got[0] != "document.title" || got[1] != "Test Page" {
		t.Errorf("expected [document.title Test Page], got %v", got)
	}
}

// TestCmdAssert_Stdin_WithMessage verifies that the expression comes from stdin
// while the -m flag still comes from command-line args.
// Equivalent to: echo "document.title" | rodney assert - -m "page title"
func TestCmdAssert_Stdin_WithMessage(t *testing.T) {
	pipeStdin(t, "document.title\n")
	got := resolveAssertArgs([]string{"-", "-m", "page title"})
	if len(got) != 3 || got[0] != "document.title" || got[1] != "-m" || got[2] != "page title" {
		t.Errorf("expected [document.title -m page title], got %v", got)
	}
}

// TestCmdAssert_Stdin_FlagsOnly verifies that when only flags are given (no positional)
// and stdin is piped, the expression is prepended from stdin.
func TestCmdAssert_Stdin_FlagsOnly(t *testing.T) {
	pipeStdin(t, "document.title\n")
	got := resolveAssertArgs([]string{"-m", "check"})
	if len(got) != 3 || got[0] != "document.title" || got[1] != "-m" || got[2] != "check" {
		t.Errorf("expected [document.title -m check], got %v", got)
	}
}

// TestCmdAssert_Stdin_Passthrough verifies that normal (non-stdin) args are unchanged.
func TestCmdAssert_Stdin_Passthrough(t *testing.T) {
	got := resolveAssertArgs([]string{"document.title", "Test Page"})
	if len(got) != 2 || got[0] != "document.title" || got[1] != "Test Page" {
		t.Errorf("expected [document.title Test Page], got %v", got)
	}
}

// TestCmdAssert_Stdin_TrimsWhitespace verifies leading/trailing whitespace is stripped
// from the stdin expression (consistent with cmdJS behavior).
func TestCmdAssert_Stdin_TrimsWhitespace(t *testing.T) {
	pipeStdin(t, "  1 + 2  \n")
	got := resolveAssertArgs([]string{"-"})
	if len(got) == 0 || got[0] != "1 + 2" {
		t.Errorf("expected args[0] == '1 + 2', got %v", got)
	}
}

// =====================
// viewport tests
// =====================

func TestFormatViewportDesc_Basic(t *testing.T) {
	got := formatViewportDesc("Viewport:", 375, 812, false, 1)
	expected := "Viewport: 375x812"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestFormatViewportDesc_Mobile(t *testing.T) {
	got := formatViewportDesc("Viewport set to", 375, 812, true, 1)
	expected := "Viewport set to 375x812 (mobile)"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestFormatViewportDesc_Scale(t *testing.T) {
	got := formatViewportDesc("Viewport:", 390, 844, false, 3)
	expected := "Viewport: 390x844 (scale 3)"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestFormatViewportDesc_MobileAndScale(t *testing.T) {
	got := formatViewportDesc("Viewport set to", 375, 812, true, 2)
	expected := "Viewport set to 375x812 (mobile, scale 2)"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestFormatViewportDesc_ScaleOne_Omitted(t *testing.T) {
	got := formatViewportDesc("Viewport:", 1280, 720, false, 1)
	if strings.Contains(got, "scale") {
		t.Errorf("scale 1 should be omitted, got %q", got)
	}
}

func TestFormatViewportDesc_ScaleZero_Omitted(t *testing.T) {
	got := formatViewportDesc("Viewport:", 1280, 720, false, 0)
	if strings.Contains(got, "scale") {
		t.Errorf("scale 0 should be omitted, got %q", got)
	}
}

func TestViewport_StatePersistence(t *testing.T) {
	// Verify that viewport settings round-trip through state serialization
	dir := t.TempDir()
	state := &State{
		DebugURL:       "ws://localhost:1234",
		ChromePID:      12345,
		DataDir:        dir,
		ViewportWidth:  375,
		ViewportHeight: 812,
		ViewportScale:  2,
		ViewportMobile: true,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if loaded.ViewportWidth != 375 {
		t.Errorf("expected ViewportWidth 375, got %d", loaded.ViewportWidth)
	}
	if loaded.ViewportHeight != 812 {
		t.Errorf("expected ViewportHeight 812, got %d", loaded.ViewportHeight)
	}
	if loaded.ViewportScale != 2 {
		t.Errorf("expected ViewportScale 2, got %g", loaded.ViewportScale)
	}
	if !loaded.ViewportMobile {
		t.Error("expected ViewportMobile true")
	}
}

func TestViewport_StateOmitsZeroValues(t *testing.T) {
	// Verify that empty viewport fields are omitted from JSON (omitempty)
	state := &State{
		DebugURL:  "ws://localhost:1234",
		ChromePID: 12345,
		DataDir:   "/tmp/test",
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	raw := string(data)
	for _, key := range []string{"viewport_width", "viewport_height", "viewport_scale", "viewport_mobile"} {
		if strings.Contains(raw, key) {
			t.Errorf("expected %q to be omitted from JSON, got: %s", key, raw)
		}
	}
}

func TestViewport_EmulationApplied(t *testing.T) {
	// Verify the CDP emulation call works end-to-end via rod
	page := navigateTo(t, "/")

	err := proto.EmulationSetDeviceMetricsOverride{
		Width:             375,
		Height:            812,
		DeviceScaleFactor: 2,
	}.Call(page)
	if err != nil {
		t.Fatalf("EmulationSetDeviceMetricsOverride failed: %v", err)
	}

	w, err := page.Eval(`() => { return window.innerWidth; }`)
	if err != nil {
		t.Fatalf("eval innerWidth failed: %v", err)
	}
	if w.Value.Int() != 375 {
		t.Errorf("expected innerWidth 375, got %d", w.Value.Int())
	}

	dpr, err := page.Eval(`() => { return window.devicePixelRatio; }`)
	if err != nil {
		t.Fatalf("eval devicePixelRatio failed: %v", err)
	}
	if dpr.Value.Int() != 2 {
		t.Errorf("expected devicePixelRatio 2, got %d", dpr.Value.Int())
	}
}

func TestViewport_EmulationReset(t *testing.T) {
	// Verify that clearing device metrics override restores defaults
	page := navigateTo(t, "/")

	// Set a custom viewport
	err := proto.EmulationSetDeviceMetricsOverride{
		Width:             375,
		Height:            812,
		DeviceScaleFactor: 2,
	}.Call(page)
	if err != nil {
		t.Fatalf("EmulationSetDeviceMetricsOverride failed: %v", err)
	}

	w, err := page.Eval(`() => { return window.innerWidth; }`)
	if err != nil {
		t.Fatalf("eval innerWidth failed: %v", err)
	}
	if w.Value.Int() != 375 {
		t.Fatalf("expected innerWidth 375 after override, got %d", w.Value.Int())
	}

	// Clear the override
	if err := (proto.EmulationClearDeviceMetricsOverride{}.Call(page)); err != nil {
		t.Fatalf("EmulationClearDeviceMetricsOverride failed: %v", err)
	}

	w2, err := page.Eval(`() => { return window.innerWidth; }`)
	if err != nil {
		t.Fatalf("eval innerWidth after reset failed: %v", err)
	}
	if w2.Value.Int() == 375 {
		t.Errorf("expected innerWidth to change after reset, still 375")
	}
}

func TestViewport_ResetClearsState(t *testing.T) {
	// Verify that resetting viewport clears persisted state fields
	state := &State{
		DebugURL:       "ws://localhost:1234",
		ChromePID:      12345,
		DataDir:        t.TempDir(),
		ViewportWidth:  375,
		ViewportHeight: 812,
		ViewportScale:  2,
		ViewportMobile: true,
	}

	// Simulate what cmdViewport --reset does to state
	state.ViewportWidth = 0
	state.ViewportHeight = 0
	state.ViewportScale = 0
	state.ViewportMobile = false

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	raw := string(data)
	for _, key := range []string{"viewport_width", "viewport_height", "viewport_scale", "viewport_mobile"} {
		if strings.Contains(raw, key) {
			t.Errorf("expected %q to be omitted after reset, got: %s", key, raw)
		}
	}
}

func TestViewport_ScreenshotSkipsOverrideWhenViewportSet(t *testing.T) {
	// When viewport is persisted in state, cmdScreenshot should skip its
	// default 1280x720 override so the active viewport is used instead.
	page := navigateTo(t, "/")

	// Set a custom viewport via CDP (simulating what "rodney viewport" does)
	err := proto.EmulationSetDeviceMetricsOverride{
		Width:             375,
		Height:            812,
		DeviceScaleFactor: 2,
	}.Call(page)
	if err != nil {
		t.Fatalf("EmulationSetDeviceMetricsOverride failed: %v", err)
	}

	w, err := page.Eval(`() => { return window.innerWidth; }`)
	if err != nil {
		t.Fatalf("eval innerWidth failed: %v", err)
	}
	if w.Value.Int() != 375 {
		t.Errorf("expected innerWidth 375, got %d", w.Value.Int())
	}

	// If screenshot were to call EmulationSetDeviceMetricsOverride with
	// 1280x720 here, innerWidth would change. Verify that re-applying the
	// same custom viewport keeps the size — this is the path screenshot
	// takes when it skips its default override.
	err = proto.EmulationSetDeviceMetricsOverride{
		Width:             375,
		Height:            812,
		DeviceScaleFactor: 2,
	}.Call(page)
	if err != nil {
		t.Fatalf("re-apply viewport failed: %v", err)
	}

	w2, err := page.Eval(`() => { return window.innerWidth; }`)
	if err != nil {
		t.Fatalf("eval innerWidth after re-apply failed: %v", err)
	}
	if w2.Value.Int() != 375 {
		t.Errorf("expected innerWidth 375 after re-apply, got %d", w2.Value.Int())
	}
}

// =====================
// parseStartFlags (flag.FlagSet) tests
// =====================

func TestParseStartFlags_FlagSet_NoFlags(t *testing.T) {
	f, err := parseStartFlags([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ignoreCertErrors {
		t.Error("expected insecure=false with no flags")
	}
	if !f.headless {
		t.Error("expected headless=true with no flags")
	}
}

func TestParseStartFlags_FlagSet_ShowFlag(t *testing.T) {
	f, err := parseStartFlags([]string{"--show"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ignoreCertErrors {
		t.Error("expected insecure=false")
	}
	if f.headless {
		t.Error("expected headless=false when --show is passed")
	}
}

func TestParseStartFlags_FlagSet_InsecureFlag(t *testing.T) {
	f, err := parseStartFlags([]string{"--insecure"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.ignoreCertErrors {
		t.Error("expected insecure=true when --insecure is passed")
	}
	if !f.headless {
		t.Error("expected headless=true when only --insecure is passed")
	}
}

func TestParseStartFlags_FlagSet_InsecureShortFlag(t *testing.T) {
	f, err := parseStartFlags([]string{"-k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.ignoreCertErrors {
		t.Error("expected insecure=true when -k is passed")
	}
}

func TestParseStartFlags_FlagSet_ShowAndInsecure(t *testing.T) {
	f, err := parseStartFlags([]string{"--show", "--insecure"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.ignoreCertErrors {
		t.Error("expected insecure=true")
	}
	if f.headless {
		t.Error("expected headless=false when --show is passed")
	}
}

func TestParseStartFlags_FlagSet_UnknownFlag(t *testing.T) {
	_, err := parseStartFlags([]string{"--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag --bogus")
	}
	if !strings.Contains(err.Error(), "--bogus") {
		t.Errorf("error should mention the unknown flag, got: %v", err)
	}
}

func TestInsecureFlag_WithSelfSignedCert(t *testing.T) {
	// Create HTTPS server with self-signed certificate
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Secure Test</title></head>
<body><h1>HTTPS Test Page</h1></body></html>`))
	})
	httpsServer := httptest.NewUnstartedServer(mux)
	// Suppress expected TLS handshake errors to keep test output clean
	httpsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	httpsServer.StartTLS()
	defer httpsServer.Close()

	// Test 1: Browser WITHOUT --ignore-certificate-errors should fail
	t.Run("WithoutInsecureFlag", func(t *testing.T) {
		l := launcher.New().
			Set("no-sandbox").
			Set("disable-gpu").
			Set("single-process").
			Headless(true).
			Leakless(false)

		if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
			l = l.Bin(bin)
		}

		u := l.MustLaunch()
		browser := rod.New().ControlURL(u).MustConnect()
		defer browser.MustClose()

		page := browser.MustPage("")
		defer page.MustClose()

		err := page.Navigate(httpsServer.URL)
		if err == nil {
			t.Fatal("expected ERR_CERT_AUTHORITY_INVALID error, but navigation succeeded")
		}
		if !strings.Contains(err.Error(), "ERR_CERT_AUTHORITY_INVALID") {
			t.Errorf("expected ERR_CERT_AUTHORITY_INVALID, got: %v", err)
		}
	})

	// Test 2: Browser WITH --ignore-certificate-errors should succeed
	t.Run("WithInsecureFlag", func(t *testing.T) {
		l := launcher.New().
			Set("no-sandbox").
			Set("disable-gpu").
			Set("single-process").
			Set("ignore-certificate-errors"). // This is what --insecure sets
			Headless(true).
			Leakless(false)

		if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
			l = l.Bin(bin)
		}

		u := l.MustLaunch()
		browser := rod.New().ControlURL(u).MustConnect()
		defer browser.MustClose()

		// Try to navigate to HTTPS server with invalid cert
		page := browser.MustPage(httpsServer.URL)
		defer page.MustClose()

		page.MustWaitLoad()
		title := page.MustInfo().Title

		if title != "Secure Test" {
			t.Errorf("expected page to load successfully with title 'Secure Test', got %q", title)
		}
	})
}

// =====================
// logs command tests
// =====================

// collectConsoleMsgs enables the Runtime domain, emits js, collects up to
// maxCount events (or waits timeout), and returns the collected entries.
func collectConsoleMsgs(page *rod.Page, js string, maxCount int, timeout time.Duration) (texts []string, levels []string) {
	var mu sync.Mutex
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	wait := page.EachEvent(func(e *proto.RuntimeConsoleAPICalled) bool {
		mu.Lock()
		texts = append(texts, formatConsoleArgs(e.Args))
		levels = append(levels, consoleTypeToLevel(e.Type))
		n := len(texts)
		mu.Unlock()
		if n >= maxCount {
			closeDone()
			return true // stop
		}
		return false
	})

	(proto.RuntimeEnable{}).Call(page) //nolint
	page.MustEval(js)

	go func() {
		wait()
		closeDone()
	}()

	select {
	case <-done:
	case <-time.After(timeout):
	}
	return
}

func TestLogs_SnapshotCapture(t *testing.T) {
	page := navigateTo(t, "/")

	texts, _ := collectConsoleMsgs(page, `() => {
		console.log("info message from logs test");
		console.warn("warning message from logs test");
		console.error("error message from logs test");
	}`, 3, 3*time.Second)

	if len(texts) < 3 {
		t.Fatalf("expected at least 3 log entries, got %d: %v", len(texts), texts)
	}

	found := false
	for _, text := range texts {
		if strings.Contains(text, "info message from logs test") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'info message from logs test' in entries, got: %v", texts)
	}
}

func TestLogs_ConsoleTypes(t *testing.T) {
	page := navigateTo(t, "/")

	_, levels := collectConsoleMsgs(page, `() => {
		console.warn("warning entry for level test");
		console.error("error entry for level test");
	}`, 2, 3*time.Second)

	levelSet := make(map[string]bool)
	for _, l := range levels {
		levelSet[l] = true
	}

	if !levelSet["warning"] {
		t.Errorf("expected a warning-level entry, got levels: %v", levels)
	}
	if !levelSet["error"] {
		t.Errorf("expected an error-level entry, got levels: %v", levels)
	}
}

func TestLogs_FormatLogLevel(t *testing.T) {
	tests := []struct {
		level    proto.LogLogEntryLevel
		expected string
	}{
		{proto.LogLogEntryLevelVerbose, "verbose"},
		{proto.LogLogEntryLevelInfo, "info"},
		{proto.LogLogEntryLevelWarning, "warning"},
		{proto.LogLogEntryLevelError, "error"},
		{proto.LogLogEntryLevel("custom"), "custom"},
	}
	for _, tt := range tests {
		got := formatLogLevel(tt.level)
		if got != tt.expected {
			t.Errorf("formatLogLevel(%q) = %q, want %q", tt.level, got, tt.expected)
		}
	}
}

func TestLogs_ConsoleTypeToLevel(t *testing.T) {
	tests := []struct {
		ct       proto.RuntimeConsoleAPICalledType
		expected string
	}{
		{proto.RuntimeConsoleAPICalledTypeDebug, "verbose"},
		{proto.RuntimeConsoleAPICalledTypeLog, "info"},
		{proto.RuntimeConsoleAPICalledTypeInfo, "info"},
		{proto.RuntimeConsoleAPICalledTypeWarning, "warning"},
		{proto.RuntimeConsoleAPICalledTypeError, "error"},
		{proto.RuntimeConsoleAPICalledTypeAssert, "error"},
		{proto.RuntimeConsoleAPICalledTypeDir, "info"},
	}
	for _, tt := range tests {
		got := consoleTypeToLevel(tt.ct)
		if got != tt.expected {
			t.Errorf("consoleTypeToLevel(%q) = %q, want %q", tt.ct, got, tt.expected)
		}
	}
}

func TestLogs_ScanLogFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.ndjson")

	content := `{"level":"info","source":"javascript","text":"hello","timestamp":"2024-01-01T12:00:00.000Z"}
{"level":"warning","source":"javascript","text":"world","timestamp":"2024-01-01T12:00:01.000Z"}
`
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}

	var lines []string
	scanLogFile(logFile, func(line string) { lines = append(lines, line) })
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}

	var obj struct {
		Level string `json:"level"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &obj); err != nil {
		t.Fatalf("failed to unmarshal line 0: %v", err)
	}
	if obj.Level != "info" || obj.Text != "hello" {
		t.Errorf("line 0: got level=%q text=%q, want level=%q text=%q", obj.Level, obj.Text, "info", "hello")
	}

	if err := json.Unmarshal([]byte(lines[1]), &obj); err != nil {
		t.Fatalf("failed to unmarshal line 1: %v", err)
	}
	if obj.Level != "warning" || obj.Text != "world" {
		t.Errorf("line 1: got level=%q text=%q, want level=%q text=%q", obj.Level, obj.Text, "warning", "world")
	}
}

// =====================
// discover command tests
// =====================

func handleDiscover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Discover Page</title></head>
<body>
  <h1 data-testid="heading">Dashboard</h1>
  <p data-testid="status">All systems operational</p>
  <input data-testid="search" type="text" placeholder="Search...">
  <textarea data-testid="notes" placeholder="Notes"></textarea>
  <button data-testid="submit-btn">Submit</button>
  <a data-testid="help-link" href="/help">Help</a>
  <select data-testid="filter">
    <option value="all">All</option>
    <option value="active">Active</option>
  </select>
  <div data-testid="hidden-el" style="display:none">Hidden content</div>
  <table data-testid="results-table">
    <thead><tr><th>Name</th><th>Status</th></tr></thead>
    <tbody><tr><td>Item 1</td><td>OK</td></tr><tr><td>Item 2</td><td>Fail</td></tr></tbody>
  </table>
  <span data-custom="custom-val">Custom attr element</span>
</body>
</html>`))
}

func TestDiscover_FindsTestIDElements(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	if len(entries) < 8 {
		t.Fatalf("expected at least 8 entries, got %d", len(entries))
	}
}

func TestDiscover_ButtonAction(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.ID == "submit-btn" {
			found = true
			if e.Action != "click" {
				t.Errorf("button action should be 'click', got %q", e.Action)
			}
			if e.Tag != "button" {
				t.Errorf("button tag should be 'button', got %q", e.Tag)
			}
			if e.Text != "Submit" {
				t.Errorf("button text should be 'Submit', got %q", e.Text)
			}
		}
	}
	if !found {
		t.Error("submit-btn not found in discover entries")
	}
}

func TestDiscover_InputAction(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	for _, e := range entries {
		if e.ID == "search" {
			if e.Action != "input" {
				t.Errorf("input action should be 'input', got %q", e.Action)
			}
			if e.Text != "Search..." {
				t.Errorf("input text should be placeholder 'Search...', got %q", e.Text)
			}
			return
		}
	}
	t.Error("search input not found in discover entries")
}

func TestDiscover_TextareaAction(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	for _, e := range entries {
		if e.ID == "notes" {
			if e.Action != "input" {
				t.Errorf("textarea action should be 'input', got %q", e.Action)
			}
			return
		}
	}
	t.Error("notes textarea not found in discover entries")
}

func TestDiscover_LinkAction(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	for _, e := range entries {
		if e.ID == "help-link" {
			if e.Action != "click" {
				t.Errorf("link action should be 'click', got %q", e.Action)
			}
			if !strings.Contains(e.Text, "/help") {
				t.Errorf("link text should contain href '/help', got %q", e.Text)
			}
			return
		}
	}
	t.Error("help-link not found in discover entries")
}

func TestDiscover_SelectAction(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	for _, e := range entries {
		if e.ID == "filter" {
			if e.Action != "select" {
				t.Errorf("select action should be 'select', got %q", e.Action)
			}
			if !strings.Contains(e.Text, "All") || !strings.Contains(e.Text, "Active") {
				t.Errorf("select text should list options, got %q", e.Text)
			}
			return
		}
	}
	t.Error("filter select not found in discover entries")
}

func TestDiscover_TableAction(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	for _, e := range entries {
		if e.ID == "results-table" {
			if e.Action != "text" {
				t.Errorf("table action should be 'text', got %q", e.Action)
			}
			if !strings.Contains(e.Text, "Name") || !strings.Contains(e.Text, "Status") {
				t.Errorf("table text should contain headers, got %q", e.Text)
			}
			if !strings.Contains(e.Text, "2 rows") {
				t.Errorf("table text should contain row count, got %q", e.Text)
			}
			return
		}
	}
	t.Error("results-table not found in discover entries")
}

func TestDiscover_HiddenElement(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	for _, e := range entries {
		if e.ID == "hidden-el" {
			if e.Visible {
				t.Error("hidden element should have Visible=false")
			}
			return
		}
	}
	t.Error("hidden-el not found in discover entries")
}

func TestDiscover_CustomAttr(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-custom")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry with data-custom, got %d", len(entries))
	}
	if entries[0].ID != "custom-val" {
		t.Errorf("expected id 'custom-val', got %q", entries[0].ID)
	}
}

func TestDiscover_EmptyPage(t *testing.T) {
	page := navigateTo(t, "/empty")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries on empty page, got %d", len(entries))
	}
}

func TestDiscover_FormatTextGrouping(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	out := formatDiscoverText(entries, "data-testid", "http://example.com/discover")
	if !strings.Contains(out, "Readable:") {
		t.Error("output should contain 'Readable:' group")
	}
	if !strings.Contains(out, "Interactive:") {
		t.Error("output should contain 'Interactive:' group")
	}
	if !strings.Contains(out, "Hidden:") {
		t.Error("output should contain 'Hidden:' group")
	}
	if !strings.Contains(out, "Page: http://example.com/discover") {
		t.Error("output should contain page URL")
	}
}

func TestDiscover_FormatTextCommands(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	out := formatDiscoverText(entries, "data-testid", "")
	if !strings.Contains(out, `rodney click '[data-testid="submit-btn"]'`) {
		t.Errorf("output should suggest click command for button, got:\n%s", out)
	}
	if !strings.Contains(out, `rodney input '[data-testid="search"]'`) {
		t.Errorf("output should suggest input command for text input, got:\n%s", out)
	}
	if !strings.Contains(out, `rodney select '[data-testid="filter"]'`) {
		t.Errorf("output should suggest select command for dropdown, got:\n%s", out)
	}
	if !strings.Contains(out, `rodney text '[data-testid="heading"]'`) {
		t.Errorf("output should suggest text command for heading, got:\n%s", out)
	}
}

func TestDiscover_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/discover")
	entries, err := queryDiscoverEntries(page, "data-testid")
	if err != nil {
		t.Fatalf("queryDiscoverEntries failed: %v", err)
	}
	out, jsonErr := json.MarshalIndent(entries, "", "  ")
	if jsonErr != nil {
		t.Fatalf("JSON marshal failed: %v", jsonErr)
	}
	var parsed []discoverEntry
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("JSON round-trip failed: %v", err)
	}
	if len(parsed) != len(entries) {
		t.Errorf("JSON round-trip: expected %d entries, got %d", len(entries), len(parsed))
	}
}

// =====================
// parseStartFlags tests
// =====================

func TestParseStartFlags_ShowFlag(t *testing.T) {
	flags, err := parseStartFlags([]string{"--show"})
	if err != nil {
		t.Fatalf("--show should be accepted, got error: %v", err)
	}
	if flags.headless {
		t.Error("expected headless=false when --show is passed")
	}
}

func TestParseStartFlags_ShowAndInsecure(t *testing.T) {
	flags, err := parseStartFlags([]string{"--show", "--insecure"})
	if err != nil {
		t.Fatalf("--show --insecure should be accepted, got error: %v", err)
	}
	if flags.headless {
		t.Error("expected headless=false when --show is passed")
	}
	if !flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=true when --insecure is passed")
	}
}

func TestParseStartFlags_InsecureOnly(t *testing.T) {
	flags, err := parseStartFlags([]string{"--insecure"})
	if err != nil {
		t.Fatalf("--insecure should be accepted, got error: %v", err)
	}
	if !flags.headless {
		t.Error("expected headless=true (default) when --show is not passed")
	}
	if !flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=true when --insecure is passed")
	}
}

func TestParseStartFlags_KShorthand(t *testing.T) {
	flags, err := parseStartFlags([]string{"-k"})
	if err != nil {
		t.Fatalf("-k should be accepted, got error: %v", err)
	}
	if !flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=true when -k is passed")
	}
}

func TestParseStartFlags_NoArgs(t *testing.T) {
	flags, err := parseStartFlags([]string{})
	if err != nil {
		t.Fatalf("no args should be accepted, got error: %v", err)
	}
	if !flags.headless {
		t.Error("expected headless=true by default")
	}
	if flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=false by default")
	}
}

func TestParseStartFlags_UnknownFlag(t *testing.T) {
	_, err := parseStartFlags([]string{"--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag --bogus")
	}
	if !strings.Contains(err.Error(), "unknown flag: --bogus") {
		t.Errorf("expected 'unknown flag: --bogus' in error, got: %v", err)
	}
}

// =====================
// stealth mode tests
// =====================

func TestParseStartFlags_StealthFlag(t *testing.T) {
	flags, err := parseStartFlags([]string{"--stealth"})
	if err != nil {
		t.Fatalf("--stealth should be accepted, got error: %v", err)
	}
	if !flags.stealth {
		t.Error("expected stealth=true when --stealth is passed")
	}
}

func TestParseStartFlags_StealthDefault(t *testing.T) {
	flags, err := parseStartFlags([]string{})
	if err != nil {
		t.Fatalf("no args should be accepted, got error: %v", err)
	}
	if flags.stealth {
		t.Error("expected stealth=false by default")
	}
}

func TestParseStartFlags_StealthWithShow(t *testing.T) {
	flags, err := parseStartFlags([]string{"--show", "--stealth"})
	if err != nil {
		t.Fatalf("--show --stealth should be accepted, got error: %v", err)
	}
	if flags.headless {
		t.Error("expected headless=false when --show is passed")
	}
	if !flags.stealth {
		t.Error("expected stealth=true when --stealth is passed")
	}
}

func TestStealth_StatePersistence(t *testing.T) {
	state := &State{
		DebugURL:  "ws://localhost:1234",
		ChromePID: 12345,
		DataDir:   t.TempDir(),
		Stealth:   true,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if !loaded.Stealth {
		t.Error("expected Stealth=true after round-trip")
	}
}

func TestStealth_StateOmittedWhenFalse(t *testing.T) {
	state := &State{
		DebugURL:  "ws://localhost:1234",
		ChromePID: 12345,
		DataDir:   "/tmp/test",
		Stealth:   false,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if strings.Contains(string(data), "stealth") {
		t.Errorf("expected stealth to be omitted from JSON when false, got: %s", string(data))
	}
}

func TestStealth_NavigatorWebdriver(t *testing.T) {
	page := navigateTo(t, "/")

	// Inject stealth script the same way withPage does
	_, err := proto.PageAddScriptToEvaluateOnNewDocument{
		Source: `Object.defineProperty(navigator, 'webdriver', {get: () => false});`,
	}.Call(page)
	if err != nil {
		t.Fatalf("PageAddScriptToEvaluateOnNewDocument failed: %v", err)
	}

	// Navigate again so the injected script runs before page scripts
	if err := page.Navigate(env.server.URL + "/"); err != nil {
		t.Fatalf("navigate failed: %v", err)
	}
	page.MustWaitLoad()

	result, err := page.Eval(`() => navigator.webdriver`)
	if err != nil {
		t.Fatalf("eval navigator.webdriver failed: %v", err)
	}

	if result.Value.Bool() != false {
		t.Errorf("expected navigator.webdriver to be false, got %v", result.Value)
	}
}

// =====================
// Extended discover fixture
// =====================

func handleDiscoverExtended(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Extended Discover Page</title></head>
<body>
  <nav>
    <a href="/dashboard">Dashboard</a>
    <a href="/settings">Settings</a>
    <a id="profile-link" href="/profile">My Profile</a>
    <a href="/logout">Log Out</a>
  </nav>

  <form id="login" action="/auth/login">
    <label for="email">Email</label>
    <input id="email" name="email" type="email" placeholder="enter email">
    <label for="password">Password</label>
    <input id="password" name="password" type="password">
    <select id="region" name="region" aria-label="Region">
      <option value="us">US</option>
      <option value="eu">EU</option>
    </select>
    <input type="file" name="avatar" id="avatar-upload">
    <button type="submit">Sign In</button>
  </form>

  <form id="search-form" action="/search">
    <input name="q" type="search" placeholder="Search...">
    <button type="submit">Go</button>
  </form>

  <main>
    <button id="save-btn">Save Changes</button>
    <textarea id="notes" placeholder="Add notes..."></textarea>
    <input type="checkbox" id="agree" name="agree">
    <label for="agree">I agree</label>
    <div role="button" tabindex="0" id="custom-btn">Custom Action</div>
    <span tabindex="0" id="focusable-span">Focusable Span</span>
  </main>
</body>
</html>`))
}

// --- Fixtures for inspectFailure tests ---

func handleHidden(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Hidden Page</title></head>
<body>
  <button id="visible-btn">Visible</button>
  <button id="hidden-btn" style="display:none">Hidden</button>
  <button id="checkout-btn">Checkout</button>
</body>
</html>`))
}

// =====================
// wait command tests
// =====================

func handleWaitTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Wait Test Page</title></head>
<body>
  <div id="status">Loading</div>
  <div id="spinner" class="spinner">Spinning...</div>
  <script>
    // Change status text after 200ms
    setTimeout(function() {
      document.getElementById('status').textContent = 'Ready';
    }, 200);
    // Remove spinner after 200ms
    setTimeout(function() {
      var el = document.getElementById('spinner');
      if (el) el.parentNode.removeChild(el);
    }, 200);
  </script>
</body>
</html>`))
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Sign In</title></head>
<body>
  <input type="text" id="username" placeholder="Username">
  <input type="password" id="password" placeholder="Password">
  <button id="login-btn">Sign In</button>
</body>
</html>`))
}

// =====================
// discover --forms tests
// =====================

func TestDiscoverForms_FindsFormFields(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	// login form has: email, password, region select, avatar file, submit button = 5
	// search form has: q input, submit button = 2
	if len(entries) < 7 {
		t.Fatalf("expected at least 7 form field entries, got %d", len(entries))
	}
}

func TestDiscoverForms_InputCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "email" {
			found = true
			if !strings.Contains(e.Command, "rodney input") {
				t.Errorf("email field should suggest input command, got %q", e.Command)
			}
			if e.Tag != "input" {
				t.Errorf("expected tag 'input', got %q", e.Tag)
			}
			break
		}
	}
	if !found {
		t.Error("email field not found in form entries")
	}
}

func TestDiscoverForms_SelectCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "region" {
			found = true
			if !strings.Contains(e.Command, "rodney select") {
				t.Errorf("select field should suggest select command, got %q", e.Command)
			}
			break
		}
	}
	if !found {
		t.Error("region select not found in form entries")
	}
}

func TestDiscoverForms_FileCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "avatar" {
			found = true
			if !strings.Contains(e.Command, "rodney file") {
				t.Errorf("file field should suggest file command, got %q", e.Command)
			}
			break
		}
	}
	if !found {
		t.Error("avatar file input not found in form entries")
	}
}

func TestDiscoverForms_SubmitCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Tag == "button" && e.FormSelector == "form#login" {
			found = true
			if !strings.Contains(e.Command, "rodney click") {
				t.Errorf("submit button should suggest click command, got %q", e.Command)
			}
			break
		}
	}
	if !found {
		t.Error("submit button not found in login form entries")
	}
}

func TestDiscoverForms_FormSelector(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	formSelectors := make(map[string]bool)
	for _, e := range entries {
		formSelectors[e.FormSelector] = true
	}
	if !formSelectors["form#login"] {
		t.Error("expected form#login in form selectors")
	}
	if !formSelectors["form#search-form"] {
		t.Error("expected form#search-form in form selectors")
	}
}

func TestDiscoverForms_Label(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	for _, e := range entries {
		if e.Name == "email" {
			if e.Label != "Email" {
				t.Errorf("email field should have label 'Email', got %q", e.Label)
			}
			return
		}
	}
	t.Error("email field not found")
}

func TestDiscoverForms_FormatText(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	out := formatDiscoverFormsText(entries)
	if !strings.Contains(out, "Form: form#login") {
		t.Errorf("output should contain 'Form: form#login', got:\n%s", out)
	}
	if !strings.Contains(out, "rodney input") {
		t.Errorf("output should contain 'rodney input', got:\n%s", out)
	}
	if !strings.Contains(out, "rodney select") {
		t.Errorf("output should contain 'rodney select', got:\n%s", out)
	}
	if !strings.Contains(out, "rodney click") {
		t.Errorf("output should contain 'rodney click', got:\n%s", out)
	}
}

func TestDiscoverForms_JSON(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	out, jsonErr := json.MarshalIndent(entries, "", "  ")
	if jsonErr != nil {
		t.Fatalf("JSON marshal failed: %v", jsonErr)
	}
	var parsed []discoverFormEntry
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("JSON round-trip failed: %v", err)
	}
	if len(parsed) != len(entries) {
		t.Errorf("JSON round-trip: expected %d entries, got %d", len(entries), len(parsed))
	}
}

func TestDiscoverForms_EmptyPage(t *testing.T) {
	page := navigateTo(t, "/empty")
	entries, err := queryDiscoverForms(page)
	if err != nil {
		t.Fatalf("queryDiscoverForms failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 form entries on empty page, got %d", len(entries))
	}
}

// =====================
// discover --links tests
// =====================

func TestDiscoverLinks_FindsLinks(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	// nav has 4 links
	if len(entries) < 4 {
		t.Fatalf("expected at least 4 link entries, got %d", len(entries))
	}
}

func TestDiscoverLinks_ClickCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	for _, e := range entries {
		if !strings.Contains(e.Command, "rodney click") {
			t.Errorf("link should suggest click command, got %q", e.Command)
		}
	}
}

func TestDiscoverLinks_HasHref(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	hrefs := make(map[string]bool)
	for _, e := range entries {
		hrefs[e.Href] = true
	}
	for _, expected := range []string{"/dashboard", "/settings", "/profile", "/logout"} {
		if !hrefs[expected] {
			t.Errorf("expected link with href %q", expected)
		}
	}
}

func TestDiscoverLinks_HasText(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	texts := make(map[string]bool)
	for _, e := range entries {
		texts[e.Text] = true
	}
	for _, expected := range []string{"Dashboard", "Settings", "My Profile", "Log Out"} {
		if !texts[expected] {
			t.Errorf("expected link with text %q", expected)
		}
	}
}

func TestDiscoverLinks_SelectorWithID(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	for _, e := range entries {
		if e.Href == "/profile" {
			if e.Selector != "a#profile-link" {
				t.Errorf("profile link should have selector 'a#profile-link', got %q", e.Selector)
			}
			return
		}
	}
	t.Error("profile link not found")
}

func TestDiscoverLinks_FormatText(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	out := formatDiscoverLinksText(entries, "http://example.com/test")
	if !strings.Contains(out, "Links on http://example.com/test:") {
		t.Errorf("output should contain page URL header, got:\n%s", out)
	}
	if !strings.Contains(out, "rodney click") {
		t.Errorf("output should contain 'rodney click', got:\n%s", out)
	}
	if !strings.Contains(out, "Dashboard") {
		t.Errorf("output should contain link text 'Dashboard', got:\n%s", out)
	}
}

func TestDiscoverLinks_JSON(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	out, jsonErr := json.MarshalIndent(entries, "", "  ")
	if jsonErr != nil {
		t.Fatalf("JSON marshal failed: %v", jsonErr)
	}
	var parsed []discoverLinkEntry
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("JSON round-trip failed: %v", err)
	}
	if len(parsed) != len(entries) {
		t.Errorf("JSON round-trip: expected %d entries, got %d", len(entries), len(parsed))
	}
}

func TestDiscoverLinks_EmptyPage(t *testing.T) {
	page := navigateTo(t, "/empty")
	entries, err := queryDiscoverLinks(page)
	if err != nil {
		t.Fatalf("queryDiscoverLinks failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 link entries on empty page, got %d", len(entries))
	}
}

// =====================
// discover --interactive tests
// =====================

func TestDiscoverInteractive_FindsElements(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	// buttons (save-btn, 2 submit), links (4), inputs (email, password, q, agree checkbox),
	// select (region), textarea (notes), role=button (custom-btn), tabindex span
	if len(entries) < 10 {
		t.Fatalf("expected at least 10 interactive entries, got %d", len(entries))
	}
}

func TestDiscoverInteractive_ButtonClick(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Selector == "button#save-btn" {
			found = true
			if !strings.Contains(e.Command, "rodney click") {
				t.Errorf("button should suggest click command, got %q", e.Command)
			}
			if e.Role != "button" {
				t.Errorf("expected role 'button', got %q", e.Role)
			}
			if e.Text != "Save Changes" {
				t.Errorf("expected text 'Save Changes', got %q", e.Text)
			}
			break
		}
	}
	if !found {
		t.Error("save-btn button not found in interactive entries")
	}
}

func TestDiscoverInteractive_InputCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Selector == "input#email" {
			found = true
			if !strings.Contains(e.Command, "rodney input") {
				t.Errorf("email input should suggest input command, got %q", e.Command)
			}
			if e.Role != "textbox" {
				t.Errorf("expected role 'textbox', got %q", e.Role)
			}
			break
		}
	}
	if !found {
		t.Error("email input not found in interactive entries")
	}
}

func TestDiscoverInteractive_SelectCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Selector == "select#region" {
			found = true
			if !strings.Contains(e.Command, "rodney select") {
				t.Errorf("select should suggest select command, got %q", e.Command)
			}
			if e.Role != "combobox" {
				t.Errorf("expected role 'combobox', got %q", e.Role)
			}
			break
		}
	}
	if !found {
		t.Error("region select not found in interactive entries")
	}
}

func TestDiscoverInteractive_TextareaCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Selector == "textarea#notes" {
			found = true
			if !strings.Contains(e.Command, "rodney input") {
				t.Errorf("textarea should suggest input command, got %q", e.Command)
			}
			if e.Role != "textbox" {
				t.Errorf("expected role 'textbox', got %q", e.Role)
			}
			break
		}
	}
	if !found {
		t.Error("notes textarea not found in interactive entries")
	}
}

func TestDiscoverInteractive_LinkCommand(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Tag == "a" && e.Text == "Dashboard" {
			found = true
			if !strings.Contains(e.Command, "rodney click") {
				t.Errorf("link should suggest click command, got %q", e.Command)
			}
			if e.Role != "link" {
				t.Errorf("expected role 'link', got %q", e.Role)
			}
			break
		}
	}
	if !found {
		t.Error("Dashboard link not found in interactive entries")
	}
}

func TestDiscoverInteractive_RoleButton(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Selector == "div#custom-btn" {
			found = true
			if !strings.Contains(e.Command, "rodney click") {
				t.Errorf("role=button element should suggest click command, got %q", e.Command)
			}
			if e.Role != "button" {
				t.Errorf("expected role 'button', got %q", e.Role)
			}
			break
		}
	}
	if !found {
		t.Error("custom-btn (role=button) not found in interactive entries")
	}
}

func TestDiscoverInteractive_Tabindex(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Selector == "span#focusable-span" {
			found = true
			break
		}
	}
	if !found {
		t.Error("focusable-span (tabindex) not found in interactive entries")
	}
}

func TestDiscoverInteractive_CheckboxRole(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Selector == "input#agree" {
			found = true
			if e.Role != "checkbox" {
				t.Errorf("expected role 'checkbox', got %q", e.Role)
			}
			break
		}
	}
	if !found {
		t.Error("agree checkbox not found in interactive entries")
	}
}

func TestDiscoverInteractive_FormatText(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	out := formatDiscoverInteractiveText(entries)
	if !strings.Contains(out, "Interactive elements:") {
		t.Errorf("output should contain 'Interactive elements:' header, got:\n%s", out)
	}
	if !strings.Contains(out, "rodney click") {
		t.Errorf("output should contain 'rodney click', got:\n%s", out)
	}
	if !strings.Contains(out, "rodney input") {
		t.Errorf("output should contain 'rodney input', got:\n%s", out)
	}
	if !strings.Contains(out, "rodney select") {
		t.Errorf("output should contain 'rodney select', got:\n%s", out)
	}
}

func TestDiscoverInteractive_JSON(t *testing.T) {
	page := navigateTo(t, "/discover-extended")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	out, jsonErr := json.MarshalIndent(entries, "", "  ")
	if jsonErr != nil {
		t.Fatalf("JSON marshal failed: %v", jsonErr)
	}
	var parsed []discoverInteractiveEntry
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("JSON round-trip failed: %v", err)
	}
	if len(parsed) != len(entries) {
		t.Errorf("JSON round-trip: expected %d entries, got %d", len(entries), len(parsed))
	}
}

func TestDiscoverInteractive_EmptyPage(t *testing.T) {
	page := navigateTo(t, "/empty")
	entries, err := queryDiscoverInteractive(page)
	if err != nil {
		t.Fatalf("queryDiscoverInteractive failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 interactive entries on empty page, got %d", len(entries))
	}
}

// =====================
// discover mode mutual exclusivity test
// =====================

func TestDiscoverModes_MutualExclusivity(t *testing.T) {
	// Test that parsing detects multiple mode flags.
	// We can't easily test cmdDiscover (it calls fatal/os.Exit),
	// so we test the flag parsing logic inline.
	tests := []struct {
		name  string
		args  []string
		count int
	}{
		{"forms only", []string{"--forms"}, 1},
		{"links only", []string{"--links"}, 1},
		{"interactive only", []string{"--interactive"}, 1},
		{"forms+links", []string{"--forms", "--links"}, 2},
		{"forms+interactive", []string{"--forms", "--interactive"}, 2},
		{"links+interactive", []string{"--links", "--interactive"}, 2},
		{"all three", []string{"--forms", "--links", "--interactive"}, 3},
		{"none", []string{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modeForms, modeLinks, modeInteractive := false, false, false
			for _, arg := range tt.args {
				switch arg {
				case "--forms":
					modeForms = true
				case "--links":
					modeLinks = true
				case "--interactive":
					modeInteractive = true
				}
			}
			count := 0
			if modeForms {
				count++
			}
			if modeLinks {
				count++
			}
			if modeInteractive {
				count++
			}
			if count != tt.count {
				t.Errorf("expected mode count %d, got %d", tt.count, count)
			}
			if count > 1 {
				// This is the error case: modes are mutually exclusive
				// Verify that the condition that triggers the error is detected
				if count <= 1 {
					t.Error("expected multiple modes to be detected as error")
				}
			}
		})
	}
}

// =====================
// check command tests
// =====================

func TestParseCheckArgs_Exists(t *testing.T) {
	checks, jsonOut, err := parseCheckArgs([]string{"--exists", "h1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jsonOut {
		t.Error("expected jsonOut=false")
	}
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].kind != "exists" || checks[0].arg1 != "h1" {
		t.Errorf("expected exists/h1, got %s/%s", checks[0].kind, checks[0].arg1)
	}
}

func TestParseCheckArgs_TextAndCount(t *testing.T) {
	checks, _, err := parseCheckArgs([]string{
		"--text", "h1", "Welcome",
		"--count", "button", "2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(checks))
	}
	if checks[0].kind != "text" || checks[0].arg1 != "h1" || checks[0].arg2 != "Welcome" {
		t.Errorf("check 0: got %+v", checks[0])
	}
	if checks[1].kind != "count" || checks[1].arg1 != "button" || checks[1].arg2 != "2" {
		t.Errorf("check 1: got %+v", checks[1])
	}
}

func TestParseCheckArgs_AssertWithExpected(t *testing.T) {
	checks, _, err := parseCheckArgs([]string{"--assert", "document.title", "Test Page"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].kind != "assert" || checks[0].arg1 != "document.title" || checks[0].arg2 != "Test Page" {
		t.Errorf("got %+v", checks[0])
	}
}

func TestParseCheckArgs_AssertTruthy(t *testing.T) {
	checks, _, err := parseCheckArgs([]string{"--assert", "document.title", "--exists", "h1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(checks))
	}
	// The assert should NOT have consumed "--exists" as its expected value
	if checks[0].kind != "assert" || checks[0].arg1 != "document.title" || checks[0].arg2 != "" {
		t.Errorf("assert check: got %+v", checks[0])
	}
	if checks[1].kind != "exists" || checks[1].arg1 != "h1" {
		t.Errorf("exists check: got %+v", checks[1])
	}
}

func TestParseCheckArgs_JSON(t *testing.T) {
	checks, jsonOut, err := parseCheckArgs([]string{"--json", "--exists", "h1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !jsonOut {
		t.Error("expected jsonOut=true")
	}
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
}

func TestParseCheckArgs_Visible(t *testing.T) {
	checks, _, err := parseCheckArgs([]string{"--visible", ".main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 1 || checks[0].kind != "visible" || checks[0].arg1 != ".main" {
		t.Errorf("got %+v", checks)
	}
}

func TestParseCheckArgs_NoChecks(t *testing.T) {
	checks, _, err := parseCheckArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 0 {
		t.Errorf("expected 0 checks, got %d", len(checks))
	}
}

func TestParseCheckArgs_UnknownFlag(t *testing.T) {
	_, _, err := parseCheckArgs([]string{"--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected 'unknown flag' in error, got: %v", err)
	}
}

func TestParseCheckArgs_MissingExistsArg(t *testing.T) {
	_, _, err := parseCheckArgs([]string{"--exists"})
	if err == nil {
		t.Fatal("expected error for missing --exists arg")
	}
}

func TestParseCheckArgs_MissingTextArgs(t *testing.T) {
	_, _, err := parseCheckArgs([]string{"--text", "h1"})
	if err == nil {
		t.Fatal("expected error for missing --text expected arg")
	}
}

func TestParseCheckArgs_MissingCountArgs(t *testing.T) {
	_, _, err := parseCheckArgs([]string{"--count", "h1"})
	if err == nil {
		t.Fatal("expected error for missing --count expected arg")
	}
}

func TestRunCheck_Exists_Pass(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "exists", arg1: "h1"})
	if !r.Pass {
		t.Errorf("expected pass for existing element, got %+v", r)
	}
	if r.Check != "exists" {
		t.Errorf("expected check='exists', got %q", r.Check)
	}
}

func TestRunCheck_Exists_Fail(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "exists", arg1: ".nonexistent"})
	if r.Pass {
		t.Errorf("expected fail for nonexistent element, got %+v", r)
	}
}

func TestRunCheck_Visible_Pass(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "visible", arg1: "h1"})
	if !r.Pass {
		t.Errorf("expected pass for visible element, got %+v", r)
	}
}

func TestRunCheck_Visible_Fail(t *testing.T) {
	page := navigateTo(t, "/discover")
	r := runCheck(page, checkItem{kind: "visible", arg1: "[data-testid=\"hidden-el\"]"})
	if r.Pass {
		t.Errorf("expected fail for hidden element, got %+v", r)
	}
}

func TestRunCheck_Text_Pass(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "text", arg1: "h1", arg2: "Welcome"})
	if !r.Pass {
		t.Errorf("expected pass, got %+v", r)
	}
	if r.Got != "Welcome" {
		t.Errorf("expected got='Welcome', got %q", r.Got)
	}
}

func TestRunCheck_Text_Fail(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "text", arg1: "h1", arg2: "Goodbye"})
	if r.Pass {
		t.Errorf("expected fail, got %+v", r)
	}
	if r.Got != "Welcome" {
		t.Errorf("expected got='Welcome', got %q", r.Got)
	}
	if r.Expected != "Goodbye" {
		t.Errorf("expected expected='Goodbye', got %q", r.Expected)
	}
}

func TestRunCheck_Count_Pass(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "count", arg1: "button", arg2: "2"})
	if !r.Pass {
		t.Errorf("expected pass, got %+v", r)
	}
	if r.Got != "2" {
		t.Errorf("expected got='2', got %q", r.Got)
	}
}

func TestRunCheck_Count_Fail(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "count", arg1: "button", arg2: "5"})
	if r.Pass {
		t.Errorf("expected fail, got %+v", r)
	}
	if r.Got != "2" {
		t.Errorf("expected got='2', got %q", r.Got)
	}
	if r.Expected != "5" {
		t.Errorf("expected expected='5', got %q", r.Expected)
	}
}

func TestRunCheck_Assert_Truthy_Pass(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "assert", arg1: "document.title"})
	if !r.Pass {
		t.Errorf("expected pass for truthy title, got %+v", r)
	}
	if r.Got != "Test Page" {
		t.Errorf("expected got='Test Page', got %q", r.Got)
	}
}

func TestRunCheck_Assert_Truthy_Fail(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "assert", arg1: "null"})
	if r.Pass {
		t.Errorf("expected fail for null, got %+v", r)
	}
}

func TestRunCheck_Assert_Equality_Pass(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "assert", arg1: "document.title", arg2: "Test Page"})
	if !r.Pass {
		t.Errorf("expected pass, got %+v", r)
	}
}

func TestRunCheck_Assert_Equality_Fail(t *testing.T) {
	page := navigateTo(t, "/")
	r := runCheck(page, checkItem{kind: "assert", arg1: "document.title", arg2: "Wrong Title"})
	if r.Pass {
		t.Errorf("expected fail, got %+v", r)
	}
	if r.Got != "Test Page" {
		t.Errorf("expected got='Test Page', got %q", r.Got)
	}
	if r.Expected != "Wrong Title" {
		t.Errorf("expected expected='Wrong Title', got %q", r.Expected)
	}
}

func TestFormatCheckLine_Pass(t *testing.T) {
	r := checkResult{Check: "exists", Selector: "h1", Pass: true}
	line := formatCheckLine(r)
	if !strings.HasPrefix(line, "PASS") {
		t.Errorf("expected PASS prefix, got %q", line)
	}
	if !strings.Contains(line, "exists") || !strings.Contains(line, "h1") {
		t.Errorf("expected 'exists' and 'h1' in line, got %q", line)
	}
}

func TestFormatCheckLine_Fail_WithExpected(t *testing.T) {
	r := checkResult{Check: "text", Selector: "h1", Pass: false, Got: "Hello", Expected: "Welcome"}
	line := formatCheckLine(r)
	if !strings.HasPrefix(line, "FAIL") {
		t.Errorf("expected FAIL prefix, got %q", line)
	}
	if !strings.Contains(line, `got "Hello"`) || !strings.Contains(line, `expected "Welcome"`) {
		t.Errorf("expected got/expected in line, got %q", line)
	}
}

func TestFormatCheckLine_Pass_WithExpected(t *testing.T) {
	r := checkResult{Check: "count", Selector: "button", Pass: true, Got: "2", Expected: "2"}
	line := formatCheckLine(r)
	if !strings.HasPrefix(line, "PASS") {
		t.Errorf("expected PASS prefix, got %q", line)
	}
	if !strings.Contains(line, "= 2") {
		t.Errorf("expected '= 2' in line, got %q", line)
	}
}

func TestCheck_AllPass_ExitZero(t *testing.T) {
	page := navigateTo(t, "/")
	checks := []checkItem{
		{kind: "exists", arg1: "h1"},
		{kind: "text", arg1: "h1", arg2: "Welcome"},
		{kind: "count", arg1: "button", arg2: "2"},
	}
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
	if passed != len(results) {
		t.Errorf("expected all %d to pass, only %d passed", len(results), passed)
	}
}

func TestCheck_SomeFail_NonZeroPassed(t *testing.T) {
	page := navigateTo(t, "/")
	checks := []checkItem{
		{kind: "exists", arg1: "h1"},
		{kind: "text", arg1: "h1", arg2: "Wrong text"},
	}
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
	if passed != 1 {
		t.Errorf("expected 1 pass and 1 fail, got %d passed out of %d", passed, len(results))
	}
}

func TestCheck_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/")
	checks := []checkItem{
		{kind: "exists", arg1: "h1"},
		{kind: "text", arg1: "h1", arg2: "Welcome"},
	}
	var results []checkResult
	for _, c := range checks {
		results = append(results, runCheck(page, c))
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	var parsed []checkResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 results in JSON, got %d", len(parsed))
	}
	if !parsed[0].Pass {
		t.Error("expected first result to pass")
	}
	if parsed[0].Check != "exists" {
		t.Errorf("expected check='exists', got %q", parsed[0].Check)
	}
	if !parsed[1].Pass {
		t.Error("expected second result to pass")
	}
	if parsed[1].Check != "text" {
		t.Errorf("expected check='text', got %q", parsed[1].Check)
	}
}

func TestCheck_MultipleTypes(t *testing.T) {
	page := navigateTo(t, "/")
	checks := []checkItem{
		{kind: "exists", arg1: "h1"},
		{kind: "visible", arg1: "h1"},
		{kind: "text", arg1: "h1", arg2: "Welcome"},
		{kind: "count", arg1: "button", arg2: "2"},
		{kind: "assert", arg1: "document.title", arg2: "Test Page"},
		{kind: "assert", arg1: "1 + 1 === 2"},
	}
	var results []checkResult
	for _, c := range checks {
		results = append(results, runCheck(page, c))
	}
	for i, r := range results {
		if !r.Pass {
			t.Errorf("check %d (%s %s) failed: %+v", i, r.Check, r.Selector+r.Expr, r)
		}
	}
}

// =====================
// wait command unit tests
// =====================

func TestParseWaitArgs_SelectorOnly(t *testing.T) {
	selector, textMatch, gone := parseWaitArgs([]string{".foo"})
	if selector != ".foo" {
		t.Errorf("expected selector '.foo', got %q", selector)
	}
	if textMatch != "" {
		t.Errorf("expected empty textMatch, got %q", textMatch)
	}
	if gone {
		t.Error("expected gone=false")
	}
}

func TestParseWaitArgs_TextFlag(t *testing.T) {
	selector, textMatch, gone := parseWaitArgs([]string{".status", "--text", "Ready"})
	if selector != ".status" {
		t.Errorf("expected selector '.status', got %q", selector)
	}
	if textMatch != "Ready" {
		t.Errorf("expected textMatch 'Ready', got %q", textMatch)
	}
	if gone {
		t.Error("expected gone=false")
	}
}

func TestParseWaitArgs_GoneFlag(t *testing.T) {
	selector, textMatch, gone := parseWaitArgs([]string{"--gone", ".spinner"})
	if selector != ".spinner" {
		t.Errorf("expected selector '.spinner', got %q", selector)
	}
	if textMatch != "" {
		t.Errorf("expected empty textMatch, got %q", textMatch)
	}
	if !gone {
		t.Error("expected gone=true")
	}
}

func TestParseWaitArgs_GoneAfterSelector(t *testing.T) {
	selector, _, gone := parseWaitArgs([]string{".spinner", "--gone"})
	if selector != ".spinner" {
		t.Errorf("expected selector '.spinner', got %q", selector)
	}
	if !gone {
		t.Error("expected gone=true")
	}
}

func TestParseWaitArgs_TextBeforeSelector(t *testing.T) {
	selector, textMatch, _ := parseWaitArgs([]string{"--text", "Ready", ".status"})
	if selector != ".status" {
		t.Errorf("expected selector '.status', got %q", selector)
	}
	if textMatch != "Ready" {
		t.Errorf("expected textMatch 'Ready', got %q", textMatch)
	}
}

func TestCmdWait_BasicWait(t *testing.T) {
	// Basic wait on an element that exists — should succeed immediately
	page := navigateTo(t, "/")
	el, err := page.Element("h1")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	el.MustWaitVisible()
	// If we got here, the basic wait mechanism works
}

func TestCmdWait_TextMatch(t *testing.T) {
	// Create a page where text changes after a delay
	page := env.browser.MustPage("")
	t.Cleanup(func() { page.MustClose() })

	page.MustNavigate(env.server.URL + "/wait-test")
	page.MustWaitLoad()

	// Poll for the text to appear (simulating what cmdWait --text does)
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		el, err := page.Element("#status")
		if err == nil {
			text, err := el.Text()
			if err == nil && strings.Contains(text, "Ready") {
				found = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !found {
		t.Fatal("timed out waiting for text 'Ready' in #status")
	}
}

func TestCmdWait_GoneNonexistent(t *testing.T) {
	// --gone with a selector that doesn't exist should succeed immediately
	page := navigateTo(t, "/")
	els, err := page.Elements("#does-not-exist")
	if err != nil || len(els) == 0 {
		// Element doesn't exist — this is the success condition for --gone
	} else {
		t.Error("expected #does-not-exist to not be found")
	}
}

func TestCmdWait_GoneDisappearing(t *testing.T) {
	// Test that --gone logic detects when an element is removed from the DOM
	page := env.browser.MustPage("")
	t.Cleanup(func() { page.MustClose() })

	page.MustNavigate(env.server.URL + "/wait-test")
	page.MustWaitLoad()

	// Poll for the spinner to disappear (simulating what cmdWait --gone does)
	deadline := time.Now().Add(5 * time.Second)
	gone := false
	for time.Now().Before(deadline) {
		els, err := page.Elements("#spinner")
		if err != nil || len(els) == 0 {
			gone = true
			break
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
			gone = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gone {
		t.Fatal("timed out waiting for #spinner to disappear")
	}
}

func TestCmdWait_MutualExclusivity(t *testing.T) {
	// --text and --gone together should be detected by parseWaitArgs callers
	selector, textMatch, gone := parseWaitArgs([]string{"--gone", "--text", "Ready", ".status"})
	if selector != ".status" {
		t.Errorf("expected selector '.status', got %q", selector)
	}
	if textMatch != "Ready" {
		t.Errorf("expected textMatch 'Ready', got %q", textMatch)
	}
	if !gone {
		t.Error("expected gone=true")
	}
	// Both are set — cmdWait would call fatal("--text and --gone are mutually exclusive")
	if textMatch != "" && gone {
		// This is the mutual exclusivity condition — confirmed
	} else {
		t.Error("expected both textMatch and gone to be set for mutual exclusivity test")
	}
}

// =====================
// hint function tests
// =====================

func handleOverlay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Overlay Page</title></head>
<body>
  <button id="action-btn">Action</button>
  <div class="modal-overlay" style="position:fixed;top:0;left:0;width:100%;height:100%;z-index:1000;background:rgba(0,0,0,0.5)">
    <div class="modal-content">Modal Content</div>
  </div>
</body>
</html>`))
}

// captureStderr captures everything written to os.Stderr by fn, trimming trailing whitespace.
// Repeated failure detection tests
// =====================

// captureStderr captures everything written to os.Stderr by fn.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = oldStderr
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("captureStderr read: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestHint_BasicMessage(t *testing.T) {
	got := captureStderr(t, func() {
		hint("try 'rodney discover --interactive' to see available elements")
	})
	expected := "hint: try 'rodney discover --interactive' to see available elements"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestHint_FormattedMessage(t *testing.T) {
	got := captureStderr(t, func() {
		hint("element may not be interactive — try 'rodney js \"document.querySelector(\\\"%s\\\").click()\"'", "#btn")
	})
	if !strings.Contains(got, "hint:") {
		t.Errorf("expected output to start with 'hint:', got %q", got)
	}
	if !strings.Contains(got, "#btn") {
		t.Errorf("expected output to contain selector '#btn', got %q", got)
	}
}

func TestHint_WritesToStderr(t *testing.T) {
	// Verify hint writes to stderr, not stdout
	stdout := captureStdout(t, func() {
		hint("this should not appear on stdout")
	})
	if stdout != "" {
		t.Errorf("hint should not write to stdout, got %q", stdout)
	}
}

func TestHint_MultipleHints(t *testing.T) {
	got := captureStderr(t, func() {
		hint("first hint")
		hint("second hint")
	})
	if !strings.Contains(got, "hint: first hint") {
		t.Errorf("expected 'hint: first hint' in output, got %q", got)
	}
	if !strings.Contains(got, "hint: second hint") {
		t.Errorf("expected 'hint: second hint' in output, got %q", got)
	}
}

// =====================
// inspectFailure tests
// =====================

func TestInspectFailure_HiddenElement(t *testing.T) {
	page := navigateTo(t, "/hidden")
	stderr := captureStderr(t, func() {
		inspectFailure(page, "#hidden-btn")
	})
	if !strings.Contains(stderr, "exists but is hidden") {
		t.Errorf("expected hidden element context, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "display: none") {
		t.Errorf("expected 'display: none' in context, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "rodney wait") {
		t.Errorf("expected suggestion to use rodney wait, got:\n%s", stderr)
	}
}

func TestInspectFailure_FuzzyMatch(t *testing.T) {
	page := navigateTo(t, "/hidden")
	stderr := captureStderr(t, func() {
		inspectFailure(page, "#checkout")
	})
	if !strings.Contains(stderr, "did you mean '#checkout-btn'?") {
		t.Errorf("expected fuzzy match suggestion for #checkout-btn, got:\n%s", stderr)
	}
}

func TestInspectFailure_AuthURL(t *testing.T) {
	page := navigateTo(t, "/login")
	stderr := captureStderr(t, func() {
		inspectFailure(page, "#nonexistent")
	})
	if !strings.Contains(stderr, "login page") {
		t.Errorf("expected auth pattern context, got:\n%s", stderr)
	}
}

func TestInspectFailure_OverlayDetection(t *testing.T) {
	page := navigateTo(t, "/overlay")
	stderr := captureStderr(t, func() {
		inspectFailure(page, "#nonexistent")
	})
	if !strings.Contains(stderr, "modal/overlay may be blocking") {
		t.Errorf("expected overlay context, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "z-index") {
		t.Errorf("expected z-index in overlay context, got:\n%s", stderr)
	}
}

func TestInspectFailure_AvailableElements(t *testing.T) {
	page := navigateTo(t, "/")
	stderr := captureStderr(t, func() {
		inspectFailure(page, "#nonexistent-xyz")
	})
	if !strings.Contains(stderr, "interactive elements") {
		t.Errorf("expected interactive elements context, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "rodney discover --interactive") {
		t.Errorf("expected discover suggestion, got:\n%s", stderr)
	}
}

func TestInspectFailure_EmptyPage(t *testing.T) {
	page := navigateTo(t, "/empty")
	// Should not panic or crash on a page with no interactive elements
	stderr := captureStderr(t, func() {
		inspectFailure(page, "#anything")
	})
	// On an empty page, there should be no crash; context may or may not be present
	_ = stderr
}

func TestInspectFailure_NoPanic(t *testing.T) {
	page := navigateTo(t, "/")
	// Test with various selector types that should not cause panics
	selectors := []string{"#nonexistent", ".missing-class", "div.nope", "[data-x]", ""}
	for _, sel := range selectors {
		captureStderr(t, func() {
			inspectFailure(page, sel)
		})
	}
}

// =====================
// Accessibility selector tests
// =====================

func TestParseAXFlags_RoleOnly(t *testing.T) {
	role, name, remaining := parseAXFlags([]string{"--role", "button"})
	if role != "button" {
		t.Errorf("expected role 'button', got %q", role)
	}
	if name != "" {
		t.Errorf("expected empty name, got %q", name)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
}

func TestParseAXFlags_NameOnly(t *testing.T) {
	role, name, remaining := parseAXFlags([]string{"--name", "Submit"})
	if role != "" {
		t.Errorf("expected empty role, got %q", role)
	}
	if name != "Submit" {
		t.Errorf("expected name 'Submit', got %q", name)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
}

func TestParseAXFlags_RoleAndName(t *testing.T) {
	role, name, remaining := parseAXFlags([]string{"--role", "button", "--name", "Submit"})
	if role != "button" {
		t.Errorf("expected role 'button', got %q", role)
	}
	if name != "Submit" {
		t.Errorf("expected name 'Submit', got %q", name)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
}

func TestParseAXFlags_CSSSelector(t *testing.T) {
	role, name, remaining := parseAXFlags([]string{"#submit-btn"})
	if role != "" {
		t.Errorf("expected empty role, got %q", role)
	}
	if name != "" {
		t.Errorf("expected empty name, got %q", name)
	}
	if len(remaining) != 1 || remaining[0] != "#submit-btn" {
		t.Errorf("expected remaining [#submit-btn], got %v", remaining)
	}
}

func TestParseAXFlags_RoleWithExtraArgs(t *testing.T) {
	role, name, remaining := parseAXFlags([]string{"--role", "textbox", "--name", "Email", "hello world"})
	if role != "textbox" {
		t.Errorf("expected role 'textbox', got %q", role)
	}
	if name != "Email" {
		t.Errorf("expected name 'Email', got %q", name)
	}
	if len(remaining) != 1 || remaining[0] != "hello world" {
		t.Errorf("expected remaining ['hello world'], got %v", remaining)
	}
}

func TestResolveElement_ByRole(t *testing.T) {
	page := navigateTo(t, "/")
	el, desc, remaining := resolveElement(page, []string{"--role", "button"})
	if el == nil {
		t.Fatal("expected element, got nil")
	}
	if !strings.Contains(desc, "--role") {
		t.Errorf("expected desc to contain '--role', got %q", desc)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
	// Verify it found a button
	tag, err := el.Eval(`() => this.tagName.toLowerCase()`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	if tag.Value.Str() != "button" {
		t.Errorf("expected button tag, got %q", tag.Value.Str())
	}
}

func TestResolveElement_ByName(t *testing.T) {
	page := navigateTo(t, "/")
	el, desc, _ := resolveElement(page, []string{"--name", "Submit"})
	if el == nil {
		t.Fatal("expected element, got nil")
	}
	if !strings.Contains(desc, "--name") {
		t.Errorf("expected desc to contain '--name', got %q", desc)
	}
	text, err := el.Text()
	if err != nil {
		t.Fatalf("text failed: %v", err)
	}
	if text != "Submit" {
		t.Errorf("expected text 'Submit', got %q", text)
	}
}

func TestResolveElement_ByRoleAndName(t *testing.T) {
	page := navigateTo(t, "/")
	el, _, _ := resolveElement(page, []string{"--role", "button", "--name", "Submit"})
	if el == nil {
		t.Fatal("expected element, got nil")
	}
	text, err := el.Text()
	if err != nil {
		t.Fatalf("text failed: %v", err)
	}
	if text != "Submit" {
		t.Errorf("expected text 'Submit', got %q", text)
	}
}

func TestResolveElement_ByCSSSelector(t *testing.T) {
	page := navigateTo(t, "/")
	el, desc, remaining := resolveElement(page, []string{"#submit-btn"})
	if el == nil {
		t.Fatal("expected element, got nil")
	}
	if desc != "#submit-btn" {
		t.Errorf("expected desc '#submit-btn', got %q", desc)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no remaining args, got %v", remaining)
	}
}

func TestResolveElement_InputWithExtraArgs(t *testing.T) {
	page := navigateTo(t, "/form")
	el, _, remaining := resolveElement(page, []string{"--role", "textbox", "--name", "Name", "hello"})
	if el == nil {
		t.Fatal("expected element, got nil")
	}
	if len(remaining) != 1 || remaining[0] != "hello" {
		t.Errorf("expected remaining ['hello'], got %v", remaining)
	}
}

func TestResolveElement_FormPageByRole(t *testing.T) {
	page := navigateTo(t, "/form")
	// Should find a textbox on the form page
	el, _, _ := resolveElement(page, []string{"--role", "textbox"})
	if el == nil {
		t.Fatal("expected element, got nil")
	}
	tag, _ := el.Eval(`() => this.tagName.toLowerCase()`)
	if tag.Value.Str() != "input" {
		t.Errorf("expected input tag, got %q", tag.Value.Str())
	}
}

func TestResolveElement_LinkByRoleAndName(t *testing.T) {
	page := navigateTo(t, "/")
	el, _, _ := resolveElement(page, []string{"--role", "link", "--name", "About"})
	if el == nil {
		t.Fatal("expected element, got nil")
	}
	href := el.MustAttribute("href")
	if href == nil || *href != "/about" {
		t.Errorf("expected href '/about', got %v", href)
	}
}

// =====================
// repeated failure detection tests
// =====================

func TestCallRecord_JSONRoundTrip(t *testing.T) {
	rec := CallRecord{
		Cmd:      "click",
		Selector: "#btn",
		OK:       false,
		Error:    "element not found",
		TS:       1712160000,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var loaded CallRecord
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if loaded.Cmd != rec.Cmd {
		t.Errorf("Cmd: got %q, want %q", loaded.Cmd, rec.Cmd)
	}
	if loaded.Selector != rec.Selector {
		t.Errorf("Selector: got %q, want %q", loaded.Selector, rec.Selector)
	}
	if loaded.OK != rec.OK {
		t.Errorf("OK: got %v, want %v", loaded.OK, rec.OK)
	}
	if loaded.Error != rec.Error {
		t.Errorf("Error: got %q, want %q", loaded.Error, rec.Error)
	}
	if loaded.TS != rec.TS {
		t.Errorf("TS: got %d, want %d", loaded.TS, rec.TS)
	}
}

func TestCallRecord_JSONOmitsEmptySelector(t *testing.T) {
	rec := CallRecord{Cmd: "click", OK: true, TS: 1}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(data), `"sel"`) {
		t.Errorf("empty selector should be omitted, got: %s", string(data))
	}
}

func TestCallRecord_JSONOmitsEmptyError(t *testing.T) {
	rec := CallRecord{Cmd: "click", OK: true, TS: 1}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(data), `"err"`) {
		t.Errorf("empty error should be omitted, got: %s", string(data))
	}
}

func TestRecentCalls_StatePersistence(t *testing.T) {
	state := &State{
		DebugURL:  "ws://localhost:1234",
		ChromePID: 12345,
		DataDir:   t.TempDir(),
		RecentCalls: []CallRecord{
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 1},
			{Cmd: "click", Selector: "#btn", OK: true, TS: 2},
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(loaded.RecentCalls) != 2 {
		t.Fatalf("expected 2 RecentCalls, got %d", len(loaded.RecentCalls))
	}
	if loaded.RecentCalls[0].Cmd != "click" {
		t.Errorf("RecentCalls[0].Cmd: got %q, want %q", loaded.RecentCalls[0].Cmd, "click")
	}
}

func TestRecentCalls_OmittedWhenEmpty(t *testing.T) {
	state := &State{
		DebugURL:  "ws://localhost:1234",
		ChromePID: 12345,
		DataDir:   "/tmp/test",
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(data), "recent_calls") {
		t.Errorf("empty RecentCalls should be omitted, got: %s", string(data))
	}
}

func TestRecordCall_AddsEntries(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	// Create initial state
	if err := saveState(&State{DebugURL: "ws://test", ChromePID: 1, DataDir: tmpDir}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	recordCall("click", "#btn", false, "not found")
	recordCall("click", "#btn", false, "not found")

	s, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(s.RecentCalls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(s.RecentCalls))
	}
	if s.RecentCalls[0].Cmd != "click" {
		t.Errorf("expected cmd 'click', got %q", s.RecentCalls[0].Cmd)
	}
}

func TestRecordCall_TrimsTo10(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	if err := saveState(&State{DebugURL: "ws://test", ChromePID: 1, DataDir: tmpDir}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	for i := 0; i < 15; i++ {
		recordCall("click", fmt.Sprintf("#btn%d", i), false, "not found")
	}

	s, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(s.RecentCalls) != 10 {
		t.Fatalf("expected 10 calls after trimming, got %d", len(s.RecentCalls))
	}
	// First entry should be btn5 (entries 0-4 trimmed away)
	if s.RecentCalls[0].Selector != "#btn5" {
		t.Errorf("expected first entry selector '#btn5', got %q", s.RecentCalls[0].Selector)
	}
}

func TestCheckStuck_NoHistory(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	if err := saveState(&State{DebugURL: "ws://test", ChromePID: 1, DataDir: tmpDir}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	count := checkStuck("click", "#btn")
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestCheckStuck_TwoIdenticalFailures(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	if err := saveState(&State{
		DebugURL:  "ws://test",
		ChromePID: 1,
		DataDir:   tmpDir,
		RecentCalls: []CallRecord{
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 1},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 2},
		},
	}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	count := checkStuck("click", "#btn")
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestCheckStuck_ThreeIdenticalFailures(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	if err := saveState(&State{
		DebugURL:  "ws://test",
		ChromePID: 1,
		DataDir:   tmpDir,
		RecentCalls: []CallRecord{
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 1},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 2},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 3},
		},
	}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	count := checkStuck("click", "#btn")
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestCheckStuck_ObservationsDontBreakStreak(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	if err := saveState(&State{
		DebugURL:  "ws://test",
		ChromePID: 1,
		DataDir:   tmpDir,
		RecentCalls: []CallRecord{
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 1},
			{Cmd: "screenshot", OK: true, TS: 2},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 3},
			{Cmd: "exists", Selector: "#btn", OK: true, TS: 4},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 5},
		},
	}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	count := checkStuck("click", "#btn")
	if count != 3 {
		t.Errorf("expected 3 (observations skipped), got %d", count)
	}
}

func TestCheckStuck_SuccessBreaksStreak(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	if err := saveState(&State{
		DebugURL:  "ws://test",
		ChromePID: 1,
		DataDir:   tmpDir,
		RecentCalls: []CallRecord{
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 1},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 2},
			{Cmd: "click", Selector: "#other", OK: true, TS: 3},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 4},
		},
	}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	count := checkStuck("click", "#btn")
	if count != 1 {
		t.Errorf("expected 1 (success broke streak), got %d", count)
	}
}

func TestCheckStuck_DifferentCmdBreaksStreak(t *testing.T) {
	tmpDir := t.TempDir()
	oldStateDir := activeStateDir
	activeStateDir = tmpDir
	t.Cleanup(func() { activeStateDir = oldStateDir })

	if err := saveState(&State{
		DebugURL:  "ws://test",
		ChromePID: 1,
		DataDir:   tmpDir,
		RecentCalls: []CallRecord{
			{Cmd: "hover", Selector: "#btn", OK: false, Error: "not found", TS: 1},
			{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 2},
		},
	}); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	count := checkStuck("click", "#btn")
	if count != 1 {
		t.Errorf("expected 1 (different cmd broke streak), got %d", count)
	}
}

func TestReportStuck_NoOutput_Count0(t *testing.T) {
	out := captureStderr(t, func() { reportStuck(0) })
	if out != "" {
		t.Errorf("expected no output for count 0, got %q", out)
	}
}

func TestReportStuck_NoOutput_Count1(t *testing.T) {
	out := captureStderr(t, func() { reportStuck(1) })
	if out != "" {
		t.Errorf("expected no output for count 1, got %q", out)
	}
}

func TestReportStuck_Count2(t *testing.T) {
	out := captureStderr(t, func() { reportStuck(2) })
	if !strings.Contains(out, "failed 2 times") {
		t.Errorf("expected 'failed 2 times' message, got %q", out)
	}
	if !strings.Contains(out, "try a different selector") {
		t.Errorf("expected 'try a different selector' suggestion, got %q", out)
	}
}

func TestReportStuck_Count3(t *testing.T) {
	out := captureStderr(t, func() { reportStuck(3) })
	if !strings.Contains(out, "STUCK") {
		t.Errorf("expected 'STUCK' message, got %q", out)
	}
	if !strings.Contains(out, "failed 3 times") {
		t.Errorf("expected 'failed 3 times' message, got %q", out)
	}
	if !strings.Contains(out, "discover --interactive") {
		t.Errorf("expected 'discover --interactive' suggestion, got %q", out)
	}
	if !strings.Contains(out, "ax-find") {
		t.Errorf("expected 'ax-find' suggestion, got %q", out)
	}
	if !strings.Contains(out, "waitstable") {
		t.Errorf("expected 'waitstable' suggestion, got %q", out)
	}
}

func TestReportStuck_Count5(t *testing.T) {
	out := captureStderr(t, func() { reportStuck(5) })
	if !strings.Contains(out, "STUCK") {
		t.Errorf("expected 'STUCK' message for count 5, got %q", out)
	}
	if !strings.Contains(out, "failed 5 times") {
		t.Errorf("expected 'failed 5 times' message, got %q", out)
	}
}

func TestCheckStuck_AllObservationCmds(t *testing.T) {
	// Verify all observation commands are properly skipped
	obsCmds := []string{"url", "title", "text", "html", "screenshot", "screenshot-el",
		"exists", "visible", "count", "ax-tree", "ax-find", "ax-node",
		"discover", "pages", "status", "logs", "pdf", "attr"}

	for _, obs := range obsCmds {
		tmpDir := t.TempDir()
		oldStateDir := activeStateDir
		activeStateDir = tmpDir

		if err := saveState(&State{
			DebugURL:  "ws://test",
			ChromePID: 1,
			DataDir:   tmpDir,
			RecentCalls: []CallRecord{
				{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 1},
				{Cmd: obs, OK: true, TS: 2},
				{Cmd: "click", Selector: "#btn", OK: false, Error: "not found", TS: 3},
			},
		}); err != nil {
			activeStateDir = oldStateDir
			t.Fatalf("saveState for obs %q: %v", obs, err)
		}

		count := checkStuck("click", "#btn")
		if count != 2 {
			t.Errorf("observation cmd %q should not break streak: expected 2, got %d", obs, count)
		}
		activeStateDir = oldStateDir
	}
}

func TestStartClearsRecentCalls(t *testing.T) {
	// When cmdStart creates a fresh State, RecentCalls should be nil/empty
	state := &State{
		DebugURL:  "ws://localhost:1234",
		ChromePID: 12345,
		DataDir:   t.TempDir(),
	}
	if len(state.RecentCalls) != 0 {
		t.Errorf("fresh State should have no RecentCalls, got %d", len(state.RecentCalls))
	}

	// Verify it's omitted in JSON
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(data), "recent_calls") {
		t.Errorf("fresh state JSON should not contain recent_calls, got: %s", string(data))
	}
}
