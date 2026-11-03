package ua

import "testing"

func TestParseDesktopChrome(t *testing.T) {
	info := Parse("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	if info.Browser != "Chrome" {
		t.Errorf("browser: got %q, want Chrome", info.Browser)
	}
	if info.OS != "Windows" {
		t.Errorf("os: got %q, want Windows", info.OS)
	}
	if info.Device != "Desktop" {
		t.Errorf("device: got %q, want Desktop", info.Device)
	}
}

func TestParseMobileAndroid(t *testing.T) {
	info := Parse("Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36")
	if info.Browser != "Chrome" {
		t.Errorf("browser: got %q, want Chrome", info.Browser)
	}
	if info.OS != "Android" {
		t.Errorf("os: got %q, want Android", info.OS)
	}
	if info.Device != "Mobile" {
		t.Errorf("device: got %q, want Mobile", info.Device)
	}
}

func TestParseTabletIPad(t *testing.T) {
	info := Parse("Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")
	if info.Browser != "Safari" {
		t.Errorf("browser: got %q, want Safari", info.Browser)
	}
	if info.OS != "iOS" {
		t.Errorf("os: got %q, want iOS", info.OS)
	}
	if info.Device != "Tablet" {
		t.Errorf("device: got %q, want Tablet", info.Device)
	}
}

func TestParseIPhoneSafari(t *testing.T) {
	info := Parse("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")
	if info.Browser != "Safari" {
		t.Errorf("browser: got %q, want Safari", info.Browser)
	}
	if info.OS != "iOS" {
		t.Errorf("os: got %q, want iOS", info.OS)
	}
	if info.Device != "Mobile" {
		t.Errorf("device: got %q, want Mobile", info.Device)
	}
}

func TestParseFirefoxMac(t *testing.T) {
	info := Parse("Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:127.0) Gecko/20100101 Firefox/127.0")
	if info.Browser != "Firefox" {
		t.Errorf("browser: got %q, want Firefox", info.Browser)
	}
	if info.OS != "macOS" {
		t.Errorf("os: got %q, want macOS", info.OS)
	}
	if info.Device != "Desktop" {
		t.Errorf("device: got %q, want Desktop", info.Device)
	}
}

func TestParseEmpty(t *testing.T) {
	info := Parse("")
	if info.Device != "Desktop" {
		t.Errorf("device: got %q, want Desktop", info.Device)
	}
	if info.Browser != "" || info.OS != "" {
		t.Errorf("expected empty browser/os, got %+v", info)
	}
}
