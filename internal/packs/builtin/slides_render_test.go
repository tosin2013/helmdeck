package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// fakeExecutor records every Exec call and replays a scripted result.
// We deliberately implement session.Executor (not Runtime) because the
// engine takes the executor through a separate option, and slides
// tests don't need any session.Runtime methods.
type fakeExecutor struct {
	last   session.ExecRequest
	calls  int
	result session.ExecResult
	err    error
}

func (f *fakeExecutor) Exec(ctx context.Context, id string, req session.ExecRequest) (session.ExecResult, error) {
	f.calls++
	f.last = req
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

	res, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(`{"markdown":"# hi","format":"pdf"}`))
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
			res, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(body))
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
	_, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(`{"markdown":"# hi"}`))
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
	_, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(`{"markdown":"# hi","format":"docx"}`))
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
	_, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(`{"markdown":""}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeInvalidInput {
		t.Errorf("err = %v, want CodeInvalidInput", err)
	}
}

func TestSlidesRenderMarpFailure(t *testing.T) {
	ex := &fakeExecutor{result: session.ExecResult{ExitCode: 1, Stderr: []byte("syntax error on line 3")}}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(`{"markdown":"# x"}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeHandlerFailed {
		t.Errorf("err = %v, want CodeHandlerFailed", err)
	}
}

func TestSlidesRenderEmptyOutput(t *testing.T) {
	ex := &fakeExecutor{result: session.ExecResult{ExitCode: 0, Stdout: nil}}
	eng := newSlidesEngine(t, ex)
	_, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(`{"markdown":"# x"}`))
	if err == nil {
		t.Fatal("expected error on empty stdout")
	}
}

func TestSlidesRenderNoExecutor(t *testing.T) {
	// Engine has runtime but no executor: handler must surface
	// session_unavailable instead of nil-deref on ec.Exec.
	eng := packs.New(packs.WithRuntime(fakeRuntime{}))
	_, err := eng.Execute(context.Background(), SlidesRender(), json.RawMessage(`{"markdown":"# x"}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeSessionUnavailable {
		t.Errorf("err = %v, want CodeSessionUnavailable", err)
	}
}
