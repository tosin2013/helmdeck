package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// fakeSessionLister returns a fixed list (and optionally an error) for
// the helmdeck://sessions resource — keeps tests independent of any
// real session.Runtime backend.
type fakeSessionLister struct {
	out []SessionView
	err error
}

func (f fakeSessionLister) List(ctx context.Context) ([]SessionView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

// startServerWithOpts spins up a PackServer over in-memory pipes with
// the supplied options. Mirrors startPackServerScanner but threads
// PackServerOptions through so tests can wire a session lister.
func startServerWithOpts(t *testing.T, opts ...PackServerOption) (write func(string), read func() string, stop func()) {
	t.Helper()
	reg := packs.NewPackRegistry()
	srv := NewPackServer(reg, nil, opts...)
	clientToServer, fromClient := io.Pipe()
	fromServer, serverToClient := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, clientToServer, serverToClient)
	}()
	sc := bufio.NewScanner(fromServer)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	write = func(line string) {
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		if _, err := fromClient.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	read = func() string {
		if !sc.Scan() {
			t.Fatal("server closed before response")
		}
		return sc.Text()
	}
	stop = func() {
		cancel()
		_ = fromClient.Close()
		_ = serverToClient.Close()
		<-done
	}
	return
}

func TestResourcesList_PacksOnly_WhenNoSessionLister(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()

	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// helmdeck://packs / routing-guide / my-defaults / my-memory are
	// always present (ADR 047 + ADR 048 — unconditional). The optional
	// resources (sessions/voices/image-models/models) are absent here.
	if len(got.Result.Resources) != 4 {
		t.Fatalf("want 4 resources (packs + routing-guide + my-defaults + my-memory), got %d: %s", len(got.Result.Resources), resp)
	}
	want := map[string]bool{
		"helmdeck://packs":         false,
		"helmdeck://routing-guide": false,
		"helmdeck://my-defaults":   false,
		"helmdeck://my-memory":     false,
	}
	for _, r := range got.Result.Resources {
		if _, ok := want[r.URI]; ok {
			want[r.URI] = true
		} else {
			t.Errorf("unexpected resource %q", r.URI)
		}
	}
	for uri, seen := range want {
		if !seen {
			t.Errorf("missing expected resource %q", uri)
		}
	}
}

func TestResourcesList_IncludesSessions_WhenListerWired(t *testing.T) {
	lister := fakeSessionLister{out: []SessionView{{ID: "abc", Status: "running"}}}
	write, read, stop := startServerWithOpts(t, WithSessions(lister))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()

	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	uris := make([]string, len(got.Result.Resources))
	for i, r := range got.Result.Resources {
		uris[i] = r.URI
	}
	wantSet := map[string]bool{"helmdeck://packs": false, "helmdeck://sessions": false}
	for _, u := range uris {
		if _, ok := wantSet[u]; ok {
			wantSet[u] = true
		}
	}
	for u, found := range wantSet {
		if !found {
			t.Errorf("resources/list missing %s; got %v", u, uris)
		}
	}
}

func TestResourcesRead_Sessions_HappyPath(t *testing.T) {
	lister := fakeSessionLister{out: []SessionView{
		{ID: "s1", Status: "running", Image: "ghcr.io/tosin2013/helmdeck-sidecar:0.10.2"},
		{ID: "s2", Status: "terminated"},
	}}
	write, read, stop := startServerWithOpts(t, WithSessions(lister))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://sessions"}}`)
	resp := read()

	var got struct {
		Result struct {
			Contents []ResourceContent `json:"contents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Result.Contents) != 1 {
		t.Fatalf("want 1 content block, got %d", len(got.Result.Contents))
	}
	if got.Result.Contents[0].URI != "helmdeck://sessions" {
		t.Errorf("URI mismatch: %q", got.Result.Contents[0].URI)
	}
	if got.Result.Contents[0].MimeType != "application/json" {
		t.Errorf("mime type mismatch: %q", got.Result.Contents[0].MimeType)
	}
	var sessions []SessionView
	if err := json.Unmarshal([]byte(got.Result.Contents[0].Text), &sessions); err != nil {
		t.Fatalf("unmarshal sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("want 2 sessions, got %d", len(sessions))
	}
	if sessions[0].ID != "s1" || sessions[0].Image != "ghcr.io/tosin2013/helmdeck-sidecar:0.10.2" {
		t.Errorf("session 0 mismatch: %+v", sessions[0])
	}
}

func TestResourcesRead_Sessions_UnavailableWhenNoLister(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://sessions"}}`)
	resp := read()

	var got struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatal("want rpc error, got nil")
	}
	if !strings.Contains(got.Error.Message, "session runtime not wired") {
		t.Errorf("unexpected error message: %q", got.Error.Message)
	}
}

func TestResourcesRead_UnknownURI(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://unknown"}}`)
	resp := read()

	var got struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil {
		t.Fatal("want rpc error, got nil")
	}
	if !strings.Contains(got.Error.Message, "unknown resource URI") {
		t.Errorf("unexpected error message: %q", got.Error.Message)
	}
}

func TestResourcesRead_PropagatesListerError(t *testing.T) {
	lister := fakeSessionLister{err: errors.New("backend down")}
	write, read, stop := startServerWithOpts(t, WithSessions(lister))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://sessions"}}`)
	resp := read()

	var got struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "backend down") {
		t.Errorf("want lister error to surface, got %+v", got.Error)
	}
}

// fakeVoiceLister is the test double for the helmdeck://voices resource.
type fakeVoiceLister struct {
	out []VoiceView
	err error
}

func (f fakeVoiceLister) List(ctx context.Context) ([]VoiceView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestResourcesList_IncludesVoices_WhenListerWired(t *testing.T) {
	vl := fakeVoiceLister{out: []VoiceView{{VoiceID: "v1", Name: "Rachel", Source: "elevenlabs"}}}
	write, read, stop := startServerWithOpts(t, WithVoices(vl))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()

	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	uris := make([]string, len(got.Result.Resources))
	for i, r := range got.Result.Resources {
		uris[i] = r.URI
	}
	found := false
	for _, u := range uris {
		if u == "helmdeck://voices" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("resources/list missing helmdeck://voices; got %v", uris)
	}
}

func TestResourcesRead_Voices_HappyPath(t *testing.T) {
	vl := fakeVoiceLister{out: []VoiceView{
		{VoiceID: "v1", Name: "Rachel", Labels: map[string]string{"accent": "american", "gender": "female"}, PreviewURL: "https://example.com/r.mp3", Source: "elevenlabs"},
		{VoiceID: "v2", Name: "Adam", Source: "elevenlabs"},
	}}
	write, read, stop := startServerWithOpts(t, WithVoices(vl))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://voices"}}`)
	resp := read()

	var got struct {
		Result struct {
			Contents []ResourceContent `json:"contents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Result.Contents) != 1 {
		t.Fatalf("want 1 content block, got %d", len(got.Result.Contents))
	}
	if got.Result.Contents[0].URI != "helmdeck://voices" {
		t.Errorf("URI mismatch: %q", got.Result.Contents[0].URI)
	}
	if got.Result.Contents[0].MimeType != "application/json" {
		t.Errorf("mime mismatch: %q", got.Result.Contents[0].MimeType)
	}
	var payload struct {
		Voices    []VoiceView `json:"voices"`
		Engine    string      `json:"engine"`
		FetchedAt string      `json:"fetched_at"`
	}
	if err := json.Unmarshal([]byte(got.Result.Contents[0].Text), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Voices) != 2 {
		t.Errorf("want 2 voices, got %d", len(payload.Voices))
	}
	if payload.Engine != "elevenlabs" {
		t.Errorf("engine = %q, want elevenlabs", payload.Engine)
	}
	if payload.FetchedAt == "" {
		t.Error("fetched_at should be populated")
	}
	if payload.Voices[0].Labels["accent"] != "american" {
		t.Errorf("labels not round-tripped: %+v", payload.Voices[0])
	}
}

func TestResourcesRead_Voices_UnavailableWhenNoLister(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://voices"}}`)
	resp := read()

	var got struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "voice catalog not wired") {
		t.Errorf("expected voice-catalog-not-wired error, got %+v", got.Error)
	}
}

// fakeImageModelLister is the test double for helmdeck://image-models.
type fakeImageModelLister struct {
	out []ImageModelView
	err error
}

func (f fakeImageModelLister) List(ctx context.Context) ([]ImageModelView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestResourcesList_IncludesImageModels_WhenListerWired(t *testing.T) {
	ml := fakeImageModelLister{out: []ImageModelView{
		{ID: "fal-ai/flux/schnell", DisplayName: "FLUX schnell", Provider: "fal-ai", Engine: "fal"},
	}}
	write, read, stop := startServerWithOpts(t, WithImageModels(ml))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()

	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, r := range got.Result.Resources {
		if r.URI == "helmdeck://image-models" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("resources/list missing helmdeck://image-models; got %+v", got.Result.Resources)
	}
}

func TestResourcesRead_ImageModels_HappyPath(t *testing.T) {
	ml := fakeImageModelLister{out: []ImageModelView{
		{
			ID: "fal-ai/flux/schnell", DisplayName: "FLUX schnell", Provider: "fal-ai", Engine: "fal",
			ApproxCostPerImageUSD: 0.003, P50LatencyS: 2,
			SupportsSeed: true, SupportsImageSize: true, MaxResolution: "1024x1024",
			Capabilities: []string{"photorealistic", "fast"},
		},
		{
			ID: "fal-ai/flux-pro/v1.1", DisplayName: "FLUX Pro", Provider: "fal-ai", Engine: "fal",
			ApproxCostPerImageUSD: 0.04, P50LatencyS: 8,
		},
	}}
	write, read, stop := startServerWithOpts(t, WithImageModels(ml))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://image-models"}}`)
	resp := read()

	var got struct {
		Result struct {
			Contents []ResourceContent `json:"contents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Result.Contents) != 1 {
		t.Fatalf("want 1 content, got %d", len(got.Result.Contents))
	}
	var payload struct {
		Models    []ImageModelView `json:"models"`
		Engine    string           `json:"engine"`
		FetchedAt string           `json:"fetched_at"`
	}
	if err := json.Unmarshal([]byte(got.Result.Contents[0].Text), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Models) != 2 {
		t.Errorf("want 2 models, got %d", len(payload.Models))
	}
	if payload.Engine != "fal" {
		t.Errorf("engine = %q, want fal", payload.Engine)
	}
	if payload.FetchedAt == "" {
		t.Error("fetched_at should be populated")
	}
	if payload.Models[0].ApproxCostPerImageUSD != 0.003 {
		t.Errorf("cost not round-tripped: %+v", payload.Models[0])
	}
	if len(payload.Models[0].Capabilities) != 2 {
		t.Errorf("capabilities not round-tripped: %+v", payload.Models[0].Capabilities)
	}
}

func TestResourcesRead_ImageModels_UnavailableWhenNoLister(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://image-models"}}`)
	resp := read()

	var got struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "image-model catalog not wired") {
		t.Errorf("expected catalog-not-wired error, got %+v", got.Error)
	}
}

func TestInitialize_DeclaresResourcesCapability(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	resp := read()

	var got struct {
		Result struct {
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got.Result.Capabilities["resources"]; !ok {
		t.Errorf("initialize missing resources capability: %v", got.Result.Capabilities)
	}
	if _, ok := got.Result.Capabilities["tools"]; !ok {
		t.Errorf("initialize missing tools capability: %v", got.Result.Capabilities)
	}
}

// fakeModelLister is the test double for the helmdeck://models resource.
type fakeModelLister struct {
	out []ModelView
	err error
}

func (f fakeModelLister) List(ctx context.Context) ([]ModelView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestResourcesList_IncludesModels_WhenListerWired(t *testing.T) {
	ml := fakeModelLister{out: []ModelView{{ID: "openrouter/minimax/minimax-m2.7", Provider: "openrouter"}}}
	write, read, stop := startServerWithOpts(t, WithModels(ml))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()

	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, r := range got.Result.Resources {
		if r.URI == "helmdeck://models" {
			found = true
		}
	}
	if !found {
		t.Errorf("resources/list missing helmdeck://models")
	}
}

func TestResourcesRead_Models_HappyPath(t *testing.T) {
	ml := fakeModelLister{out: []ModelView{
		{ID: "openrouter/minimax/minimax-m2.7", Provider: "openrouter"},
		{ID: "anthropic/claude-sonnet-4-6", Provider: "anthropic"},
	}}
	write, read, stop := startServerWithOpts(t, WithModels(ml))
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://models"}}`)
	resp := read()

	var got struct {
		Result struct {
			Contents []ResourceContent `json:"contents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Result.Contents) != 1 || got.Result.Contents[0].URI != "helmdeck://models" {
		t.Fatalf("unexpected contents: %+v", got.Result.Contents)
	}
	var payload struct {
		Models    []ModelView `json:"models"`
		FetchedAt string      `json:"fetched_at"`
	}
	if err := json.Unmarshal([]byte(got.Result.Contents[0].Text), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Models) != 2 {
		t.Errorf("want 2 models, got %d", len(payload.Models))
	}
	if payload.Models[0].ID != "openrouter/minimax/minimax-m2.7" {
		t.Errorf("model id not round-tripped: %+v", payload.Models[0])
	}
	if payload.FetchedAt == "" {
		t.Error("fetched_at should be populated")
	}
}

func TestResourcesRead_Models_NotWired(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()
	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://models"}}`)
	resp := read()
	var got struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32602 {
		t.Errorf("want -32602 when models lister not wired, got %+v", got.Error)
	}
}

// TestResources_RoutingGuide_AlwaysListed — routing-guide is unconditionally
// available (no optional lister). ADR 047 makes it canonical for routing
// decisions, so it must show up in resources/list every time.
func TestResources_RoutingGuide_AlwaysListed(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()

	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, r := range got.Result.Resources {
		if r.URI == "helmdeck://routing-guide" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("helmdeck://routing-guide should always be listed, got %+v", got.Result.Resources)
	}
}

// TestResources_RoutingGuide_Read_Shape — reading the resource returns a
// well-shaped payload: top-level policy block, packs[] (each with name +
// description; metadata only when populated), pipelines[] (empty when no
// pipeline service is wired — additive contract).
func TestResources_RoutingGuide_Read_Shape(t *testing.T) {
	// Register one pack with full metadata so we can assert the metadata
	// surfaces. A second pack with no metadata exercises the
	// collapse-empty-JSON path.
	reg := packs.NewPackRegistry()
	if err := reg.Register(&packs.Pack{
		Name:        "demo.with_meta",
		Version:     "v1",
		Description: "demo pack with metadata",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"markdown"},
			Produces:       []string{"pdf"},
			IntentKeywords: []string{"demo"},
			TypicalUse:     "for tests",
			Limitations:    []string{"toy pack"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&packs.Pack{
		Name:        "demo.no_meta",
		Version:     "v1",
		Description: "demo pack without metadata",
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	srv := NewPackServer(reg, nil)
	clientToServer, fromClient := io.Pipe()
	fromServer, serverToClient := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, clientToServer, serverToClient)
	}()
	defer func() {
		cancel()
		_ = fromClient.Close()
		_ = serverToClient.Close()
		<-done
	}()
	sc := bufio.NewScanner(fromServer)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if _, err := fromClient.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://routing-guide"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if !sc.Scan() {
		t.Fatal("server closed before response")
	}
	resp := sc.Text()

	var rpc struct {
		Result struct {
			Contents []ResourceContent `json:"contents"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &rpc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rpc.Error != nil {
		t.Fatalf("unexpected error: %+v", rpc.Error)
	}
	if len(rpc.Result.Contents) == 0 {
		t.Fatal("expected at least one ResourceContent in response")
	}

	var guide RoutingGuide
	if err := json.Unmarshal([]byte(rpc.Result.Contents[0].Text), &guide); err != nil {
		t.Fatalf("decode guide body: %v", err)
	}
	if !strings.Contains(guide.Policy, "ADR 047") {
		t.Errorf("policy block should mention ADR 047, got: %s", guide.Policy)
	}
	if len(guide.Packs) != 2 {
		t.Fatalf("expected 2 packs (with + without metadata), got %d", len(guide.Packs))
	}
	// Order is registry-defined; find each by name.
	var withMeta, noMeta *routingGuidePackEntry
	for i, p := range guide.Packs {
		switch p.Name {
		case "demo.with_meta":
			withMeta = &guide.Packs[i]
		case "demo.no_meta":
			noMeta = &guide.Packs[i]
		}
	}
	if withMeta == nil || noMeta == nil {
		t.Fatalf("missing pack entries: with=%v no=%v", withMeta, noMeta)
	}
	if len(withMeta.Metadata) == 0 {
		t.Errorf("populated pack should carry metadata in routing-guide; got empty")
	}
	if !strings.Contains(string(withMeta.Metadata), "intent_keywords") {
		t.Errorf("metadata should serialize fields; got %s", withMeta.Metadata)
	}
	if len(noMeta.Metadata) != 0 {
		t.Errorf("unpopulated pack should have nil/empty Metadata (collapse-empty-JSON); got %s", noMeta.Metadata)
	}
}

// TestCollapseEmptyJSON exercises the small helper used by the routing
// guide to keep `{}` / `null` from leaking into the wire shape.
func TestCollapseEmptyJSON(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"{}", ""},
		{"null", ""},
		{`{"x":1}`, `{"x":1}`},
		{`["a"]`, `["a"]`},
	} {
		got := collapseEmptyJSON(json.RawMessage(tc.in))
		if string(got) != tc.want {
			t.Errorf("collapseEmptyJSON(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Verify the errors import is exercised by referencing it once so
	// go vet's "declared and not used" stays quiet across the file.
	_ = errors.New("sentinel")
}

// TestResources_MyDefaults_AlwaysListed — my-defaults resource is
// always present in resources/list (ADR 047 PR #2) regardless of
// whether a memory store is configured.
func TestResources_MyDefaults_AlwaysListed(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()

	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range got.Result.Resources {
		if r.URI == "helmdeck://my-defaults" {
			return
		}
	}
	t.Errorf("helmdeck://my-defaults should always be listed, got %+v", got.Result.Resources)
}

// TestResources_MyDefaults_Read_EmptyWithoutMemory — when no memory
// store is wired, the projection returns a well-shaped empty payload
// with an explanatory note (not an error).
func TestResources_MyDefaults_Read_EmptyWithoutMemory(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://my-defaults"}}`)
	resp := read()

	var rpc struct {
		Result struct {
			Contents []ResourceContent `json:"contents"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &rpc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rpc.Error != nil {
		t.Fatalf("expected success, got error: %+v", rpc.Error)
	}
	var md MyDefaults
	if err := json.Unmarshal([]byte(rpc.Result.Contents[0].Text), &md); err != nil {
		t.Fatalf("decode my-defaults body: %v", err)
	}
	if len(md.Packs) != 0 || len(md.Pipelines) != 0 {
		t.Errorf("want empty projection without memory, got packs=%d pipelines=%d", len(md.Packs), len(md.Pipelines))
	}
	if md.Note == "" {
		t.Errorf("expected an explanatory note in empty projection; got none")
	}
}

// Projection coverage lives in internal/packs/defaults_test.go now —
// the aggregation logic moved there in PR #3 so helmdeck.route can
// reuse it. The MCP wrapper is exercised by the always-listed +
// empty-projection tests above.

// ── ADR 048 PR #2: helmdeck://my-memory ───────────────────────────────

// TestResources_MyMemory_AlwaysListed — like my-defaults, my-memory
// is unconditional. Agents query it at the top of a session to learn
// what facts already exist; an absent resource would force the agent
// to assume nothing.
func TestResources_MyMemory_AlwaysListed(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()
	write(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	resp := read()
	var got struct {
		Result struct {
			Resources []Resource `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range got.Result.Resources {
		if r.URI == "helmdeck://my-memory" {
			return
		}
	}
	t.Errorf("helmdeck://my-memory should always be listed, got %+v", got.Result.Resources)
}

// TestResources_MyMemory_Read_EmptyWithoutMemory — nil store ⇒
// well-shaped empty payload + explanatory note.
func TestResources_MyMemory_Read_EmptyWithoutMemory(t *testing.T) {
	write, read, stop := startServerWithOpts(t)
	defer stop()
	write(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"helmdeck://my-memory"}}`)
	resp := read()
	var rpc struct {
		Result struct {
			Contents []ResourceContent `json:"contents"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(resp), &rpc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rpc.Error != nil {
		t.Fatalf("expected success, got error: %+v", rpc.Error)
	}
	var mm MyMemory
	if err := json.Unmarshal([]byte(rpc.Result.Contents[0].Text), &mm); err != nil {
		t.Fatalf("decode my-memory body: %v", err)
	}
	if len(mm.Categories) != 0 {
		t.Errorf("want empty categories without memory; got %+v", mm.Categories)
	}
	if mm.Note == "" {
		t.Errorf("expected explanatory note in nil-store projection")
	}
}
