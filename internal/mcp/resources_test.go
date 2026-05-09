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
	if len(got.Result.Resources) != 1 {
		t.Fatalf("want 1 resource (packs only), got %d: %s", len(got.Result.Resources), resp)
	}
	if got.Result.Resources[0].URI != "helmdeck://packs" {
		t.Errorf("want URI helmdeck://packs, got %q", got.Result.Resources[0].URI)
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
