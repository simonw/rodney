package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// --- fixtures ---

const testExtensionManifest = `{
  "manifest_version": 3,
  "name": "Rodney Test Extension",
  "version": "1.2.3",
  "content_scripts": [{"matches": ["<all_urls>"], "js": ["content.js"]}]
}`

// The content script renames the page so a test can prove it ran.
const testExtensionContentScript = `document.title = "extension-was-here";`

// writeTestExtension creates an unpacked extension directory and returns its path.
func writeTestExtension(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", testExtensionManifest)
	write("content.js", testExtensionContentScript)
	return dir
}

// zipBytes builds an in-memory zip archive from name -> contents.
func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// crx3Bytes wraps zip payload in a CRX3 container with a dummy header.
func crx3Bytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	header := []byte("dummy-crx3-header")
	var buf bytes.Buffer
	buf.WriteString("Cr24")
	binary.Write(&buf, binary.LittleEndian, uint32(3))
	binary.Write(&buf, binary.LittleEndian, uint32(len(header)))
	buf.Write(header)
	buf.Write(payload)
	return buf.Bytes()
}

// crx2Bytes wraps zip payload in a CRX2 container with dummy key and signature.
func crx2Bytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	key, sig := []byte("dummy-key"), []byte("dummy-signature")
	var buf bytes.Buffer
	buf.WriteString("Cr24")
	binary.Write(&buf, binary.LittleEndian, uint32(2))
	binary.Write(&buf, binary.LittleEndian, uint32(len(key)))
	binary.Write(&buf, binary.LittleEndian, uint32(len(sig)))
	buf.Write(key)
	buf.Write(sig)
	buf.Write(payload)
	return buf.Bytes()
}

// --- extensionID ---

func TestExtensionID_MatchesChromeAlgorithm(t *testing.T) {
	// Chrome derives an unpacked extension's ID from its absolute path. This
	// expectation was captured by loading an extension from this exact path in
	// a real Chrome session and reading back the id it assigned.
	got := extensionID("/private/tmp/rodney-test-extension")
	want := "kpcblmbemcppaagejgmknhdmdmmnhcml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtensionID_UsesLettersAToP(t *testing.T) {
	id := extensionID("/tmp/some-extension")
	if len(id) != 32 {
		t.Fatalf("expected a 32 character id, got %d: %q", len(id), id)
	}
	for _, c := range id {
		if c < 'a' || c > 'p' {
			t.Errorf("id contains out-of-range character %q: %s", c, id)
		}
	}
}

// TestExtensionIDPathBytes_Windows pins the Windows encoding, which differs
// from every other platform: Chrome hashes the raw bytes of its native path
// type, which on Windows is UTF-16, and it upper-cases the drive letter first.
func TestExtensionIDPathBytes_Windows(t *testing.T) {
	got := extensionIDPathBytesFor(`c:\ext`, "windows")
	want := []byte{'C', 0, ':', 0, '\\', 0, 'e', 0, 'x', 0, 't', 0}
	if !bytes.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtensionIDPathBytes_Posix(t *testing.T) {
	got := extensionIDPathBytesFor("/tmp/ext", "linux")
	if !bytes.Equal(got, []byte("/tmp/ext")) {
		t.Errorf("got %v, want the plain path bytes", got)
	}
}

func TestExtensionID_DiffersByPath(t *testing.T) {
	if extensionID("/tmp/a") == extensionID("/tmp/b") {
		t.Error("different paths should produce different ids")
	}
}

// --- parseStartArgs ---

func TestParseStartArgs_NoExtensions(t *testing.T) {
	opts, err := parseStartArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts.extensions) != 0 {
		t.Errorf("expected no extensions, got %v", opts.extensions)
	}
}

func TestParseStartArgs_SingleExtension(t *testing.T) {
	opts, err := parseStartArgs([]string{"--extension", "/tmp/ext"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts.extensions) != 1 || opts.extensions[0] != "/tmp/ext" {
		t.Errorf("got %v, want [/tmp/ext]", opts.extensions)
	}
}

func TestParseStartArgs_RepeatedExtensions(t *testing.T) {
	opts, err := parseStartArgs([]string{"--extension", "/tmp/one", "--extension", "/tmp/two"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts.extensions) != 2 || opts.extensions[0] != "/tmp/one" || opts.extensions[1] != "/tmp/two" {
		t.Errorf("got %v, want [/tmp/one /tmp/two]", opts.extensions)
	}
}

func TestParseStartArgs_ExtensionWithOtherFlags(t *testing.T) {
	opts, err := parseStartArgs([]string{"--show", "--extension", "/tmp/ext", "-k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.headless {
		t.Error("expected headless=false when --show is passed")
	}
	if !opts.ignoreCertErrors {
		t.Error("expected insecure=true when -k is passed")
	}
	if len(opts.extensions) != 1 {
		t.Errorf("got %v, want one extension", opts.extensions)
	}
}

// --- resolveExtension ---

func TestResolveExtension_Directory(t *testing.T) {
	dir := writeTestExtension(t, filepath.Join(t.TempDir(), "ext"))
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	info, err := resolveExtension(dir, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "Rodney Test Extension" {
		t.Errorf("got name %q", info.Name)
	}
	if info.Version != "1.2.3" {
		t.Errorf("got version %q", info.Version)
	}
	if info.Dir != dir {
		t.Errorf("got dir %q, want %q", info.Dir, dir)
	}
	if info.ID != extensionID(dir) {
		t.Errorf("got id %q, want %q", info.ID, extensionID(dir))
	}
}

func TestResolveExtension_RelativeDirectoryBecomesAbsolute(t *testing.T) {
	tmp := t.TempDir()
	writeTestExtension(t, filepath.Join(tmp, "ext"))
	t.Chdir(tmp)

	info, err := resolveExtension("ext", t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(info.Dir) {
		t.Errorf("expected an absolute path, got %q", info.Dir)
	}
}

func TestResolveExtension_ResolvesSymlinks(t *testing.T) {
	// Chrome derives the extension ID from the symlink-free path, so rodney has
	// to resolve links or it would report an ID Chrome never uses.
	real := writeTestExtension(t, filepath.Join(t.TempDir(), "real"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	info, err := resolveExtension(link, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if info.Dir != want {
		t.Errorf("got dir %q, want %q", info.Dir, want)
	}
}

func TestResolveExtension_MissingPath(t *testing.T) {
	_, err := resolveExtension(filepath.Join(t.TempDir(), "nope"), t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a missing path")
	}
	if !strings.Contains(err.Error(), "no such file") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveExtension_DirectoryWithoutManifest(t *testing.T) {
	_, err := resolveExtension(t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a directory with no manifest.json")
	}
	if !strings.Contains(err.Error(), "manifest.json") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveExtension_UnsupportedFileType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ext.tar.gz")
	if err := os.WriteFile(path, []byte("nope"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveExtension(path, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for an unsupported archive type")
	}
	if !strings.Contains(err.Error(), ".crx/.zip") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveExtension_Zip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packed.zip")
	payload := zipBytes(t, map[string]string{
		"manifest.json": testExtensionManifest,
		"content.js":    testExtensionContentScript,
	})
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}

	info, err := resolveExtension(path, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "Rodney Test Extension" {
		t.Errorf("got name %q", info.Name)
	}
	if _, err := os.Stat(filepath.Join(info.Dir, "content.js")); err != nil {
		t.Errorf("content.js was not extracted: %v", err)
	}
}

func TestResolveExtension_CRX3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packed.crx")
	payload := crx3Bytes(t, zipBytes(t, map[string]string{
		"manifest.json": testExtensionManifest,
		"content.js":    testExtensionContentScript,
	}))
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}

	info, err := resolveExtension(path, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Version != "1.2.3" {
		t.Errorf("got version %q", info.Version)
	}
}

func TestResolveExtension_CRX2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packed.crx")
	payload := crx2Bytes(t, zipBytes(t, map[string]string{
		"manifest.json": testExtensionManifest,
	}))
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveExtension(path, t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveExtension_ArchiveWithNestedDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested.zip")
	payload := zipBytes(t, map[string]string{
		"my-extension/manifest.json": testExtensionManifest,
		"my-extension/content.js":    testExtensionContentScript,
	})
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}

	info, err := resolveExtension(path, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(info.Dir, "manifest.json")); err != nil {
		t.Errorf("manifest.json should sit at the root of the resolved dir: %v", err)
	}
}

func TestResolveExtension_ArchiveWithoutManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.zip")
	if err := os.WriteFile(path, zipBytes(t, map[string]string{"readme.txt": "hi"}), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveExtension(path, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for an archive with no manifest.json")
	}
}

func TestResolveExtension_UnsupportedCRXVersion(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("Cr24")
	binary.Write(&buf, binary.LittleEndian, uint32(99))
	buf.Write(make([]byte, 8))
	path := filepath.Join(t.TempDir(), "future.crx")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := resolveExtension(path, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for an unknown crx version")
	}
	if !strings.Contains(err.Error(), "crx version 99") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUnpackDirName_StaysInsideUnpackRoot guards a nasty case: an archive named
// "...zip" has extension ".zip" and stem "..", which naively joined onto the
// unpack root escapes it and points at the session directory, which unpacking
// then deletes.
func TestUnpackDirName_StaysInsideUnpackRoot(t *testing.T) {
	root := "/state/extensions"
	for _, name := range []string{"...zip", "..zip", ".zip", ".crx", "..crx", "a/b/../../evil.zip", "ext.zip"} {
		dir := filepath.Join(root, unpackDirName(name))
		if filepath.Dir(dir) != root {
			t.Errorf("archive %q unpacks to %q, which is outside %q", name, dir, root)
		}
	}
}

func TestUnpackDirName_DistinctForSameBasename(t *testing.T) {
	if unpackDirName("/one/ext.zip") == unpackDirName("/two/ext.zip") {
		t.Error("archives with the same basename must not share an unpack directory")
	}
}

func TestUnpackDirName_StableForSamePath(t *testing.T) {
	if unpackDirName("/one/ext.zip") != unpackDirName("/one/ext.zip") {
		t.Error("the same archive path must map to the same unpack directory")
	}
}

// TestResolveExtension_ArchiveDoesNotEscapeUnpackRoot is the end-to-end version
// of the check above: it proves a hostile archive name cannot delete the
// session directory that contains the unpack root.
func TestResolveExtension_ArchiveDoesNotEscapeUnpackRoot(t *testing.T) {
	stateDir := t.TempDir()
	unpackRoot := filepath.Join(stateDir, "extensions")
	if err := os.MkdirAll(unpackRoot, 0755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(stateDir, "state.json")
	if err := os.WriteFile(canary, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "...zip")
	payload := zipBytes(t, map[string]string{"manifest.json": testExtensionManifest})
	if err := os.WriteFile(archive, payload, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveExtension(archive, unpackRoot); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Errorf("unpacking deleted the session directory: %v", err)
	}
}

func TestResolveExtension_SameBasenameArchivesStaySeparate(t *testing.T) {
	unpackRoot := t.TempDir()
	newArchive := func(dir, name, version string) string {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "ext.zip")
		manifest := `{"manifest_version": 3, "name": "` + name + `", "version": "` + version + `"}`
		if err := os.WriteFile(path, zipBytes(t, map[string]string{"manifest.json": manifest}), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	tmp := t.TempDir()
	first, err := resolveExtension(newArchive(filepath.Join(tmp, "a"), "First", "1.0.0"), unpackRoot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveExtension(newArchive(filepath.Join(tmp, "b"), "Second", "2.0.0"), unpackRoot)
	if err != nil {
		t.Fatal(err)
	}

	if first.Dir == second.Dir {
		t.Fatalf("both archives unpacked to %q", first.Dir)
	}
	if first.ID == second.ID {
		t.Error("both extensions were given the same id")
	}
	// The first extension must survive unpacking the second.
	if got, err := readManifest(first.Dir); err != nil || got.Name != "First" {
		t.Errorf("first extension was clobbered: name=%q err=%v", got.Name, err)
	}
}

func TestCrxZipOffset_RejectsOverflowingLengths(t *testing.T) {
	// A CRX2 header claiming a key length near uint32 max used to wrap around
	// to a small offset instead of being rejected.
	var crx2 bytes.Buffer
	crx2.WriteString("Cr24")
	binary.Write(&crx2, binary.LittleEndian, uint32(2))
	binary.Write(&crx2, binary.LittleEndian, uint32(0xFFFFFFF0))
	binary.Write(&crx2, binary.LittleEndian, uint32(0xFFFFFFF0))
	crx2.Write(make([]byte, 32))

	var crx3 bytes.Buffer
	crx3.WriteString("Cr24")
	binary.Write(&crx3, binary.LittleEndian, uint32(3))
	binary.Write(&crx3, binary.LittleEndian, uint32(0xFFFFFFFC))
	crx3.Write(make([]byte, 32))

	for name, payload := range map[string][]byte{"crx2": crx2.Bytes(), "crx3": crx3.Bytes()} {
		r := bytes.NewReader(payload)
		if _, err := crxZipOffset(r, int64(len(payload))); err == nil {
			t.Errorf("%s: expected an error for a header longer than the file", name)
		}
	}
}

func TestUnpackExtension_AcceptsArchiveRootEntry(t *testing.T) {
	// Some zip writers emit a "./" entry, which must not be mistaken for an
	// attempt to escape the destination directory.
	path := filepath.Join(t.TempDir(), "rooted.zip")
	payload := zipBytes(t, map[string]string{
		"./":            "",
		"manifest.json": testExtensionManifest,
	})
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveExtension(path, t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnpackExtension_RejectsPathTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evil.zip")
	payload := zipBytes(t, map[string]string{"../escaped.txt": "pwned"})
	if err := os.WriteFile(path, payload, 0644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "unpacked")
	err := unpackExtension(path, dest)
	if err == nil {
		t.Fatal("expected an error for an archive entry escaping the destination")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- end to end ---

// TestExtension_LoadsInHeadlessChrome is the test that matters: it launches a
// headless browser the same way "rodney start --extension" does and checks the
// extension's content script actually ran on a page.
// baseLauncher mirrors the flags cmdStart sets before configureExtensions runs.
func baseLauncher() *launcher.Launcher {
	return launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Leakless(false)
}

// headlessMode reports the --headless value: "" for old headless (the flag is
// present but valueless), "new" for new headless, "off" when it is absent.
// launcher.Get panics on a valueless flag, so read the values directly.
func headlessMode(l *launcher.Launcher) string {
	values, ok := l.GetFlags("headless")
	if !ok {
		return "off"
	}
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func TestConfigureExtensions_NoExtensionsLeavesLauncherAlone(t *testing.T) {
	l := configureExtensions(baseLauncher().Headless(true), true, nil)

	if !l.Has("single-process") {
		t.Error("--single-process was dropped for a launch with no extensions")
	}
	if got := headlessMode(l); got != "" {
		t.Errorf("headless = %q, want %q (old headless) when no extensions are loaded", got, "")
	}
	if l.Has("load-extension") {
		t.Error("--load-extension set with no extensions")
	}
}

// New headless brings up the full browser stack (renderer, GPU, utility services,
// the extension service worker). --single-process collapses all of that into one
// OS process where a single CHECK failure kills the whole browser, so extensions
// must drop it.
func TestConfigureExtensions_DropsSingleProcess(t *testing.T) {
	l := configureExtensions(baseLauncher().Headless(true), true,
		[]extensionInfo{{Dir: "/tmp/ext"}})

	if l.Has("single-process") {
		t.Error("--single-process survived; new headless + extensions must not run single-process")
	}
}

func TestConfigureExtensions_SelectsNewHeadless(t *testing.T) {
	l := configureExtensions(baseLauncher().Headless(true), true,
		[]extensionInfo{{Dir: "/tmp/ext"}})

	if got := headlessMode(l); got != "new" {
		t.Errorf("headless = %q, want %q; old headless cannot run extensions", got, "new")
	}
}

// --show must stay a real visible window, not new headless.
func TestConfigureExtensions_HeadedStaysHeaded(t *testing.T) {
	l := configureExtensions(baseLauncher().Headless(false), false,
		[]extensionInfo{{Dir: "/tmp/ext"}})

	if got := headlessMode(l); got != "off" {
		t.Errorf("headless = %q, want it absent, for a --show launch", got)
	}
	if l.Has("single-process") {
		t.Error("--single-process survived; extensions must not run single-process")
	}
}

func TestConfigureExtensions_JoinsDirsWithCommas(t *testing.T) {
	l := configureExtensions(baseLauncher().Headless(true), true,
		[]extensionInfo{{Dir: "/tmp/one"}, {Dir: "/tmp/two"}})

	const want = "/tmp/one,/tmp/two"
	if got := l.Get("load-extension"); got != want {
		t.Errorf("load-extension = %q, want %q", got, want)
	}
	if got := l.Get("disable-extensions-except"); got != want {
		t.Errorf("disable-extensions-except = %q, want %q", got, want)
	}
}

// rod disables some features by default; the extension switch must be appended
// to those rather than replacing them.
func TestConfigureExtensions_AppendsToExistingDisableFeatures(t *testing.T) {
	base := baseLauncher().Headless(true).Set("disable-features", "TranslateUI")
	l := configureExtensions(base, true, []extensionInfo{{Dir: "/tmp/ext"}})

	flags, _ := l.GetFlags("disable-features")
	got := strings.Join(flags, ",")
	const want = "TranslateUI,DisableLoadExtensionCommandLineSwitch"
	if got != want {
		t.Errorf("disable-features = %q, want %q", got, want)
	}
}

func TestExtension_LoadsInHeadlessChrome(t *testing.T) {
	dir := writeTestExtension(t, filepath.Join(t.TempDir(), "ext"))

	l := configureExtensions(baseLauncher().Headless(true), true,
		[]extensionInfo{{Dir: dir}})

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	browser := rod.New().ControlURL(l.MustLaunch()).MustConnect()
	defer browser.MustClose()

	page := browser.MustPage(env.server.URL + "/")
	page.MustWaitLoad()

	if got := page.MustInfo().Title; got != "extension-was-here" {
		t.Errorf("content script did not run: page title is %q, want %q", got, "extension-was-here")
	}
}
