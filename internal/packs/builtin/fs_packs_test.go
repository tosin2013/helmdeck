package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// --- safeJoin -----------------------------------------------------------

func TestSafeJoin(t *testing.T) {
	cases := []struct {
		name      string
		clonePath string
		rel       string
		want      string
		wantErr   bool
	}{
		{"happy", "/tmp/helmdeck-clone-X1", "src/main.go", "/tmp/helmdeck-clone-X1/src/main.go", false},
		{"empty rel", "/tmp/helmdeck-clone-X1", "", "/tmp/helmdeck-clone-X1", false},
		{"absolute rel", "/tmp/helmdeck-clone-X1", "/etc/passwd", "", true},
		{"dotdot", "/tmp/helmdeck-clone-X1", "../../etc/passwd", "", true},
		{"backslash", "/tmp/helmdeck-clone-X1", "src\\main.go", "", true},
		{"unsafe clone path", "/etc/passwd", "x", "", true},
		{"home workspace", "/home/helmdeck/work/repo", "src/x.go", "/home/helmdeck/work/repo/src/x.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := safeJoin(tc.clonePath, tc.rel)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- fs.read ------------------------------------------------------------

func newFSEngine(t *testing.T, ex *recordingExecutor) *packs.Engine {
	t.Helper()
	return packs.New(
		packs.WithRuntime(fakeRuntime{}),
		packs.WithSessionExecutor(ex),
	)
}

func TestFSRead_HappyPath(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("12\n"), ExitCode: 0}, // wc -c
		{Stdout: []byte("hello world\n"), ExitCode: 0}, // cat
	}}
	eng := newFSEngine(t, ex)
	res, err := eng.Execute(context.Background(), FSRead(),
		json.RawMessage(`{"clone_path":"/tmp/helmdeck-clone-X1","path":"README.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Content string `json:"content"`
		SHA256  string `json:"sha256"`
		Size    int    `json:"size"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Content != "hello world\n" || out.Size != 12 {
		t.Errorf("output wrong: %+v", out)
	}
	if len(out.SHA256) != 64 {
		t.Errorf("sha256 wrong length: %s", out.SHA256)
	}
}

func TestFSRead_RejectsLargeFile(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("99999999\n")}, // wc -c reports a huge file
	}}
	eng := newFSEngine(t, ex)
	_, err := eng.Execute(context.Background(), FSRead(),
		json.RawMessage(`{"clone_path":"/tmp/helmdeck-clone-X1","path":"big"}`))
	if err == nil {
		t.Fatal("expected size cap to fail")
	}
}

func TestFSRead_RejectsUnsafePath(t *testing.T) {
	eng := newFSEngine(t, &recordingExecutor{})
	_, err := eng.Execute(context.Background(), FSRead(),
		json.RawMessage(`{"clone_path":"/tmp/helmdeck-clone-X1","path":"../../etc/passwd"}`))
	if err == nil {
		t.Fatal("expected unsafe path to be rejected")
	}
}

// --- fs.write -----------------------------------------------------------

func TestFSWrite_HappyPath(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{{ExitCode: 0}}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","path":"new/file.txt","content":"hi there"}`
	res, err := eng.Execute(context.Background(), FSWrite(), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	// The script should have been `mkdir -p ... && cat > ...`
	// with the content piped on stdin.
	script := ex.calls[0].Cmd[2]
	if !strings.Contains(script, "mkdir -p '/tmp/helmdeck-clone-X1/new'") {
		t.Errorf("mkdir missing: %s", script)
	}
	if !strings.Contains(script, "cat > '/tmp/helmdeck-clone-X1/new/file.txt'") {
		t.Errorf("cat missing: %s", script)
	}
	if string(ex.calls[0].Stdin) != "hi there" {
		t.Errorf("stdin not piped: %q", ex.calls[0].Stdin)
	}
	var out struct {
		Size int `json:"size"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Size != 8 {
		t.Errorf("size = %d", out.Size)
	}
}

// --- fs.patch -----------------------------------------------------------

func TestFSPatch_LiteralReplace(t *testing.T) {
	original := "package main\n\nfunc Hello() {}\n"
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("32\n")},          // wc -c
		{Stdout: []byte(original)},        // cat
		{ExitCode: 0},                     // write back
	}}
	eng := newFSEngine(t, ex)
	body := `{
	  "clone_path": "/tmp/helmdeck-clone-X1",
	  "path":       "main.go",
	  "search":     "Hello",
	  "replace":    "Goodbye"
	}`
	res, err := eng.Execute(context.Background(), FSPatch(), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	// The third call's stdin must be the patched contents.
	patched := string(ex.calls[2].Stdin)
	if !strings.Contains(patched, "Goodbye") || strings.Contains(patched, "Hello") {
		t.Errorf("patch not applied: %s", patched)
	}
	var out struct {
		Applied int `json:"applied"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Applied != 1 {
		t.Errorf("applied = %d", out.Applied)
	}
}

func TestFSPatch_NoMatch(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("10\n")},
		{Stdout: []byte("nothing\n")},
	}}
	eng := newFSEngine(t, ex)
	body := `{
	  "clone_path": "/tmp/helmdeck-clone-X1",
	  "path":       "x",
	  "search":     "missing",
	  "replace":    "found"
	}`
	_, err := eng.Execute(context.Background(), FSPatch(), json.RawMessage(body))
	if err == nil {
		t.Fatal("expected no-match to fail")
	}
}

func TestFSPatch_RespectsOccurrenceLimit(t *testing.T) {
	original := "Foo Foo Foo Foo"
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("15\n")},
		{Stdout: []byte(original)},
		{ExitCode: 0},
	}}
	eng := newFSEngine(t, ex)
	body := `{
	  "clone_path":  "/tmp/helmdeck-clone-X1",
	  "path":        "x",
	  "search":      "Foo",
	  "replace":     "Bar",
	  "occurrences": 2
	}`
	res, err := eng.Execute(context.Background(), FSPatch(), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	patched := string(ex.calls[2].Stdin)
	if patched != "Bar Bar Foo Foo" {
		t.Errorf("occurrence limit not respected: %s", patched)
	}
	var out struct {
		Applied int `json:"applied"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Applied != 2 {
		t.Errorf("applied = %d", out.Applied)
	}
}

// --- fs.list ------------------------------------------------------------

func TestFSList_StripsClonePathPrefix(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(
			"/tmp/helmdeck-clone-X1/README.md\n" +
				"/tmp/helmdeck-clone-X1/src/main.go\n" +
				"/tmp/helmdeck-clone-X1/src/util.go\n",
		)},
	}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","recursive":true}`
	res, err := eng.Execute(context.Background(), FSList(), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Files []string `json:"files"`
		Count int      `json:"count"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Count != 3 {
		t.Errorf("count = %d", out.Count)
	}
	for _, f := range out.Files {
		if strings.HasPrefix(f, "/") {
			t.Errorf("listing should be relative paths, got %s", f)
		}
	}
}

func TestFSList_GlobPropagatedToFind(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{{Stdout: []byte("")}}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","glob":"*.go"}`
	if _, err := eng.Execute(context.Background(), FSList(), json.RawMessage(body)); err != nil {
		t.Fatal(err)
	}
	script := ex.calls[0].Cmd[2]
	if !strings.Contains(script, "-name '*.go'") {
		t.Errorf("glob not in find script: %s", script)
	}
}

// --- cmd.run ------------------------------------------------------------

func TestCmdRun_HappyPath(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("ok"), ExitCode: 0},
	}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","command":["go","test","./..."]}`
	res, err := eng.Execute(context.Background(), CmdRun(), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	script := ex.calls[0].Cmd[2]
	if !strings.Contains(script, "cd '/tmp/helmdeck-clone-X1'") {
		t.Errorf("cd missing: %s", script)
	}
	if !strings.Contains(script, "exec 'go' 'test' './...'") {
		t.Errorf("argv not quoted: %s", script)
	}
	var out struct {
		Stdout   string `json:"stdout"`
		ExitCode int    `json:"exit_code"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Stdout != "ok" || out.ExitCode != 0 {
		t.Errorf("output = %+v", out)
	}
}

func TestCmdRun_NonZeroExitNotAnError(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte(""), Stderr: []byte("FAIL"), ExitCode: 1},
	}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","command":["false"]}`
	res, err := eng.Execute(context.Background(), CmdRun(), json.RawMessage(body))
	if err != nil {
		t.Fatalf("non-zero exit should NOT be a pack error: %v", err)
	}
	var out struct {
		ExitCode int `json:"exit_code"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.ExitCode != 1 {
		t.Errorf("exit code not surfaced: %d", out.ExitCode)
	}
}

func TestCmdRun_RejectsUnsafeClonePath(t *testing.T) {
	eng := newFSEngine(t, &recordingExecutor{})
	body := `{"clone_path":"/etc","command":["ls"]}`
	_, err := eng.Execute(context.Background(), CmdRun(), json.RawMessage(body))
	if err == nil {
		t.Fatal("expected unsafe clone path to be rejected")
	}
}

// --- git.commit ---------------------------------------------------------

func TestGitCommit_HappyPath(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("abc1234\n"), ExitCode: 0},
	}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","message":"Add new feature"}`
	res, err := eng.Execute(context.Background(), GitCommit(), json.RawMessage(body))
	if err != nil {
		t.Fatal(err)
	}
	script := ex.calls[0].Cmd[2]
	if !strings.Contains(script, "git -C '/tmp/helmdeck-clone-X1' add -A") {
		t.Errorf("add -A missing (default all=true): %s", script)
	}
	if !strings.Contains(script, "GIT_AUTHOR_NAME='helmdeck-agent'") {
		t.Errorf("author env missing: %s", script)
	}
	if !strings.Contains(script, "commit -m 'Add new feature'") {
		t.Errorf("message not in script: %s", script)
	}
	var out struct {
		Commit string `json:"commit"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Commit != "abc1234" {
		t.Errorf("commit = %s", out.Commit)
	}
}

func TestGitCommit_NothingToCommitMapsInvalidInput(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{ExitCode: 1, Stderr: []byte("On branch main\nnothing to commit, working tree clean\n")},
	}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","message":"x"}`
	_, err := eng.Execute(context.Background(), GitCommit(), json.RawMessage(body))
	if err == nil {
		t.Fatal("expected error")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeInvalidInput {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestGitCommit_RequiresMessage(t *testing.T) {
	eng := newFSEngine(t, &recordingExecutor{})
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","message":""}`
	_, err := eng.Execute(context.Background(), GitCommit(), json.RawMessage(body))
	if err == nil {
		t.Fatal("expected empty message to be rejected")
	}
}

func TestGitCommit_NoAddWhenAllFalse(t *testing.T) {
	ex := &recordingExecutor{replies: []session.ExecResult{
		{Stdout: []byte("abc\n"), ExitCode: 0},
	}}
	eng := newFSEngine(t, ex)
	body := `{"clone_path":"/tmp/helmdeck-clone-X1","message":"x","all":false}`
	if _, err := eng.Execute(context.Background(), GitCommit(), json.RawMessage(body)); err != nil {
		t.Fatal(err)
	}
	script := ex.calls[0].Cmd[2]
	if strings.Contains(script, "add -A") {
		t.Errorf("all=false should skip add -A: %s", script)
	}
}
