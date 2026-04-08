package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/tosin2013/helmdeck/internal/cdp"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// scrapeFakeClient is a per-selector-controllable cdp.Client. The
// fake.Client in internal/cdp/fake serves a single ExtractText for
// every selector, which can't drive partial-result tests.
type scrapeFakeClient struct {
	NavigateURL  string
	NavigateErr  error
	Texts        map[string]string // selector -> value
	ExtractCalls []string
}

func (s *scrapeFakeClient) Navigate(_ context.Context, url string) error {
	s.NavigateURL = url
	return s.NavigateErr
}
func (s *scrapeFakeClient) Extract(_ context.Context, selector string, _ cdp.Format) (string, error) {
	s.ExtractCalls = append(s.ExtractCalls, selector)
	if v, ok := s.Texts[selector]; ok {
		return v, nil
	}
	return "", errors.New("selector miss: " + selector)
}
func (s *scrapeFakeClient) Screenshot(_ context.Context, _ bool) ([]byte, error) { return nil, nil }
func (s *scrapeFakeClient) Execute(_ context.Context, _ string) (any, error)     { return nil, nil }
func (s *scrapeFakeClient) Interact(_ context.Context, _ cdp.InteractAction, _, _ string) error {
	return nil
}
func (s *scrapeFakeClient) Close() error { return nil }

type scrapeFactory struct {
	client *scrapeFakeClient
}

func (f *scrapeFactory) Get(ctx context.Context, id string) (cdp.Client, error) {
	return f.client, nil
}
func (f *scrapeFactory) Evict(id string) {}

func newScrapeEngine(t *testing.T, fc *scrapeFactory) *packs.Engine {
	t.Helper()
	return packs.New(
		packs.WithRuntime(fakeRuntime{}),
		packs.WithCDPFactory(fc),
	)
}

func TestScrapeSPAHappyPath(t *testing.T) {
	fc := &scrapeFactory{client: &scrapeFakeClient{Texts: map[string]string{
		"h1":      "Welcome",
		"article": "Body content here",
	}}}
	eng := newScrapeEngine(t, fc)
	in := json.RawMessage(`{
		"url":"https://app.example.com",
		"fields":{
			"title":{"selector":"h1","format":"text"},
			"body":{"selector":"article","format":"html"}
		}
	}`)
	res, err := eng.Execute(context.Background(), ScrapeSPA(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		URL     string            `json:"url"`
		Data    map[string]string `json:"data"`
		Missing []string          `json:"missing"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Data["title"] != "Welcome" || out.Data["body"] != "Body content here" {
		t.Errorf("data = %+v", out.Data)
	}
	if len(out.Missing) != 0 {
		t.Errorf("missing = %v, want empty", out.Missing)
	}
	if fc.client.NavigateURL != "https://app.example.com" {
		t.Errorf("navigate = %q", fc.client.NavigateURL)
	}
}

func TestScrapeSPAPartialResult(t *testing.T) {
	// Two of three selectors hit; the pack must return data for the
	// hits and list the misses without raising an error.
	fc := &scrapeFactory{client: &scrapeFakeClient{Texts: map[string]string{
		"h1":     "ok",
		"footer": "ok",
	}}}
	eng := newScrapeEngine(t, fc)
	in := json.RawMessage(`{
		"url":"https://x",
		"fields":{
			"title":{"selector":"h1"},
			"body":{"selector":"article"},
			"foot":{"selector":"footer"}
		}
	}`)
	res, err := eng.Execute(context.Background(), ScrapeSPA(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Data    map[string]string `json:"data"`
		Missing []string          `json:"missing"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if len(out.Data) != 2 {
		t.Errorf("data = %+v", out.Data)
	}
	if len(out.Missing) != 1 || out.Missing[0] != "body" {
		t.Errorf("missing = %v", out.Missing)
	}
	// Stable sort check: build expected ordering and compare.
	expected := append([]string{}, out.Missing...)
	sort.Strings(expected)
	for i := range expected {
		if expected[i] != out.Missing[i] {
			t.Errorf("missing not sorted: %v", out.Missing)
		}
	}
}

func TestScrapeSPATotalMissFails(t *testing.T) {
	fc := &scrapeFactory{client: &scrapeFakeClient{Texts: map[string]string{}}}
	eng := newScrapeEngine(t, fc)
	in := json.RawMessage(`{
		"url":"https://x",
		"fields":{"a":{"selector":"#a"},"b":{"selector":"#b"}}
	}`)
	_, err := eng.Execute(context.Background(), ScrapeSPA(), in)
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeHandlerFailed {
		t.Errorf("err = %v, want CodeHandlerFailed", err)
	}
}

func TestScrapeSPARejectsBadInput(t *testing.T) {
	fc := &scrapeFactory{client: &scrapeFakeClient{}}
	eng := newScrapeEngine(t, fc)
	cases := map[string]string{
		"missing url":      `{"fields":{"a":{"selector":"#a"}}}`,
		"missing fields":   `{"url":"https://x"}`,
		"empty fields":     `{"url":"https://x","fields":{}}`,
		"empty selector":   `{"url":"https://x","fields":{"a":{"selector":""}}}`,
		"wrong url type":   `{"url":123,"fields":{"a":{"selector":"#a"}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := eng.Execute(context.Background(), ScrapeSPA(), json.RawMessage(body))
			var perr *packs.PackError
			if !errors.As(err, &perr) || perr.Code != packs.CodeInvalidInput {
				t.Errorf("err = %v, want CodeInvalidInput", err)
			}
		})
	}
}

func TestScrapeSPANavigateError(t *testing.T) {
	fc := &scrapeFactory{client: &scrapeFakeClient{NavigateErr: errors.New("dns down")}}
	eng := newScrapeEngine(t, fc)
	_, err := eng.Execute(context.Background(), ScrapeSPA(), json.RawMessage(`{"url":"https://x","fields":{"a":{"selector":"#a"}}}`))
	if err == nil {
		t.Fatal("expected error")
	}
}
