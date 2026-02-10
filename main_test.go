package main

import (
	"encoding/json"
	"fmt"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	mux.HandleFunc("/animated", handleAnimated)
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

func handleAnimated(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><style>
.box { width: 50px; height: 50px; background: red; animation: move 1s linear infinite; }
@keyframes move { 0% { margin-left: 0; } 100% { margin-left: 200px; } }
</style></head>
<body><div class="box"></div>
<div id="c">0</div>
<script>let n=0; setInterval(()=>{document.getElementById('c').textContent=++n;},100);</script>
</body></html>`))
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
// Video recording tests
// =====================

// testStateDir overrides the state dir for test isolation
func withTestStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origStateDir := stateDirOverride
	stateDirOverride = dir
	t.Cleanup(func() { stateDirOverride = origStateDir })
	return dir
}

func TestStartVideo_SetsStateFlag(t *testing.T) {
	dir := withTestStateDir(t)

	// Write a fake state file (simulating a running browser)
	s := &State{DebugURL: "ws://fake", ChromePID: 99999}
	if err := saveState(s); err != nil {
		t.Fatal(err)
	}

	// Call startVideo
	if err := startVideo(); err != nil {
		t.Fatalf("startVideo failed: %v", err)
	}

	// State should now have VideoRecording=true and a VideoDir
	s2, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !s2.VideoRecording {
		t.Error("expected VideoRecording=true")
	}
	if s2.VideoDir == "" {
		t.Error("expected VideoDir to be set")
	}

	// VideoDir should exist on disk
	info, err := os.Stat(s2.VideoDir)
	if err != nil {
		t.Fatalf("VideoDir does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("VideoDir is not a directory")
	}

	// VideoDir should be under our state dir
	if !strings.HasPrefix(s2.VideoDir, dir) {
		t.Errorf("VideoDir %q should be under state dir %q", s2.VideoDir, dir)
	}
}

func TestStartVideo_ErrorsIfAlreadyRecording(t *testing.T) {
	withTestStateDir(t)

	s := &State{DebugURL: "ws://fake", ChromePID: 99999, VideoRecording: true, VideoDir: "/tmp/fake"}
	saveState(s)

	err := startVideo()
	if err == nil {
		t.Error("expected error when already recording")
	}
}

func TestVideoCapture_RecordsFramesDuringPageUse(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	// Navigate to a page with animation (generates continuous frames)
	page := navigateTo(t, "/animated")

	// Start video capture on this page
	stop := startVideoCapture(page, framesDir)

	// Give screencast time to emit some frames
	time.Sleep(1 * time.Second)

	// Stop capture and get frame count
	n := stop()

	if n == 0 {
		t.Fatal("expected at least 1 frame captured, got 0")
	}

	// Check frames exist on disk
	entries, err := os.ReadDir(framesDir)
	if err != nil {
		t.Fatal(err)
	}

	jpegCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jpeg" {
			jpegCount++
		}
	}
	if jpegCount == 0 {
		t.Fatal("no JPEG files found in frames dir")
	}
	if jpegCount != n {
		t.Errorf("frame count mismatch: stop() returned %d but found %d files", n, jpegCount)
	}

	// Check metadata file exists
	metaPath := filepath.Join(framesDir, "meta.jsonl")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("meta.jsonl not found: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(metaData)), "\n")
	if len(lines) != n {
		t.Errorf("meta.jsonl has %d lines, expected %d", len(lines), n)
	}

	// First frame should be valid JPEG
	firstFrame, err := os.ReadFile(filepath.Join(framesDir, "frame_000000.jpeg"))
	if err != nil {
		t.Fatalf("could not read first frame: %v", err)
	}
	if len(firstFrame) < 3 || firstFrame[0] != 0xFF || firstFrame[1] != 0xD8 {
		t.Error("first frame is not a valid JPEG (missing magic bytes)")
	}
}

func TestVideoCapture_AccumulatesAcrossCalls(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	page := navigateTo(t, "/animated")

	// First capture session
	stop1 := startVideoCapture(page, framesDir)
	time.Sleep(500 * time.Millisecond)
	n1 := stop1()

	// Second capture session (should continue numbering)
	stop2 := startVideoCapture(page, framesDir)
	time.Sleep(500 * time.Millisecond)
	n2 := stop2()

	if n1 == 0 || n2 == 0 {
		t.Fatalf("expected frames from both sessions, got %d and %d", n1, n2)
	}

	// Total files should be n1 + n2
	entries, _ := os.ReadDir(framesDir)
	jpegCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jpeg" {
			jpegCount++
		}
	}
	if jpegCount != n1+n2 {
		t.Errorf("expected %d total frames, got %d", n1+n2, jpegCount)
	}
}

func TestStopVideo_ClearsStateAndReturnsFrameCount(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")
	os.MkdirAll(framesDir, 0755)

	// Simulate some captured frames + metadata
	for i := 0; i < 5; i++ {
		// Write minimal JPEG files (just magic bytes for test)
		os.WriteFile(filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpeg", i)), []byte{0xFF, 0xD8, 0xFF}, 0644)
	}
	metaLines := ""
	for i := 0; i < 5; i++ {
		metaLines += fmt.Sprintf(`{"idx":%d,"ts":%f}`+"\n", i, float64(1000+i)*0.016)
	}
	os.WriteFile(filepath.Join(framesDir, "meta.jsonl"), []byte(metaLines), 0644)

	s := &State{DebugURL: "ws://fake", ChromePID: 99999, VideoRecording: true, VideoDir: framesDir}
	saveState(s)

	result, err := stopVideo("")
	if err != nil {
		t.Fatalf("stopVideo failed: %v", err)
	}

	if result.FrameCount != 5 {
		t.Errorf("expected 5 frames, got %d", result.FrameCount)
	}

	// State should be cleared
	s2, err := loadState()
	if err != nil {
		t.Fatal(err)
	}
	if s2.VideoRecording {
		t.Error("expected VideoRecording=false after stop")
	}
	if s2.VideoDir != "" {
		t.Error("expected VideoDir to be cleared after stop")
	}
}

func TestStopVideo_ErrorsIfNotRecording(t *testing.T) {
	withTestStateDir(t)

	s := &State{DebugURL: "ws://fake", ChromePID: 99999}
	saveState(s)

	_, err := stopVideo("")
	if err == nil {
		t.Error("expected error when not recording")
	}
}

func TestAssembleVideo_ProducesMP4(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	// Capture real frames from an animated page
	page := navigateTo(t, "/animated")
	stop := startVideoCapture(page, framesDir)
	time.Sleep(1 * time.Second)
	n := stop()

	if n < 2 {
		t.Fatalf("need at least 2 frames for video, got %d", n)
	}

	outputFile := filepath.Join(dir, "test-output.mp4")
	result, err := assembleVideo(framesDir, outputFile)
	if err != nil {
		t.Fatalf("assembleVideo failed: %v", err)
	}

	info, err := os.Stat(result)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output file is empty")
	}

	// Verify it's a real MP4 (starts with ftyp box or moov)
	header := make([]byte, 12)
	f, _ := os.Open(result)
	f.Read(header)
	f.Close()
	// MP4 files have "ftyp" at offset 4
	if string(header[4:8]) != "ftyp" {
		t.Errorf("output doesn't look like MP4, header: %x", header[:12])
	}
}

func TestVideoCapture_WithPageIntegration(t *testing.T) {
	// Test that withPageVideoCapture starts/stops screencast when recording is on
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")
	os.MkdirAll(framesDir, 0755)

	page := navigateTo(t, "/animated")

	// Simulate: recording is active
	s := &State{
		DebugURL:       "ws://fake",
		ChromePID:      99999,
		VideoRecording: true,
		VideoDir:       framesDir,
	}
	saveState(s)

	// Call the integration hook — same thing withPage() calls
	cleanup := maybeStartVideoCapture(page)

	time.Sleep(1 * time.Second)

	// Call cleanup (same as what runs via defer in main)
	cleanup()

	// Frames should have been captured
	frameCount := countFrames(framesDir)
	if frameCount == 0 {
		t.Fatal("expected frames to be captured via maybeStartVideoCapture")
	}
}

func TestStopVideo_ProducesMP4WhenFfmpegAvailable(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	// Capture real frames from animated page
	page := navigateTo(t, "/animated")
	stop := startVideoCapture(page, framesDir)
	time.Sleep(1 * time.Second)
	stop()

	// Set up state as if start-video had run
	s := &State{DebugURL: "ws://fake", ChromePID: 99999, VideoRecording: true, VideoDir: framesDir}
	saveState(s)

	outputFile := filepath.Join(dir, "result.mp4")
	result, err := stopVideo(outputFile)
	if err != nil {
		t.Fatalf("stopVideo failed: %v", err)
	}

	if result.OutputFile == "" {
		t.Error("expected OutputFile to be set")
	}
	if result.FrameCount == 0 {
		t.Error("expected non-zero frame count")
	}

	// MP4 file should exist
	info, err := os.Stat(result.OutputFile)
	if err != nil {
		t.Fatalf("MP4 file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("MP4 file is empty")
	}

	// Frames dir should be cleaned up
	if _, err := os.Stat(framesDir); !os.IsNotExist(err) {
		t.Error("expected frames dir to be removed after stop-video")
	}
}

// =====================
// GIF recording tests
// =====================

func TestAssembleGIF_ProducesValidGIF(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	// Capture real frames from animated page
	page := navigateTo(t, "/animated")
	stop := startVideoCapture(page, framesDir)
	time.Sleep(1 * time.Second)
	n := stop()

	if n < 2 {
		t.Fatalf("need at least 2 frames, got %d", n)
	}

	outputFile := filepath.Join(dir, "test-output.gif")
	result, err := assembleGIF(framesDir, outputFile)
	if err != nil {
		t.Fatalf("assembleGIF failed: %v", err)
	}

	// File should exist and be non-empty
	info, err := os.Stat(result.OutputFile)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("output file is empty")
	}

	// Should be a valid GIF (starts with GIF89a or GIF87a)
	header := make([]byte, 6)
	f, _ := os.Open(result.OutputFile)
	f.Read(header)
	f.Close()
	if string(header[:3]) != "GIF" {
		t.Errorf("not a GIF file, header: %q", string(header))
	}

	// Should have captured some frames
	if result.InputFrames == 0 {
		t.Error("expected non-zero InputFrames")
	}
	if result.UniqueFrames == 0 {
		t.Error("expected non-zero UniqueFrames")
	}
}

func TestAssembleGIF_DeduplicatesIdenticalFrames(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")
	os.MkdirAll(framesDir, 0755)

	// Write identical JPEG frames to simulate duplicate screencast output
	// Use a real JPEG from a page capture for realistic data
	page := navigateTo(t, "/")
	stop := startVideoCapture(page, framesDir)
	time.Sleep(200 * time.Millisecond)
	stop()

	// Read whatever frame we got and duplicate it
	firstFrame, err := os.ReadFile(filepath.Join(framesDir, "frame_000000.jpeg"))
	if err != nil {
		t.Fatalf("no frame captured: %v", err)
	}

	// Clear and write 10 identical frames + metadata
	os.RemoveAll(framesDir)
	os.MkdirAll(framesDir, 0755)
	metaFile, _ := os.Create(filepath.Join(framesDir, "meta.jsonl"))
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(framesDir, fmt.Sprintf("frame_%06d.jpeg", i)), firstFrame, 0644)
		fmt.Fprintf(metaFile, `{"idx":%d,"ts":%.6f}`+"\n", i, float64(1000)+float64(i)*0.033)
	}
	metaFile.Close()

	outputFile := filepath.Join(dir, "dedup-test.gif")
	result, err := assembleGIF(framesDir, outputFile)
	if err != nil {
		t.Fatalf("assembleGIF failed: %v", err)
	}

	t.Logf("Input: %d frames, Unique: %d frames", result.InputFrames, result.UniqueFrames)

	// All 10 frames are identical, so should deduplicate to 1 unique frame
	if result.UniqueFrames != 1 {
		t.Errorf("expected 1 unique frame from 10 identical inputs, got %d", result.UniqueFrames)
	}
	if result.InputFrames != 10 {
		t.Errorf("expected 10 input frames, got %d", result.InputFrames)
	}
}

func TestAssembleGIF_DecodableWithStdlib(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	page := navigateTo(t, "/animated")
	stop := startVideoCapture(page, framesDir)
	time.Sleep(1 * time.Second)
	stop()

	outputFile := filepath.Join(dir, "decode-test.gif")
	_, err := assembleGIF(framesDir, outputFile)
	if err != nil {
		t.Fatalf("assembleGIF failed: %v", err)
	}

	// Decode the GIF with stdlib to verify it's valid
	f, err := os.Open(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatalf("gif.DecodeAll failed: %v", err)
	}

	if len(g.Image) == 0 {
		t.Error("GIF has no frames")
	}
	if len(g.Image) != len(g.Delay) {
		t.Errorf("frame count (%d) != delay count (%d)", len(g.Image), len(g.Delay))
	}
	// LoopCount 0 means loop forever
	if g.LoopCount != 0 {
		t.Errorf("expected LoopCount 0 (loop forever), got %d", g.LoopCount)
	}

	// Check dimensions match our viewport
	bounds := g.Image[0].Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		t.Errorf("first frame has zero dimensions: %v", bounds)
	}

	// All delays should be positive
	for i, d := range g.Delay {
		if d <= 0 {
			t.Errorf("frame %d has non-positive delay: %d", i, d)
		}
	}
}

func TestSleep_CapturesVideoFramesWhenRecording(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")
	os.MkdirAll(framesDir, 0755)

	// Navigate to an animated page (simulates a page being open)
	page := navigateTo(t, "/animated")

	// Set up state: recording is active
	s := &State{
		DebugURL:       "ws://fake",
		ChromePID:      99999,
		VideoRecording: true,
		VideoDir:       framesDir,
	}
	saveState(s)

	// maybeStartVideoCapture should start screencast when recording is on
	cleanup := maybeStartVideoCapture(page)
	time.Sleep(2 * time.Second)
	cleanup()

	// Frames should have been captured during the sleep
	frameCount := countFrames(framesDir)
	if frameCount == 0 {
		t.Fatal("expected frames to be captured during sleep while recording, got 0")
	}
	t.Logf("captured %d frames during 2s sleep", frameCount)
}

func TestStopVideo_MP4FallsBackToGIFWithoutFfmpeg(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	page := navigateTo(t, "/animated")
	stop := startVideoCapture(page, framesDir)
	time.Sleep(1 * time.Second)
	stop()

	s := &State{DebugURL: "ws://fake", ChromePID: 99999, VideoRecording: true, VideoDir: framesDir}
	saveState(s)

	// Request .mp4 but with a bogus PATH so ffmpeg won't be found
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", "/nonexistent")
	defer os.Setenv("PATH", origPath)

	outputFile := filepath.Join(dir, "result.mp4")
	result, err := stopVideo(outputFile)
	if err != nil {
		t.Fatalf("stopVideo failed: %v", err)
	}

	// Should have fallen back to GIF
	if result.OutputFile == "" {
		t.Fatal("expected OutputFile to be set (GIF fallback)")
	}
	if !strings.HasSuffix(result.OutputFile, ".gif") {
		t.Errorf("expected .gif fallback, got %q", result.OutputFile)
	}

	// Verify it's actually a valid GIF
	header := make([]byte, 6)
	f, _ := os.Open(result.OutputFile)
	f.Read(header)
	f.Close()
	if string(header[:3]) != "GIF" {
		t.Errorf("fallback file is not a GIF, header: %q", string(header))
	}
}

func TestStopVideo_DetectsGIFExtension(t *testing.T) {
	dir := withTestStateDir(t)
	framesDir := filepath.Join(dir, "video-frames")

	page := navigateTo(t, "/animated")
	stop := startVideoCapture(page, framesDir)
	time.Sleep(1 * time.Second)
	stop()

	s := &State{DebugURL: "ws://fake", ChromePID: 99999, VideoRecording: true, VideoDir: framesDir}
	saveState(s)

	outputFile := filepath.Join(dir, "result.gif")
	result, err := stopVideo(outputFile)
	if err != nil {
		t.Fatalf("stopVideo failed: %v", err)
	}

	if result.OutputFile == "" {
		t.Fatal("expected OutputFile to be set")
	}

	// Should be a valid GIF
	header := make([]byte, 6)
	f, _ := os.Open(result.OutputFile)
	f.Read(header)
	f.Close()
	if string(header[:3]) != "GIF" {
		t.Errorf("expected GIF file for .gif extension, got header: %q", string(header))
	}
}
