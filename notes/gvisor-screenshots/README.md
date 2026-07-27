# Chrome Screenshots in gVisor Environments

## The Problem

`rodney screenshot` crashed Chrome (Chromium 128) with an `EOF` error after ~27 seconds.
The Chrome process terminated during any `Page.captureScreenshot` CDP call, including
the simplest possible variant (no viewport override, no full-page, tiny clip area).

The `rodney pdf` command also failed: `{-32000 Printing failed}`.

All other CDP operations (navigation, JS evaluation, DOM queries, clicking) worked fine.

## Environment

- **Kernel**: gVisor (Linux 4.4.0 emulated)
- **Chromium**: 128.0.6568.0 (rod-managed, `chromium-1321438`)
- **Memory**: 21 GB available (not a memory issue)
- **No display server**: headless mode only

gVisor is a user-space kernel that sandboxes containers by intercepting syscalls.
It has known limitations with certain IPC mechanisms that Chrome's multi-process
architecture depends on.

## Investigation

### Systematic testing

Tested 6 different Chrome configurations against a simple data URL page
(`<h1>Hello</h1>`), each with a 10-second timeout on `Page.captureScreenshot`:

| # | Configuration | Result |
|---|---|---|
| 1 | Standard `--headless --disable-gpu` | Timeout (Chrome crashes at ~27s) |
| 2 | `--headless=new` (new headless mode) | Timeout |
| 3 | `--enable-unsafe-swiftshader` | Timeout |
| 4 | `--use-gl=angle --use-angle=swiftshader` | Timeout |
| 5 | `--disable-gpu --in-process-gpu` | Timeout |
| 6 | **`--single-process --disable-gpu`** | **OK: 6052 bytes in 86ms** |

Also tested:
- PDF generation (old headless): `{-32000 Printing failed}`
- PDF generation (new headless): `{-32000 Printing failed}`
- JS Canvas (`canvas.toDataURL()`): Timeout (canvas rendering also broken)

### Key finding: `--single-process` fixes everything

With `--single-process`, all screenshot-related operations work:

| Operation | Time |
|---|---|
| PNG screenshot (viewport) | 66ms |
| PNG screenshot (full-page) | 68ms |
| PDF generation | 19ms |
| JS Canvas rendering | 9ms |

## Root Cause

Chrome's default multi-process architecture separates the browser, GPU/compositor,
and renderer into different OS processes that communicate via IPC (shared memory,
Unix domain sockets, pipes).

The `Page.captureScreenshot` CDP command requires the **compositor** (in the GPU
process) to produce a rendered frame and send it back to the browser process.
This compositor ↔ browser IPC pathway is broken under gVisor.

Specifically:
1. The CDP command reaches the browser process fine (we see no immediate error)
2. The browser process asks the GPU/compositor to render a frame
3. The GPU process either hangs or crashes trying to composite
4. After ~27 seconds, Chrome's watchdog kills the hung GPU process
5. This cascades to kill the entire browser (EOF on WebSocket)

Evidence that the GPU process is the bottleneck:
- JS evaluation works (runs in renderer, returns via browser - no GPU involved)
- DOM manipulation works (same path)
- Canvas rendering (`getContext('2d')`, `toDataURL()`) also fails - canvas operations
  route through the GPU process for compositing even in software mode
- `--in-process-gpu` alone doesn't help (the GPU code still uses syscalls gVisor
  can't handle, just within the same process)
- `--single-process` works because Chrome falls back to a simpler internal
  compositing path when everything runs in one process

### Why `--in-process-gpu` doesn't help but `--single-process` does

`--in-process-gpu` moves the GPU thread into the browser process but still uses
the full GPU compositing pipeline (Viz, Skia with hardware-like paths). The
problematic syscalls/IPC still occur.

`--single-process` goes further: it runs browser, renderer, and GPU all in one
process AND uses simplified internal communication paths. The compositor uses
software-only rendering without the Viz display compositor's full IPC machinery.

## Fix Applied

Added `Set("single-process")` to the Chrome launcher in `cmdStart()`:

```go
l := launcher.New().
    Set("no-sandbox").
    Set("disable-gpu").
    Set("single-process"). // Required for screenshots in gVisor/container environments
    Headless(true).
    Leakless(false).
    UserDataDir(dataDir)
```

## Trade-offs of `--single-process`

**Pros:**
- Screenshots, PDF, and canvas rendering all work
- Slightly lower memory usage (one process instead of 6+)
- Faster startup

**Cons:**
- A renderer crash takes down the whole browser (no process isolation)
- Chrome officially warns against `--single-process` for stability
- Some web features may behave differently

For a CLI automation tool that controls one page at a time, these trade-offs
are acceptable. The stability concern is mitigated by the fact that `rodney stop`
and `rodney start` can quickly restart Chrome.

## Not applied on macOS

The "a crash takes down the whole browser" trade-off turned out to be more than
theoretical on macOS, so `rodney start` skips `--single-process` there
(`singleProcessSupported` in `chrome_flags.go`).

`media/capture`'s Apple backend CHECKs that it owns a CFRunLoop-enabled thread,
which only holds when video capture runs in a utility process of its own. Under
`--single-process` that CHECK fails and aborts the browser:

```
FATAL:video_capture_device_factory_apple.mm(37)] Check failed: mode.
The MacOS video capture code must be run on a CFRunLoop-enabled thread
```

Any page calling `navigator.mediaDevices.enumerateDevices()` triggers it, which
the device-fingerprinting scripts on most commercial sites do:

```bash
rodney open https://example.com/
rodney js "navigator.mediaDevices.enumerateDevices().then(d=>d.length)"
```

gVisor is a Linux sandbox, so nothing in this investigation is lost by skipping
the flag on Darwin. Linux and Windows keep it.

## Reproducing

The test script `screenshot_test.go` in this directory reproduces the investigation.
Run it against a fresh Chrome instance to verify the fix:

```bash
cd notes/gvisor-screenshots
go run screenshot_test.go
```
