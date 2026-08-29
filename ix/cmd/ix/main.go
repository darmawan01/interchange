// Command ix is the one binary a user installs. It wraps the whole pipeline:
// scaffold, import, generate, format, lint, breaking-change detection, the
// drift gate, and inspection.
package main

import (
	"os"

	"github.com/darmawan01/interchange/ix/internal/cmd"
)

func main() { os.Exit(cmd.Execute("ix")) }
