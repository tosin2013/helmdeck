package session

import "context"

// ExecRequest is one in-container command invocation. The fields
// mirror what Docker / Kubernetes exec APIs need; backends translate
// as appropriate. Stdin is optional — pass nil for commands that
// don't read from standard input.
type ExecRequest struct {
	Cmd   []string
	Stdin []byte
	Env   []string
	// WorkingDir is the directory the command runs in. Empty means
	// "use the container's WORKDIR".
	WorkingDir string
}

// ExecResult is the captured output of an ExecRequest. ExitCode is
// 0 on success; backends MUST surface non-zero exit codes here
// instead of returning an error so callers can distinguish "the
// command ran but failed" from "the runtime couldn't run the
// command at all".
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Executor is the optional sidecar contract for backends that can
// run commands inside an existing session container. It is kept
// separate from Runtime so backends can adopt it incrementally and
// so the existing fakes used in tests don't have to grow a new
// method overnight.
//
// Pack handlers reach this through ExecutionContext.Exec — they
// never see the runtime directly.
type Executor interface {
	Exec(ctx context.Context, sessionID string, req ExecRequest) (ExecResult, error)
}
