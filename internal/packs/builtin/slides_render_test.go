package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// fakeExecutor records every Exec call and replays a scripted result.
// We deliberately implement session.Executor (not Runtime) because the
// engine takes the executor through a separate option, and slides
// tests don't need any session.Runtime methods.
//
// dispatch lets a test return different results per binary (cmd[0]).
// When set, it takes precedence over the static result/err fields.
// Used by the mermaid pre-processing tests which need to script
// distinct outputs for mmdc and marp.
type fakeExecutor struct {
	last     session.ExecRequest
	allCmds  [][]string
	calls    int
	result   session.ExecResult
	err      error
	dispatch func(req session.ExecRequest) (session.ExecResult, error)
}

func (f *fakeExecutor) Exec(ctx context.Context, id string, req session.ExecRequest) (session.ExecResult, error) {
	f.calls++
	f.last = req
	f.allCmds = append(f.allCmds, append([]string(nil), req.Cmd...))
	if f.dispatch != nil {
		return f.dispatch(req)
	}
	if f.err != nil {
		return session.ExecResult{}, f.err
	}
	return f.result, nil
}

func newSlidesEngine(t *testing.T, ex *fakeExecutor) *packs.Engine {
	t.Helper()
	return packs.New(
		packs.WithRuntime(fakeRuntime{}),
		packs.WithSessionExecutor(ex),
	)
}

func TestSlidesRenderHappyPathPDF(t *testing.T) {
	pdfBytes := []byte("%PDF-1.7 fake")
	ex := &fakeExecutor{result: session.ExecResult{Stdout: pdfBytes}}
	eng := newSlidesEngine(t, ex)

	res, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":"# hi","format":"pdf"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ex.calls != 1 {
		t.Errorf("calls = %d", ex.calls)
	}
	// stdin must be the markdown verbatim
	if string(ex.last.Stdin) != "# hi" {
		t.Errorf("stdin = %q", ex.last.Stdin)
	}
	// command must include marp + the requested format flag
	cmd := ex.last.Cmd
	if len(cmd) == 0 || cmd[0] != "marp" {
		t.Fatalf("cmd = %v", cmd)
	}
	foundFlag := false
	for _, a := range cmd {
		if a == "--pdf" {
			foundFlag = true
		}
	}
	if !foundFlag {
		t.Errorf("cmd missing --pdf: %v", cmd)
	}
	var out struct {
		Format      string `json:"format"`
		ArtifactKey string `json:"artifact_key"`
		Size        int    `json:"size"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Format != "pdf" || out.Size != len(pdfBytes) {
		t.Errorf("output = %+v", out)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].ContentType != "application/pdf" {
		t.Errorf("artifacts = %+v", res.Artifacts)
	}
}

func TestSlidesRenderFormatSelection(t *testing.T) {
	cases := map[string]struct {
		flag string
		ext  string
		mime string
	}{
		"pdf":  {"--pdf", "pdf", "application/pdf"},
		"pptx": {"--pptx", "pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		"html": {"--html", "html", "text/html"},
	}
	for format, want := range cases {
		t.Run(format, func(t *testing.T) {
			ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("data")}}
			eng := newSlidesEngine(t, ex)
			body := `{"markdown":"# hi","format":"` + format + `"}`
			res, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(body))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			found := false
			for _, a := range ex.last.Cmd {
				if a == want.flag {
					found = true
				}
			}
			if !found {
				t.Errorf("flag %q not in cmd %v", want.flag, ex.last.Cmd)
			}
			if res.Artifacts[0].ContentType != want.mime {
				t.Errorf("mime = %q want %q", res.Artifacts[0].ContentType, want.mime)
			}
		})
	}
}

func TestSlidesRenderDefaultsToPDF(t *testing.T) {
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("x")}}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":"# hi"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, a := range ex.last.Cmd {
		if a == "--pdf" {
			return
		}
	}
	t.Errorf("default did not select --pdf: %v", ex.last.Cmd)
}

func TestSlidesRenderUnsupportedFormat(t *testing.T) {
	ex := &fakeExecutor{}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":"# hi","format":"docx"}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeInvalidInput {
		t.Errorf("err = %v, want CodeInvalidInput", err)
	}
	if ex.calls != 0 {
		t.Errorf("executor should not run on bad format: %d calls", ex.calls)
	}
}

func TestSlidesRenderEmptyMarkdown(t *testing.T) {
	ex := &fakeExecutor{}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":""}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeInvalidInput {
		t.Errorf("err = %v, want CodeInvalidInput", err)
	}
}

func TestSlidesRenderMarpFailure(t *testing.T) {
	ex := &fakeExecutor{result: session.ExecResult{ExitCode: 1, Stderr: []byte("syntax error on line 3")}}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":"# x"}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeHandlerFailed {
		t.Errorf("err = %v, want CodeHandlerFailed", err)
	}
}

func TestSlidesRenderEmptyOutput(t *testing.T) {
	ex := &fakeExecutor{result: session.ExecResult{ExitCode: 0, Stdout: nil}}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":"# x"}`))
	if err == nil {
		t.Fatal("expected error on empty stdout")
	}
}

func TestSlidesRender_MermaidFencePreprocessed(t *testing.T) {
	// A deck with a ```mermaid block should trigger an mmdc exec before
	// the marp exec, and the markdown piped to marp should carry an
	// inline-SVG <img data:image/svg+xml;base64,...> in place of the
	// fence.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><g/></svg>`)
	ex := &fakeExecutor{
		dispatch: func(req session.ExecRequest) (session.ExecResult, error) {
			if len(req.Cmd) > 0 && req.Cmd[0] == "marp" {
				return session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}, nil
			}
			// mmdc path (sh -c '... mmdc ...')
			return session.ExecResult{Stdout: svg}, nil
		},
	}
	eng := newSlidesEngine(t, ex)
	body := "# Slide 1\n\n```mermaid\ngraph TD; A-->B;\n```\n\n---\n\n# Slide 2"
	input, _ := json.Marshal(map[string]any{"markdown": body, "format": "pdf"})
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ex.calls != 2 {
		t.Errorf("expected 2 execs (1 mmdc + 1 marp), got %d", ex.calls)
	}
	if len(ex.allCmds) < 2 || ex.allCmds[0][0] != "sh" {
		t.Errorf("first exec should be the mmdc sh wrapper, got %v", ex.allCmds)
	}
	if ex.last.Cmd[0] != "marp" {
		t.Errorf("last exec should be marp, got %v", ex.last.Cmd)
	}
	piped := string(ex.last.Stdin)
	if strings.Contains(piped, "```mermaid") {
		t.Errorf("markdown piped to marp should no longer contain ```mermaid fence:\n%s", piped)
	}
	if !strings.Contains(piped, `<img src="data:image/svg+xml;base64,`) {
		t.Errorf("markdown piped to marp should contain inline-SVG <img> data-URI:\n%s", piped)
	}
}

func TestSlidesRender_MermaidOptOut(t *testing.T) {
	// mermaid:false skips pre-processing — only marp exec happens, and
	// the original fence flows through unchanged.
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}}
	eng := newSlidesEngine(t, ex)
	body := "# Slide\n\n```mermaid\ngraph TD; A-->B;\n```"
	input, _ := json.Marshal(map[string]any{
		"markdown": body, "format": "pdf", "mermaid": false,
	})
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ex.calls != 1 {
		t.Errorf("expected 1 exec (marp only), got %d", ex.calls)
	}
	if !strings.Contains(string(ex.last.Stdin), "```mermaid") {
		t.Errorf("with mermaid:false the fence should pass through verbatim:\n%s", ex.last.Stdin)
	}
}

func TestSlidesRender_NoMermaidFenceSkipsMmdc(t *testing.T) {
	// Deck without any mermaid blocks should not invoke mmdc even with
	// default-on mermaid pre-processing.
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":"# Slide\n\nNo diagrams here.","format":"pdf"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ex.calls != 1 {
		t.Errorf("expected 1 exec (marp only — no mermaid pre-pass), got %d", ex.calls)
	}
	if ex.last.Cmd[0] != "marp" {
		t.Errorf("expected marp, got %v", ex.last.Cmd)
	}
}

func TestSlidesRender_MermaidFailureSurfacesSource(t *testing.T) {
	// When mmdc fails (bad mermaid syntax), the error should carry the
	// diagram source so authors can see what they wrote.
	ex := &fakeExecutor{
		dispatch: func(req session.ExecRequest) (session.ExecResult, error) {
			return session.ExecResult{
				ExitCode: 1,
				Stderr:   []byte("Parse error on line 1: graphh TD; A-->B;"),
			}, nil
		},
	}
	eng := newSlidesEngine(t, ex)
	body := "# Slide\n\n```mermaid\ngraphh TD; A-->B;\n```"
	input, _ := json.Marshal(map[string]any{"markdown": body, "format": "pdf"})
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), input)
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeHandlerFailed {
		t.Fatalf("err = %v, want CodeHandlerFailed", err)
	}
	if !strings.Contains(perr.Message, "graphh TD; A-->B;") {
		t.Errorf("error should include diagram source for debugging; got: %s", perr.Message)
	}
	if !strings.Contains(perr.Message, "Parse error") {
		t.Errorf("error should include mmdc stderr; got: %s", perr.Message)
	}
}

func TestSlidesRender_MultipleMermaidFences(t *testing.T) {
	// Two ```mermaid blocks → two mmdc execs (in order) → one marp.
	ex := &fakeExecutor{
		dispatch: func(req session.ExecRequest) (session.ExecResult, error) {
			if len(req.Cmd) > 0 && req.Cmd[0] == "marp" {
				return session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}, nil
			}
			return session.ExecResult{Stdout: []byte(`<svg/>`)}, nil
		},
	}
	eng := newSlidesEngine(t, ex)
	body := "# A\n\n```mermaid\ngraph TD; A-->B;\n```\n\n# C\n\n```mermaid\nsequenceDiagram; A->>B: msg\n```"
	input, _ := json.Marshal(map[string]any{"markdown": body, "format": "pdf"})
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ex.calls != 3 {
		t.Errorf("expected 3 execs (2 mmdc + 1 marp), got %d", ex.calls)
	}
	piped := string(ex.last.Stdin)
	if strings.Count(piped, `<img src="data:image/svg+xml;base64,`) != 2 {
		t.Errorf("expected 2 inline-SVG <img> tags in marp input:\n%s", piped)
	}
}

func TestSlidesRender_HeroImagePrependedToDeck(t *testing.T) {
	// hero_image_prompt → RunImageGen (HTTP to fal.ai stub) → base64
	// inline of PNG bytes prepended after frontmatter, before slide 1.
	// The markdown piped to marp must contain a data:image/png;base64,
	// substring AND must NOT call mmdc (no mermaid in the input).
	stubFalAPI(t, "sk_fal", 1)
	v := vaultWithFalKey(t, "sk_fal")
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}}
	eng := newSlidesEngine(t, ex)

	body := "---\nmarp: true\ntheme: gaia\n---\n\n# First slide\n\nHello."
	input, _ := json.Marshal(map[string]any{
		"markdown":          body,
		"format":            "pdf",
		"hero_image_prompt": "abstract gradient cover",
	})
	raw, err := eng.Execute(context.Background(), SlidesRender(v, nil), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ex.calls != 1 {
		t.Errorf("expected 1 exec (marp only — hero image is HTTP, not session exec), got %d", ex.calls)
	}
	if ex.last.Cmd[0] != "marp" {
		t.Errorf("expected marp, got %v", ex.last.Cmd)
	}
	piped := string(ex.last.Stdin)
	if !strings.Contains(piped, `<img src="data:image/png;base64,`) {
		t.Errorf("hero image should be base64-inlined into markdown:\n%s", piped)
	}
	// Hero block should land AFTER the frontmatter close, BEFORE slide 1.
	fmEnd := strings.Index(piped, "\n---\n") + len("\n---\n")
	firstSlide := strings.Index(piped[fmEnd:], "# First slide")
	heroPos := strings.Index(piped[fmEnd:], `<img src="data:image/png;base64,`)
	if heroPos < 0 || firstSlide < 0 || heroPos > firstSlide {
		t.Errorf("hero should land AFTER frontmatter, BEFORE first slide; got heroPos=%d firstSlide=%d in:\n%s", heroPos, firstSlide, piped[fmEnd:fmEnd+200])
	}

	// Output should include hero_image_model_used.
	var out struct {
		HeroImageModelUsed string `json:"hero_image_model_used"`
	}
	_ = json.Unmarshal(raw.Output, &out)
	if out.HeroImageModelUsed != imageGenDefaultModel {
		t.Errorf("hero_image_model_used = %q, want %q", out.HeroImageModelUsed, imageGenDefaultModel)
	}
}

func TestSlidesRender_HeroImageEmptyPromptSkipsImageGen(t *testing.T) {
	// Empty hero_image_prompt → no fal.ai call. No vault needed.
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}}
	eng := newSlidesEngine(t, ex)
	input, _ := json.Marshal(map[string]any{
		"markdown":          "# Slide",
		"format":            "pdf",
		"hero_image_prompt": "",
	})
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(string(ex.last.Stdin), `data:image/png;base64,`) {
		t.Errorf("empty hero_image_prompt should not produce a data-URI image:\n%s", ex.last.Stdin)
	}
}

func TestSlidesRender_HeroImageNoFrontmatterPrepends(t *testing.T) {
	// Deck with no `---` frontmatter: hero block prepends to the
	// markdown directly (no anchor to insert after).
	stubFalAPI(t, "sk_fal", 1)
	v := vaultWithFalKey(t, "sk_fal")
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}}
	eng := newSlidesEngine(t, ex)
	input, _ := json.Marshal(map[string]any{
		"markdown":          "# Lead slide\n\nNo frontmatter here.",
		"format":            "pdf",
		"hero_image_prompt": "minimal gradient",
	})
	_, err := eng.Execute(context.Background(), SlidesRender(v, nil), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	piped := string(ex.last.Stdin)
	// Image must come BEFORE the `# Lead slide` heading.
	imgIdx := strings.Index(piped, `<img src="data:image/png;base64,`)
	leadIdx := strings.Index(piped, "# Lead slide")
	if imgIdx < 0 || leadIdx < 0 || imgIdx > leadIdx {
		t.Errorf("hero should land before lead slide when no frontmatter; got imgIdx=%d leadIdx=%d", imgIdx, leadIdx)
	}
}

func TestSlidesRender_HeroImageRespectsExplicitModel(t *testing.T) {
	stubFalAPI(t, "sk_fal", 1)
	v := vaultWithFalKey(t, "sk_fal")
	ex := &fakeExecutor{result: session.ExecResult{Stdout: []byte("%PDF-1.7 fake")}}
	eng := newSlidesEngine(t, ex)
	input, _ := json.Marshal(map[string]any{
		"markdown":          "# slide",
		"format":            "pdf",
		"hero_image_prompt": "cover",
		"hero_image_model":  "fal-ai/flux/dev",
	})
	raw, err := eng.Execute(context.Background(), SlidesRender(v, nil), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		HeroImageModelUsed string `json:"hero_image_model_used"`
	}
	_ = json.Unmarshal(raw.Output, &out)
	if out.HeroImageModelUsed != "fal-ai/flux/dev" {
		t.Errorf("model = %q, want fal-ai/flux/dev", out.HeroImageModelUsed)
	}
}

func TestSlidesRender_HeroImageNoCredentialFailsLoud(t *testing.T) {
	// hero_image_prompt set but no fal-key in vault and no env var.
	// Pack should hard-fail (consistent with #138 / image_generate
	// behavior) rather than silently render without the hero.
	v := vaultWithFalKey(t, "") // empty key → no credential seeded
	ex := &fakeExecutor{}
	eng := newSlidesEngine(t, ex)
	input, _ := json.Marshal(map[string]any{
		"markdown":          "# slide",
		"format":            "pdf",
		"hero_image_prompt": "cover",
	})
	_, err := eng.Execute(context.Background(), SlidesRender(v, nil), input)
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeInvalidInput {
		t.Errorf("err = %v, want CodeInvalidInput on missing fal-key", err)
	}
	if ex.calls != 0 {
		t.Errorf("marp should not run if hero image generation failed: %d execs", ex.calls)
	}
}

func TestSlidesRenderNoExecutor(t *testing.T) {
	// Engine has runtime but no executor: handler must surface
	// session_unavailable instead of nil-deref on ec.Exec.
	eng := packs.New(packs.WithRuntime(fakeRuntime{}))
	_, err := eng.Execute(context.Background(), SlidesRender(nil, nil), json.RawMessage(`{"markdown":"# x"}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeSessionUnavailable {
		t.Errorf("err = %v, want CodeSessionUnavailable", err)
	}
}
