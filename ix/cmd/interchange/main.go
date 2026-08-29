// Command interchange is `ix` under its long name. Same tree, same flags --
// docs/08 settled on `ix` with `interchange` as an alias, and this is the
// alias as a real binary so `go install` gives you both without a symlink.
package main

import (
	"os"

	"github.com/darmawan01/interchange/ix/internal/cmd"
)

func main() { os.Exit(cmd.Execute("interchange")) }
