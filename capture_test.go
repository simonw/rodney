package main

import (
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// visibilityState reports what the page thinks its own visibility is, which is
// how Chromium exposes whether a target is the foreground one.
func visibilityState(t *testing.T, p *rod.Page) string {
	t.Helper()
	res, err := p.Eval("() => document.visibilityState")
	if err != nil {
		t.Fatalf("reading visibilityState: %v", err)
	}
	return res.Value.String()
}

// openTwoPages returns a page that has been pushed into the background by a
// second one, plus that second page.
func openTwoPages(t *testing.T) (background, foreground *rod.Page) {
	t.Helper()
	background = env.browser.MustPage(env.server.URL)
	t.Cleanup(func() { background.MustClose() })
	foreground = env.browser.MustPage(env.server.URL + "/form")
	t.Cleanup(func() { foreground.MustClose() })

	if got := visibilityState(t, background); got != "hidden" {
		t.Fatalf("first page visibilityState = %q, want %q -- opening a second page "+
			"is supposed to background the first, so this test proves nothing", got, "hidden")
	}
	return background, foreground
}

func TestBringToFront_MakesTargetVisible(t *testing.T) {
	background, _ := openTwoPages(t)

	bringToFront(background)

	if got := visibilityState(t, background); got != "visible" {
		t.Errorf("visibilityState = %q after bringToFront, want %q", got, "visible")
	}
}

// The regression. A backgrounded target has no compositor producing frames, so
// Page.captureScreenshot waits for one that never arrives and the CLI reports
// "screenshot failed: context deadline exceeded". Without the bringToFront call
// this test hangs until the timeout below and fails.
func TestScreenshot_BackgroundedTargetDoesNotHang(t *testing.T) {
	background, _ := openTwoPages(t)

	bringToFront(background)

	data, err := background.Timeout(20*time.Second).Screenshot(true, nil)
	if err != nil {
		t.Fatalf("full-page screenshot of a backgrounded target: %v", err)
	}
	if len(data) == 0 {
		t.Error("screenshot returned no data")
	}
}

// Element capture goes through rod's visibility/stability wait as well, where a
// hidden target hangs for far longer than the screenshot deadline.
func TestScreenshotEl_BackgroundedTargetDoesNotHang(t *testing.T) {
	background, _ := openTwoPages(t)

	bringToFront(background)

	// rod's element capture ignores the page timeout while it waits for the
	// element to become stable, and on a hidden target that wait outlives the
	// whole test binary. Bound it here so a regression fails this one test
	// instead of panicking the suite ten minutes later.
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		el, err := background.Element("h1")
		if err != nil {
			done <- result{err: err}
			return
		}
		data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
		done <- result{data: data, err: err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("element screenshot of a backgrounded target: %v", r.err)
		}
		if len(r.data) == 0 {
			t.Error("element screenshot returned no data")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("element screenshot of a backgrounded target did not finish within 30s")
	}
}
