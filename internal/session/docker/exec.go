package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/tosin2013/helmdeck/internal/session"
)

// Exec runs a command inside an existing session container and
// returns its captured output. Implements session.Executor.
//
// Non-zero exit codes are surfaced via ExecResult.ExitCode rather
// than as Go errors so callers (T210's slides.render pack) can
// distinguish "marp returned 1" from "the docker daemon was
// unreachable" without scraping error messages.
func (r *Runtime) Exec(ctx context.Context, id string, req session.ExecRequest) (session.ExecResult, error) {
	r.mu.RLock()
	s, ok := r.sessions[id]
	r.mu.RUnlock()
	if !ok {
		return session.ExecResult{}, session.ErrSessionNotFound
	}
	if len(req.Cmd) == 0 {
		return session.ExecResult{}, fmt.Errorf("docker exec: cmd required")
	}

	createResp, err := r.cli.ContainerExecCreate(ctx, s.ContainerID, container.ExecOptions{
		Cmd:          req.Cmd,
		Env:          req.Env,
		WorkingDir:   req.WorkingDir,
		AttachStdin:  len(req.Stdin) > 0,
		AttachStdout: true,
		AttachStderr: true,
		// Tty=false so docker emits the multiplexed stdout/stderr stream
		// that stdcopy can demux. With Tty=true the streams are merged
		// and we'd lose the stderr/stdout boundary marp's `--stderr`
		// flag relies on.
		Tty: false,
	})
	if err != nil {
		return session.ExecResult{}, fmt.Errorf("docker exec create: %w", err)
	}

	hijacked, err := r.cli.ContainerExecAttach(ctx, createResp.ID, container.ExecStartOptions{Tty: false})
	if err != nil {
		return session.ExecResult{}, fmt.Errorf("docker exec attach: %w", err)
	}
	defer hijacked.Close()

	// Stream stdin in a goroutine so a command that reads everything
	// before producing output (like marp) doesn't deadlock against us
	// reading the response. We close the write half once stdin is
	// drained so the upstream sees EOF.
	if len(req.Stdin) > 0 {
		go func() {
			_, _ = io.Copy(hijacked.Conn, bytes.NewReader(req.Stdin))
			_ = hijacked.CloseWrite()
		}()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, hijacked.Reader); err != nil {
		return session.ExecResult{}, fmt.Errorf("docker exec read: %w", err)
	}

	insp, err := r.cli.ContainerExecInspect(ctx, createResp.ID)
	if err != nil {
		return session.ExecResult{}, fmt.Errorf("docker exec inspect: %w", err)
	}

	return session.ExecResult{
		Stdout:   stdoutBuf.Bytes(),
		Stderr:   stderrBuf.Bytes(),
		ExitCode: insp.ExitCode,
	}, nil
}
