package mcp

import "os/exec"

// osExec is a tiny wrapper that lets stdio_test.go avoid pulling
// "os/exec" into its own import block (the embedded fake program
// source already mentions os.Stdin, which would shadow). Keeping it
// in its own file means stdio_test.go reads cleanly.
func osExec(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
