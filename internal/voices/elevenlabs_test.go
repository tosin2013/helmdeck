// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package voices

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubElevenLabs returns an httptest server that responds to
// /v1/voices with the given body when the xi-api-key header matches
// `wantKey`. Returns 401 otherwise. Test redirects ElevenLabsBaseURL
// to the stub URL via the package var override.
func stubElevenLabs(t *testing.T, wantKey, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/voices") {
			http.Error(w, "unexpected path", 404)
			return
		}
		if r.Header.Get("xi-api-key") != wantKey {
			http.Error(w, "bad key", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prev := ElevenLabsBaseURL
	ElevenLabsBaseURL = srv.URL
	t.Cleanup(func() { ElevenLabsBaseURL = prev })
	return srv
}

func TestListVoices_HappyPath(t *testing.T) {
	stubElevenLabs(t, "sk_test", `{"voices":[
		{"voice_id":"v1","name":"Rachel","labels":{"accent":"american","gender":"female","use_case":"narration"},"preview_url":"https://example.com/r.mp3"},
		{"voice_id":"v2","name":"Adam","labels":{"accent":"british","gender":"male"},"preview_url":""}
	]}`)
	got, err := ListVoices(context.Background(), "sk_test")
	if err != nil {
		t.Fatalf("ListVoices: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].VoiceID != "v1" || got[0].Name != "Rachel" || got[0].Source != "elevenlabs" {
		t.Errorf("voice[0] = %+v", got[0])
	}
	if got[0].Labels["accent"] != "american" {
		t.Errorf("labels not parsed: %+v", got[0].Labels)
	}
	if got[0].PreviewURL != "https://example.com/r.mp3" {
		t.Errorf("preview_url = %q", got[0].PreviewURL)
	}
}

func TestListVoices_BadKey_PropagatesStatus(t *testing.T) {
	stubElevenLabs(t, "sk_correct", `{"voices":[]}`)
	_, err := ListVoices(context.Background(), "sk_wrong")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("want 401-bearing error, got %v", err)
	}
}

func TestListVoices_EmptyKeyRejected(t *testing.T) {
	_, err := ListVoices(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "api key is required") {
		t.Errorf("want api-key-required error, got %v", err)
	}
}

func TestListVoices_EmptyAccountIsNotAnError(t *testing.T) {
	stubElevenLabs(t, "sk_empty", `{"voices":[]}`)
	got, err := ListVoices(context.Background(), "sk_empty")
	if err != nil {
		t.Fatalf("ListVoices: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}
