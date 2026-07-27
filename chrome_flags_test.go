package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/launcher"
)

// features returns the --disable-features values as rod would render them.
func features(l *launcher.Launcher) string {
	values, ok := l.GetFlags("disable-features")
	if !ok {
		return ""
	}
	return strings.Join(values, ",")
}

// HistoryEmbeddings runs in the browser process, so its CHECK failure takes the
// whole browser down rather than one tab. It is enabled by the field-trial
// testing config baked into the pinned Chromium snapshot.
func TestConfigureExperiments_DisablesFieldTrialConfig(t *testing.T) {
	l := configureExperiments(launcher.New())

	if !l.Has("disable-field-trial-config") {
		t.Error("--disable-field-trial-config missing; the snapshot will force-enable in-development features")
	}
}

func TestConfigureExperiments_DisablesHistoryEmbeddings(t *testing.T) {
	l := configureExperiments(launcher.New())

	if !strings.Contains(features(l), "HistoryEmbeddings") {
		t.Errorf("disable-features = %q, want it to contain HistoryEmbeddings", features(l))
	}
}

// rod disables features of its own, and configureExtensions appends one more.
// Setting rather than appending would silently re-enable them.
func TestConfigureExperiments_KeepsExistingDisabledFeatures(t *testing.T) {
	l := configureExperiments(launcher.New().Set("disable-features", "site-per-process", "TranslateUI"))

	got := features(l)
	for _, want := range []string{"site-per-process", "TranslateUI", "HistoryEmbeddings"} {
		if !strings.Contains(got, want) {
			t.Errorf("disable-features = %q, want it to contain %q", got, want)
		}
	}
}

// Later callers append to the same flag; the values must accumulate rather than
// replace each other.
func TestConfigureExperiments_LeavesRoomForLaterAppends(t *testing.T) {
	l := configureExperiments(launcher.New().Set("disable-features", "site-per-process"))
	l = l.Append("disable-features", "SomeLaterFeature")

	got := features(l)
	for _, want := range []string{"site-per-process", "HistoryEmbeddings", "SomeLaterFeature"} {
		if !strings.Contains(got, want) {
			t.Errorf("disable-features = %q, want it to contain %q", got, want)
		}
	}
}

// On macOS --single-process makes any navigator.mediaDevices call abort the
// browser; on Linux it is what makes screenshots work under gVisor.
func TestSingleProcessSupported_SkipsMacOSOnly(t *testing.T) {
	got := singleProcessSupported()
	want := runtime.GOOS != "darwin"

	if got != want {
		t.Errorf("singleProcessSupported() = %v on %s, want %v", got, runtime.GOOS, want)
	}
}
