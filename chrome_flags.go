package main

import (
	"runtime"

	"github.com/go-rod/rod/lib/launcher"
)

// singleProcessSupported reports whether --single-process is safe to pass on
// this platform.
//
// The flag collapses Chromium's renderer, GPU and utility services into the
// browser process. It is there because the multi-process compositor hangs under
// gVisor and screenshots time out (notes/gvisor-screenshots/README.md), but on
// macOS the collapse is fatal. media/capture's Apple backend CHECKs that it owns
// a CFRunLoop-enabled thread, which only holds when video capture has a utility
// process of its own, so any page that touches navigator.mediaDevices aborts the
// whole browser:
//
//	FATAL:video_capture_device_factory_apple.mm(37)] Check failed: mode.
//	The MacOS video capture code must be run on a CFRunLoop-enabled thread
//
// Device enumeration is routine in the fingerprinting scripts commercial sites
// ship, which is what made this look like "heavy sites crash the browser".
// Losing --single-process on macOS costs nothing: gVisor is a Linux sandbox.
func singleProcessSupported() bool {
	return runtime.GOOS != "darwin"
}

// configureExperiments stops Chromium from running features that are still
// being written.
//
// rodney's pinned browser (128.0.6568.0) is a development snapshot, and those
// builds apply testing/variations/fieldtrial_testing_config.json by default,
// which force-enables in-development features. One of them, HistoryEmbeddings,
// computes passage embeddings on the browser's main thread after a navigation
// and CHECK-fails when its cache lookup misses, killing the browser process a
// few seconds after a text-heavy page loads:
//
//	FATAL:history_embeddings_service.cc(597)] Check failed:
//	cached_embedding != embedding_cache.end()
//
// It needs the on-device model the optimization guide downloads, so it only
// starts biting once a profile has been used for a while — a long-lived
// ~/.rodney profile crashes where a throwaway one does not.
//
// --disable-field-trial-config drops that config wholesale, so rodney drives a
// browser with shipped defaults rather than whatever happened to be mid-flight
// when the snapshot was cut. HistoryEmbeddings is named explicitly as well
// because a browser supplied through ROD_CHROME_BIN can enable it from a
// server-side variations seed, which that switch does not cover.
func configureExperiments(l *launcher.Launcher) *launcher.Launcher {
	// Append, not Set: rod already disables features of its own.
	return l.Set("disable-field-trial-config").
		Append("disable-features", "HistoryEmbeddings")
}
