package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// platformVersionInfo is the shape of GET /api/v1/bridge/version.
type platformVersionInfo struct {
	PlatformVersion string `json:"platform_version"`
	MinRecommended  string `json:"min_recommended"`
}

// fetchPlatformVersion calls /api/v1/bridge/version against the
// configured base URL. The endpoint is unauthenticated so this
// runs before the token-bearing WebSocket dial. A failure here is
// logged to stderr and treated as "skip the skew check" — the
// bridge must still be usable against an older platform that
// hasn't shipped the endpoint yet.
func fetchPlatformVersion(ctx context.Context, baseURL string) (*platformVersionInfo, error) {
	httpURL, err := toHTTPURL(baseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"/api/v1/bridge/version", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var info platformVersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// toHTTPURL is the inverse of toWebSocketURL — accepts ws/wss/http
// /https URLs and returns the http(s) form. Used for the version
// probe because that's a plain HTTP GET, not a WebSocket dial.
func toHTTPURL(base string) (string, error) {
	switch {
	case strings.HasPrefix(base, "http://"), strings.HasPrefix(base, "https://"):
		return base, nil
	case strings.HasPrefix(base, "wss://"):
		return "https://" + strings.TrimPrefix(base, "wss://"), nil
	case strings.HasPrefix(base, "ws://"):
		return "http://" + strings.TrimPrefix(base, "ws://"), nil
	}
	return "", fmt.Errorf("scheme must be http(s)/ws(s), got %q", base)
}

// compareVersions returns -1, 0, or 1 if a is older, equal, or
// newer than b. Both arguments are `vMAJOR.MINOR.PATCH` strings;
// missing components default to 0. Pre-release / build metadata
// suffixes are stripped before comparison so `v0.2.0-rc1` sorts
// alongside `v0.2.0` — close enough for skew warnings, and the
// alternative (a real semver dependency) isn't worth the weight.
func compareVersions(a, b string) int {
	pa := parseVersion(a)
	pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	// Strip pre-release suffix (-rc1, -alpha) and build metadata (+sha)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(part)
		out[i] = n
	}
	return out
}
