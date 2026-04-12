package docker

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// T509 sandbox spec tests. These exercise buildHostConfig directly
// so we don't need a real docker daemon — every assertion is on the
// HostConfig struct the runtime would hand to ContainerCreate.

func TestBuildHostConfig_DefaultsCapDropALL(t *testing.T) {
	r := &Runtime{pidsLimit: defaultPidsLimit}
	hc := r.buildHostConfig(1<<30, 1<<30, 1.0)
	if len(hc.CapDrop) != 1 || hc.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v, want [ALL]", hc.CapDrop)
	}
	if len(hc.CapAdd) != 1 || hc.CapAdd[0] != "SYS_ADMIN" {
		t.Errorf("CapAdd = %v, want [SYS_ADMIN]", hc.CapAdd)
	}
}

func TestBuildHostConfig_DefaultsNoNewPrivileges(t *testing.T) {
	r := &Runtime{pidsLimit: defaultPidsLimit}
	hc := r.buildHostConfig(1<<30, 1<<30, 1.0)
	found := false
	for _, opt := range hc.SecurityOpt {
		if opt == "no-new-privileges:true" {
			found = true
		}
	}
	if !found {
		t.Errorf("SecurityOpt missing no-new-privileges:true: %v", hc.SecurityOpt)
	}
}

func TestBuildHostConfig_DefaultsPidsLimit(t *testing.T) {
	r := &Runtime{pidsLimit: defaultPidsLimit}
	hc := r.buildHostConfig(1<<30, 1<<30, 1.0)
	if hc.PidsLimit == nil || *hc.PidsLimit != defaultPidsLimit {
		t.Errorf("PidsLimit = %v, want %d", hc.PidsLimit, defaultPidsLimit)
	}
}

func TestBuildHostConfig_CustomPidsLimit(t *testing.T) {
	r := &Runtime{pidsLimit: 256}
	hc := r.buildHostConfig(1<<30, 1<<30, 1.0)
	if hc.PidsLimit == nil || *hc.PidsLimit != 256 {
		t.Errorf("PidsLimit = %v, want 256", hc.PidsLimit)
	}
}

func TestBuildHostConfig_SeccompProfile(t *testing.T) {
	r := &Runtime{pidsLimit: defaultPidsLimit, seccompProfile: "/etc/helmdeck/chrome.json"}
	hc := r.buildHostConfig(1<<30, 1<<30, 1.0)
	found := false
	for _, opt := range hc.SecurityOpt {
		if strings.HasPrefix(opt, "seccomp=") && strings.Contains(opt, "chrome.json") {
			found = true
		}
	}
	if !found {
		t.Errorf("SecurityOpt missing seccomp=<profile>: %v", hc.SecurityOpt)
	}
}

func TestBuildHostConfig_NoSeccompFallbackToDefault(t *testing.T) {
	// Empty seccompProfile MUST mean "use docker's default profile"
	// — i.e. we omit the seccomp= entry entirely. Docker applies
	// its built-in profile when no override is set.
	r := &Runtime{pidsLimit: defaultPidsLimit}
	hc := r.buildHostConfig(1<<30, 1<<30, 1.0)
	for _, opt := range hc.SecurityOpt {
		if strings.HasPrefix(opt, "seccomp=") {
			t.Errorf("empty seccompProfile should NOT add a seccomp= entry, got %q", opt)
		}
	}
}

func TestBuildHostConfig_NetworkAttachedWhenSet(t *testing.T) {
	r := &Runtime{pidsLimit: defaultPidsLimit, network: "baas-net"}
	hc := r.buildHostConfig(1<<30, 1<<30, 1.0)
	if string(hc.NetworkMode) != "baas-net" {
		t.Errorf("NetworkMode = %q, want baas-net", hc.NetworkMode)
	}
}

func TestBuildHostConfig_ResourceLimitsApplied(t *testing.T) {
	r := &Runtime{pidsLimit: defaultPidsLimit}
	hc := r.buildHostConfig(2<<30, 1<<30, 1.5)
	if hc.Memory != 2<<30 {
		t.Errorf("Memory = %d", hc.Memory)
	}
	if hc.NanoCPUs != int64(1.5*1e9) {
		t.Errorf("NanoCPUs = %d", hc.NanoCPUs)
	}
	if hc.ShmSize != 1<<30 {
		t.Errorf("ShmSize = %d", hc.ShmSize)
	}
}

func TestNew_DefaultsPidsLimit(t *testing.T) {
	r := &Runtime{}
	// Direct field access — calling New() requires a docker daemon
	// because of NewClientWithOpts. Verify that buildHostConfig
	// receives defaultPidsLimit when constructed via the option.
	WithPidsLimit(defaultPidsLimit)(r)
	if r.pidsLimit != defaultPidsLimit {
		t.Errorf("WithPidsLimit didn't apply: %d", r.pidsLimit)
	}
}

func TestWithSeccompProfile(t *testing.T) {
	r := &Runtime{}
	WithSeccompProfile("/path/to/profile.json")(r)
	if r.seccompProfile != "/path/to/profile.json" {
		t.Errorf("WithSeccompProfile didn't apply: %q", r.seccompProfile)
	}
}

// T807a — Playwright MCP endpoint builder. The happy path mirrors
// buildCDPEndpoint (picks the configured network's IP first, falls
// back to DefaultNetworkSettings.IPAddress) and appends /sse because
// @playwright/mcp exposes its SSE transport at that mount point.
// The opt-out env var MUST zero the endpoint so packs can detect
// that the operator disabled Playwright MCP without having to know
// about the sidecar's entrypoint toggle.

func inspectWithNetworkIP(networkName, ip string) container.InspectResponse {
	return container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				networkName: {IPAddress: ip},
			},
		},
	}
}

func TestBuildPlaywrightMCPEndpoint_HappyPath(t *testing.T) {
	insp := inspectWithNetworkIP("baas-net", "172.18.0.42")
	got := buildPlaywrightMCPEndpoint(insp, "baas-net", playwrightMCPPort, nil)
	want := "http://172.18.0.42:8931/sse"
	if got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}
}

func TestBuildPlaywrightMCPEndpoint_OptOutEnvVar(t *testing.T) {
	insp := inspectWithNetworkIP("baas-net", "172.18.0.42")
	got := buildPlaywrightMCPEndpoint(insp, "baas-net", playwrightMCPPort,
		map[string]string{"HELMDECK_PLAYWRIGHT_MCP_ENABLED": "false"})
	if got != "" {
		t.Errorf("opted-out endpoint = %q, want empty string", got)
	}
}

func TestBuildPlaywrightMCPEndpoint_EnvVarTrueStillBuilds(t *testing.T) {
	// Any value that isn't exactly "false" keeps Playwright MCP on so
	// a typo in HELMDECK_PLAYWRIGHT_MCP_ENABLED doesn't silently
	// disable the endpoint.
	insp := inspectWithNetworkIP("baas-net", "172.18.0.42")
	got := buildPlaywrightMCPEndpoint(insp, "baas-net", playwrightMCPPort,
		map[string]string{"HELMDECK_PLAYWRIGHT_MCP_ENABLED": "true"})
	if got != "http://172.18.0.42:8931/sse" {
		t.Errorf("endpoint = %q, want http://172.18.0.42:8931/sse", got)
	}
}

func TestBuildPlaywrightMCPEndpoint_NoIP(t *testing.T) {
	// No usable IP → no endpoint. Same behavior as buildCDPEndpoint.
	insp := container.InspectResponse{NetworkSettings: &container.NetworkSettings{}}
	got := buildPlaywrightMCPEndpoint(insp, "baas-net", playwrightMCPPort, nil)
	if got != "" {
		t.Errorf("endpoint with no IP = %q, want empty string", got)
	}
}
