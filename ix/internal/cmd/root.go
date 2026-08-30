// Package cmd is the ix command tree.
//
// One binary wraps the whole pipeline -- init, import, generate, fmt, lint,
// breaking, the drift gate, and inspection -- because the design goal is
// `ix init` to a generated typed client in under a minute, and every command
// here exists to keep that true as the project grows (docs/11).
//
// ix shells out to the installed buf for build, lint, breaking and generate.
// buf's CLI is its supported interface, the version a project pins is the
// version that must run, and a user can reproduce anything ix did by reading
// the command `ix --verbose` printed.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is stamped at link time by the release build. It is not baked into
// generated output anywhere -- a version string that changes per build would
// flap the drift gate.
var Version = "dev"

func init() {
	// A `go install ...@v0.1.0` build carries no ldflags, so without this it
	// reports "dev" and `ix doctor` tells a user nothing about which binary
	// they are running. The module version is right there in the build info.
	if Version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return
	}
	Version = info.Main.Version
}

type globals struct {
	dir     string
	buf     string
	verbose bool
	ui      *UI
}

// exitCode carries a specific exit status out of a command. `ix verify`
// failing the drift gate is not a usage error, and CI reads the code.
type exitCode struct {
	code int
	err  error
}

func (e *exitCode) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e *exitCode) Unwrap() error { return e.err }

// silent marks an error whose message a command already printed.
var errSilent = errors.New("")

// New builds the root command under a given binary name. `ix` and
// `interchange` are the same tree; docs/08 settled on ix with interchange as
// an alias.
func New(name string) *cobra.Command {
	return newRoot(name, &globals{ui: NewUI()})
}

// newRoot builds the tree against a caller-supplied globals, which is how the
// tests drive real commands with captured output.
func newRoot(name string, g *globals) *cobra.Command {
	root := &cobra.Command{
		Use:   name,
		Short: "One contract, every road",
		Long: "ix declares a service once and fans it out onto every transport.\n" +
			"It wraps the pipeline: scaffold, generate, format, lint, breaking-change\n" +
			"detection, the drift gate, and inspection.\n\n" +
			"Everything that reads a contract reads the descriptor set buf builds, which\n" +
			"is the same descriptor a running server reflects on.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	root.PersistentFlags().StringVarP(&g.dir, "dir", "C", "", "run as if ix were started in this directory")
	root.PersistentFlags().StringVar(&g.buf, "buf", "", "path to the buf binary (default: buf on PATH)")
	root.PersistentFlags().BoolVarP(&g.verbose, "verbose", "v", false, "echo every buf invocation")

	root.AddCommand(
		newInit(g),
		newImport(g),
		newGenerate(g),
		newFmt(g),
		newLint(g),
		newBreaking(g),
		newVerify(g),
		newDescribe(g),
		newPlugin(g),
		newDev(g),
		newDoctor(g),
	)
	return root
}

// Execute runs the tree and maps errors to exit codes.
func Execute(name string) int {
	root := New(name)
	err := root.Execute()
	if err == nil {
		return 0
	}
	var ec *exitCode
	if errors.As(err, &ec) {
		if ec.err != nil && !errors.Is(ec.err, errSilent) {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, ec.err)
		}
		return ec.code
	}
	if errors.Is(err, errSilent) {
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
	return 1
}

func fail(code int, err error) error { return &exitCode{code: code, err: err} }

// failed is a command that printed its own diagnostics and just needs the
// exit status.
func failed(code int) error { return &exitCode{code: code, err: errSilent} }
