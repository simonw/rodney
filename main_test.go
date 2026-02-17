package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// testEnv holds a shared browser and test HTTP server for all tests.
type testEnv struct {
	browser *rod.Browser
	server  *httptest.Server
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
	server := httptest.NewServer(mux)

	env = &testEnv{browser: browser, server: server}

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
