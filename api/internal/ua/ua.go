// Package ua parses User-Agent strings into browser, OS and device info
// (ТЗ §12). The matchers are intentionally small and deterministic: they cover
// the common desktop and mobile agents without external dependencies.
package ua

import (
	"strings"
)

// Info describes a parsed User-Agent.
type Info struct {
	// Browser is e.g. "Chrome", "Firefox", "Safari" (empty when unknown).
	Browser string
	// OS is e.g. "Windows", "macOS", "Android" (empty when unknown).
	OS string
	// Device is one of "Desktop", "Mobile", "Tablet" (empty when unknown).
	Device string
}

var mobilePatterns = []string{
	"Android",
	"iPhone",
	"iPad",
	"iPod",
	"Windows Phone",
	"Mobile",
}

var tabletPatterns = []string{"iPad", "Tablet"}

var browserChecks = []struct {
	marker string
	name   string
}{
	{"Edg/", "Edge"},
	{"OPR/", "Opera"},
	{"Opera", "Opera"},
	{"YaBrowser/", "Yandex Browser"},
	{"Chrome/", "Chrome"},
	{"Firefox/", "Firefox"},
	{"Safari/", "Safari"},
	{"MSIE ", "Internet Explorer"},
	{"Trident/", "Internet Explorer"},
}

var osChecks = []struct {
	marker string
	name   string
}{
	{"Windows NT 10", "Windows"},
	{"Windows NT 6.3", "Windows"},
	{"Windows NT 6.1", "Windows"},
	{"Windows", "Windows"},
	{"iPhone OS", "iOS"},
	{"iPad", "iOS"},
	{"iPod", "iOS"},
	{"Mac OS X", "macOS"},
	{"Android", "Android"},
	{"Linux", "Linux"},
	{"CrOS", "ChromeOS"},
}

// Parse extracts browser, OS and device from a raw User-Agent header.
func Parse(raw string) Info {
	if strings.TrimSpace(raw) == "" {
		return Info{Device: "Desktop"}
	}

	info := Info{Device: "Desktop"}
	for _, check := range browserChecks {
		if strings.Contains(raw, check.marker) {
			info.Browser = check.name
			break
		}
	}
	for _, check := range osChecks {
		if strings.Contains(raw, check.marker) {
			info.OS = check.name
			break
		}
	}

	lower := strings.ToLower(raw)
	if containsAny(lower, tabletPatterns...) {
		info.Device = "Tablet"
	} else if containsAny(lower, mobilePatterns...) {
		info.Device = "Mobile"
	}
	return info
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
