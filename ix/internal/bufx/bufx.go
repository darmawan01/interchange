// Package bufx runs the installed buf binary.
//
// ix shells out rather than embedding buf as a library: buf's CLI is its
// supported interface, the version a project pins is the version that must
// run, and a user who already knows buf can reproduce anything ix does by
// reading the command it printed.
package bufx

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner invokes buf in a working directory.
type Runner struct {
	// Bin is the buf executable; empty means "buf" on PATH.
	Bin string

	// Dir is the working directory for every invocation.
	Dir string

	// Env overrides the child environment when non-nil.
	Env []string

	// Verbose echoes each command to Stderr before running it.
	Verbose bool

	// Stdout and Stderr receive a streamed command's output. Nil means the
	// process's own. Commands wire these to the UI so that buf's diagnostics
	// land wherever the rest of ix's output lands.
	Stdout, Stderr io.Writer
}

// ErrNotFound is what every command surfaces when buf is not installed. The
// message names the fix, because "exec: buf: not found" three frames deep is
// not a fix.
type ErrNotFound struct{ Err error }

func (e *ErrNotFound) Error() string {
	return "buf not found on PATH -- ix drives buf for build, lint, breaking and generate.\n" +
		"  install:  brew install bufbuild/buf/buf   (or see https://buf.build/docs/installation)"
}

func (e *ErrNotFound) Unwrap() error { return e.Err }

func (r *Runner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "buf"
}

// Lookup resolves the buf binary, or returns *ErrNotFound.
func (r *Runner) Lookup() (string, error) {
	p, err := exec.LookPath(r.bin())
	if err != nil {
		return "", &ErrNotFound{Err: err}
	}
	return p, nil
}

// Version is buf's reported version.
func (r *Runner) Version() (string, error) {
	out, err := r.Output("--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ExitError carries a failed buf invocation's diagnostics. Commands print
// Stderr verbatim: buf's messages carry file and line, and rewording them
// loses that.
type ExitError struct {
	Args   []string
	Code   int
	Stderr string
	Err    error
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("buf %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *ExitError) Unwrap() error { return e.Err }

func (r *Runner) cmd(args ...string) *exec.Cmd {
	c := exec.Command(r.bin(), args...)
	c.Dir = r.Dir
	if r.Env != nil {
		c.Env = r.Env
	}
	return c
}

// Output runs buf and returns stdout. A binary image on stdout is the point:
// `buf build -o -` is how every descriptor-reading command gets its input.
func (r *Runner) Output(args ...string) ([]byte, error) {
	if _, err := r.Lookup(); err != nil {
		return nil, err
	}
	r.echo(args)
	c := r.cmd(args...)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return stdout.Bytes(), &ExitError{Args: args, Code: c.ProcessState.ExitCode(), Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// Run streams buf's output to the caller's stdout and stderr. Commands that
// exist to relay buf's diagnostics -- lint, breaking, format -- use this so
// the user sees buf's own formatting rather than a re-rendered copy.
func (r *Runner) Run(args ...string) error {
	if _, err := r.Lookup(); err != nil {
		return err
	}
	r.echo(args)
	c := r.cmd(args...)
	// buf's diagnostics are the answer, not noise: they carry file and line.
	// They are streamed AND kept, so an ExitError can carry them to a caller
	// that needs to decide something from them.
	var stderr bytes.Buffer
	c.Stdout = orW(r.Stdout, os.Stdout)
	c.Stderr = io.MultiWriter(orW(r.Stderr, os.Stderr), &stderr)
	if err := c.Run(); err != nil {
		return &ExitError{Args: args, Code: c.ProcessState.ExitCode(), Stderr: stderr.String(), Err: err}
	}
	return nil
}

func (r *Runner) echo(args []string) {
	if !r.Verbose {
		return
	}
	fmt.Fprintf(orW(r.Stderr, os.Stderr), "+ %s %s\n", r.bin(), strings.Join(args, " "))
}

func orW(w io.Writer, def io.Writer) io.Writer {
	if w == nil {
		return def
	}
	return w
}
