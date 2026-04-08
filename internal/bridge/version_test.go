package bridge

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.2.0", -1},
		{"v0.2.0", "v0.2.0", 0},
		{"v0.2.0", "v0.1.9", 1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.2.0-rc1", "v0.2.0", 0}, // pre-release stripped
		{"0.1.0", "v0.1.0", 0},      // optional v prefix
		{"v0.1", "v0.1.0", 0},       // missing patch defaults to 0
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestToHTTPURL(t *testing.T) {
	cases := map[string]string{
		"http://x:3000":  "http://x:3000",
		"https://x":      "https://x",
		"ws://x:3000":    "http://x:3000",
		"wss://x":        "https://x",
	}
	for in, want := range cases {
		got, err := toHTTPURL(in)
		if err != nil || got != want {
			t.Errorf("toHTTPURL(%q) = %q,%v want %q", in, got, err, want)
		}
	}
	if _, err := toHTTPURL("ftp://x"); err == nil {
		t.Error("expected error for unsupported scheme")
	}
}

func TestBridgeVersionSkewWarning(t *testing.T) {
	srvURL, token := startServer(t)

	// Bridge version older than the platform's MinRecommended
	// (v0.2.0). Use immediate-EOF stdin so Run terminates as soon
	// as the pumps spin up — we only care about the warning
	// emitted before the dial.
	var stderr strings.Builder
	err := Run(context.Background(), Config{
		URL:     srvURL,
		Token:   token,
		Version: "v0.0.1",
		Stdin:   emptyReader{},
		Stdout:  io.Discard,
		Stderr:  &stderr,
	})
	// Run returns nil on EOF + clean shutdown.
	if err != nil {
		t.Errorf("Run err = %v", err)
	}
	if !strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("expected WARNING in stderr, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "v0.0.1") {
		t.Errorf("expected old version in warning, got %q", stderr.String())
	}
}

func TestBridgeVersionNoWarningWhenCurrent(t *testing.T) {
	srvURL, token := startServer(t)

	var stderr strings.Builder
	err := Run(context.Background(), Config{
		URL:     srvURL,
		Token:   token,
		Version: "v9.9.9", // newer than MinRecommended
		Stdin:   emptyReader{},
		Stdout:  io.Discard,
		Stderr:  &stderr,
	})
	if err != nil {
		t.Errorf("Run err = %v", err)
	}
	if strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("did not expect WARNING for current bridge, got %q", stderr.String())
	}
}

func TestBridgeVersionDevSkipsCheck(t *testing.T) {
	srvURL, token := startServer(t)
	var stderr strings.Builder
	err := Run(context.Background(), Config{
		URL:     srvURL,
		Token:   token,
		Version: "dev",
		Stdin:   emptyReader{},
		Stdout:  io.Discard,
		Stderr:  &stderr,
	})
	if err != nil {
		t.Errorf("Run err = %v", err)
	}
	// "dev" must NOT trigger the warning even though it lexically
	// looks "older" than v0.2.0 — devs build from main constantly.
	if strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("dev build should skip skew check, got %q", stderr.String())
	}
}

type emptyReader struct{}

func (emptyReader) Read(p []byte) (int, error) { return 0, io.EOF }
