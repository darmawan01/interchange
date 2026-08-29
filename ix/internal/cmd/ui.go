package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// UI is where a command writes. Everything goes through it so that tests
// capture output and so that colour is decided in exactly one place.
type UI struct {
	Out   io.Writer
	Err   io.Writer
	Color bool
}

// NewUI colours only when stdout is a terminal. A CI log is the primary
// consumer of `ix verify`, and escape codes in a CI log are noise.
func NewUI() *UI {
	return &UI{Out: os.Stdout, Err: os.Stderr, Color: isTTY(os.Stdout)}
}

func isTTY(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func (u *UI) Printf(format string, a ...any) { fmt.Fprintf(u.Out, format, a...) }
func (u *UI) Println(a ...any)               { fmt.Fprintln(u.Out, a...) }
func (u *UI) Errf(format string, a ...any)   { fmt.Fprintf(u.Err, format, a...) }

const (
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

func (u *UI) green(s string) string { return u.wrap(ansiGreen, s) }
func (u *UI) red(s string) string   { return u.wrap(ansiRed, s) }

// Dim renders secondary text, or plain text with colour off.
func (u *UI) Dim(s string) string { return u.wrap(ansiDim, s) }

func (u *UI) wrap(code, s string) string {
	if !u.Color {
		return s
	}
	return code + s + ansiReset
}

// checkWidth is the label column in the ✓/✗ report `ix verify` prints.
const checkWidth = 16

// OK writes a passing check line: "  ✓ label           detail".
func (u *UI) OK(label, detail string) {
	u.Printf("  %s %s%s\n", u.green("✓"), pad(label, checkWidth), detail)
}

// Fail writes a failing check line.
func (u *UI) Fail(label, detail string) {
	u.Printf("  %s %s%s\n", u.red("✗"), pad(label, checkWidth), detail)
}

// Warn writes an advisory line. It is not a failure: ix warns where the
// design leaves the decision to the adopter.
func (u *UI) Warn(label, detail string) {
	u.Printf("  %s %s%s\n", u.wrap(ansiDim, "⚠"), pad(label, checkWidth), detail)
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-len(s))
}
