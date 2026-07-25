package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/go-rod/rod/lib/launcher"
)

// extensionList collects repeatable --extension flag values.
type extensionList []string

func (e *extensionList) String() string { return strings.Join(*e, ",") }

func (e *extensionList) Set(v string) error {
	if v == "" {
		return fmt.Errorf("--extension requires a path")
	}
	*e = append(*e, v)
	return nil
}

// configureExtensions points Chrome at the given extensions, returning l
// unchanged when there are none.
//
// Old headless Chrome cannot run extensions at all, so extensions switch to the
// new headless mode: https://developer.chrome.com/docs/chromium/new-headless
//
// New headless is the full browser stack — renderer, GPU, utility services and
// the extension's own service worker — where rodney's --single-process becomes
// dangerous: it collapses all of those into one OS process (measured: 0 child
// processes and 205 threads, versus 10 and 59 without it), so any CHECK failure
// or bad access anywhere takes down the entire browser rather than one renderer.
// It is dropped here only; launches without --extension keep it, preserving the
// screenshot behaviour it was added for in gVisor/container environments.
func configureExtensions(l *launcher.Launcher, headless bool, extensions []extensionInfo) *launcher.Launcher {
	if len(extensions) == 0 {
		return l
	}
	dirs := make([]string, len(extensions))
	for i, ext := range extensions {
		dirs[i] = ext.Dir
	}
	l = l.HeadlessNew(headless).
		Delete("single-process").
		Set("load-extension", strings.Join(dirs, ",")).
		Set("disable-extensions-except", strings.Join(dirs, ","))
	// Chrome 137+ ignores --load-extension unless this feature is turned off.
	// Append rather than Set: rod already disables some features by default.
	return l.Append("disable-features", "DisableLoadExtensionCommandLineSwitch")
}

// manifest is the subset of manifest.json we care about.
type manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// extensionInfo describes one extension that was handed to Chrome.
type extensionInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Dir     string `json:"dir"`
}

// extensionID computes the ID Chrome assigns to an unpacked extension, which is
// derived from the absolute path of its directory: the first 16 bytes of the
// SHA-256 of the path, with each hex digit mapped from 0-f onto a-p.
func extensionID(dir string) string {
	sum := sha256.Sum256(extensionIDPathBytes(dir))
	id := make([]byte, 32)
	for i, b := range sum[:16] {
		id[i*2] = 'a' + (b >> 4)
		id[i*2+1] = 'a' + (b & 0x0f)
	}
	return string(id)
}

// extensionIDPathBytes reproduces the bytes Chrome hashes for a path. Chrome
// hashes the raw bytes of its native path representation, which is UTF-16 on
// Windows (where it also upper-cases the drive letter) and UTF-8 elsewhere.
// See crx_file::id_util::GenerateIdForPath in the Chromium source.
func extensionIDPathBytes(dir string) []byte {
	return extensionIDPathBytesFor(dir, runtime.GOOS)
}

// extensionIDPathBytesFor is extensionIDPathBytes with the OS passed in, so the
// Windows encoding can be tested from any platform.
func extensionIDPathBytesFor(dir, goos string) []byte {
	if goos != "windows" {
		return []byte(dir)
	}
	if len(dir) >= 2 && dir[1] == ':' {
		dir = strings.ToUpper(dir[:1]) + dir[1:]
	}
	var buf bytes.Buffer
	for _, unit := range utf16.Encode([]rune(dir)) {
		binary.Write(&buf, binary.LittleEndian, unit)
	}
	return buf.Bytes()
}

// readManifest loads name and version from an extension directory.
func readManifest(dir string) (manifest, error) {
	var m manifest
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, fmt.Errorf("no manifest.json in %s", dir)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("invalid manifest.json in %s: %v", dir, err)
	}
	return m, nil
}

// resolveExtension turns a user-supplied path into a directory Chrome can load.
// Directories are used as-is; .crx and .zip archives are unpacked underneath
// unpackRoot. The returned path is absolute and contains a manifest.json.
func resolveExtension(path, unpackRoot string) (extensionInfo, error) {
	var info extensionInfo

	abs, err := filepath.Abs(path)
	if err != nil {
		return info, fmt.Errorf("%s: %v", path, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return info, fmt.Errorf("%s: no such file or directory", path)
	}

	dir := abs
	if !st.IsDir() {
		switch strings.ToLower(filepath.Ext(abs)) {
		case ".crx", ".zip":
			dir = filepath.Join(unpackRoot, unpackDirName(abs))
			if err := unpackExtension(abs, dir); err != nil {
				return info, fmt.Errorf("%s: %v", path, err)
			}
		default:
			return info, fmt.Errorf("%s: expected a directory or a .crx/.zip archive", path)
		}
	}

	// Chrome resolves symlinks before deriving the extension ID from the path
	// (on macOS /tmp/x is really /private/tmp/x), so resolve them here too or
	// the id we report back would not be the one Chrome uses.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	// Chrome separates --load-extension paths with commas, so a path containing
	// one would silently be split into two bogus paths.
	if strings.Contains(dir, ",") {
		return info, fmt.Errorf("%s: extension paths cannot contain commas", path)
	}

	m, err := readManifest(dir)
	if err != nil {
		return info, err
	}
	return extensionInfo{ID: extensionID(dir), Name: m.Name, Version: m.Version, Dir: dir}, nil
}

// unpackDirName picks the directory an archive is unpacked into. The archive's
// own name is only a readable prefix: names like "...zip" or ".zip" reduce to
// "..", "." or "", which would escape the unpack root and get deleted, so the
// full path is hashed to guarantee a distinct, well-formed directory name.
func unpackDirName(archivePath string) string {
	sum := sha256.Sum256([]byte(archivePath))
	suffix := hex.EncodeToString(sum[:4])

	name := strings.TrimSuffix(filepath.Base(archivePath), filepath.Ext(archivePath))
	name = strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '_' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, ".-")
	if name == "" {
		return suffix
	}
	return name + "-" + suffix
}

// unpackExtension extracts a .crx or .zip archive into dest, which is replaced
// if it already exists. If the archive wraps everything in a single top-level
// directory, dest is rewritten to point at that directory instead.
func unpackExtension(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	offset, err := crxZipOffset(f, size)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, offset, size-offset), size-offset)
	if err != nil {
		return fmt.Errorf("not a readable crx/zip archive: %v", err)
	}

	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	for _, zf := range zr.File {
		if err := extractZipEntry(zf, dest); err != nil {
			return err
		}
	}

	if _, err := os.Stat(filepath.Join(dest, "manifest.json")); err == nil {
		return nil
	}
	// Some archives nest the extension one level down.
	entries, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		nested := filepath.Join(dest, e.Name())
		if _, err := os.Stat(filepath.Join(nested, "manifest.json")); err == nil {
			return moveDir(nested, dest)
		}
	}
	return fmt.Errorf("archive does not contain a manifest.json")
}

// crxZipOffset returns the offset of the zip payload within a crx file of the
// given size. Plain zip files (no "Cr24" magic) start at 0. The declared
// lengths are attacker-controlled, so they are widened to uint64 before being
// added and the result is checked against the file size.
func crxZipOffset(r io.ReaderAt, size int64) (int64, error) {
	header := make([]byte, 16)
	if _, err := r.ReadAt(header, 0); err != nil {
		return 0, fmt.Errorf("file is too small to be an extension archive")
	}
	if string(header[:4]) != "Cr24" {
		return 0, nil // plain zip
	}

	var offset uint64
	switch version := binary.LittleEndian.Uint32(header[4:8]); version {
	case 2:
		// magic + version + key length + signature length, then key and signature
		keyLen := uint64(binary.LittleEndian.Uint32(header[8:12]))
		sigLen := uint64(binary.LittleEndian.Uint32(header[12:16]))
		offset = 16 + keyLen + sigLen
	case 3:
		// magic + version + header length, then the protobuf header
		offset = 12 + uint64(binary.LittleEndian.Uint32(header[8:12]))
	default:
		return 0, fmt.Errorf("unsupported crx version %d", version)
	}

	if offset > uint64(size) {
		return 0, fmt.Errorf("crx header extends past the end of the file")
	}
	return int64(offset), nil
}

// extractZipEntry writes a single zip entry underneath dest, rejecting paths
// that would escape it.
func extractZipEntry(zf *zip.File, dest string) error {
	target := filepath.Join(dest, filepath.FromSlash(zf.Name))
	if target == filepath.Clean(dest) {
		return nil // an entry for the archive root itself, nothing to write
	}
	if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
		return fmt.Errorf("archive entry escapes the destination directory: %s", zf.Name)
	}
	if zf.FileInfo().IsDir() {
		return os.MkdirAll(target, 0755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// moveDir replaces dest with the contents of the nested directory src.
func moveDir(src, dest string) error {
	tmp := dest + ".unwrap"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.Rename(src, tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
