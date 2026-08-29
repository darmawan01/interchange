package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/band"
	"github.com/darmawan01/interchange/ix/internal/config"
	"github.com/darmawan01/interchange/ix/internal/gentmpl"
	"github.com/spf13/cobra"
)

// A broken setup fails somewhere downstream with a message about the symptom.
// `ix doctor` is where it fails with a message about the cause.
func newDoctor(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose a broken setup",
		Long: "Checks the tools ix drives, the config it reads, and whether the committed\n" +
			"generated output is still current. Exits non-zero if anything is broken\n" +
			"enough to make another command fail confusingly.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(g)
		},
	}
}

func runDoctor(g *globals) error {
	ui := g.ui
	fmt.Fprintln(ui.Out)
	bad := 0
	fail := func(label, detail string) {
		ui.Fail(label, detail)
		bad++
	}

	// buf.
	p, err := openProjectLenient(g)
	runner := p.Buf
	if bin, lerr := runner.Lookup(); lerr != nil {
		fail("buf", lerr.Error())
	} else if v, verr := runner.Version(); verr != nil {
		fail("buf", "found at "+bin+" but `buf --version` failed: "+verr.Error())
	} else {
		ui.OK("buf", fmt.Sprintf("%s  (%s)", v, bin))
	}

	// Go toolchain: local plugins are built with it, and generated Go needs
	// it to compile.
	if bin, gerr := exec.LookPath("go"); gerr != nil {
		fail("go", "not on PATH -- local plugins are built with `go build`")
	} else {
		out, _ := exec.Command(bin, "version").Output()
		ui.OK("go", strings.TrimSpace(strings.TrimPrefix(string(out), "go version "))+"  ("+bin+")")
	}
	ui.OK("ix", fmt.Sprintf("%s  (%s/%s)", Version, runtime.GOOS, runtime.GOARCH))

	// Config. A directory that is not a project yet is not a broken setup:
	// doctor reports on the toolchain it did check and stops there.
	if errors.Is(err, config.ErrNotFound) {
		ui.Warn(config.Name, "not an interchange project — run `ix init` to scaffold one")
		fmt.Fprintln(ui.Out)
		if bad > 0 {
			return failed(1)
		}
		return nil
	}
	if err != nil {
		fail(config.Name, err.Error())
		fmt.Fprintln(ui.Out)
		return failed(1)
	}
	ui.OK(config.Name, fmt.Sprintf("%s  (%d source(s), %d generator(s))", p.Rel(p.Cfg.Path), len(p.Cfg.Sources), len(p.Cfg.Generate)))

	// Local plugin binaries.
	var missing []string
	local := 0
	for _, gen := range p.Cfg.Generate {
		if !gen.Local() {
			continue
		}
		local++
		bin := filepath.Join(p.Cfg.Root, gen.Binary())
		if _, serr := os.Stat(bin); serr != nil {
			if _, perr := exec.LookPath(gen.Binary()); perr != nil {
				missing = append(missing, gen.Binary())
			}
		}
	}
	switch {
	case local == 0:
		ui.OK("plugins", "no local plugins configured")
	case len(missing) == 0:
		ui.OK("plugins", fmt.Sprintf("%d local plugin(s) built", local))
	default:
		fail("plugins", fmt.Sprintf("not built: %s -- run `ix generate`, which builds them first", strings.Join(missing, ", ")))
	}

	// The band table.
	tbl := band.Load(p.Cfg.Root)
	ui.OK("band", fmt.Sprintf("%d registered annotation(s)  (%s)", len(tbl.Entries()), tbl.Source))

	// buf.lock staleness: a lock older than the buf.yaml that declares the
	// deps means somebody added a dependency without updating the lock, and
	// the next clean build resolves something different from this one.
	lockPath := filepath.Join(p.Cfg.Root, "buf.lock")
	yamlPath := filepath.Join(p.Cfg.Root, "buf.yaml")
	ls, lerr := os.Stat(lockPath)
	ys, yerr := os.Stat(yamlPath)
	switch {
	case yerr != nil:
		fail("buf.yaml", "missing -- ix needs a buf module or workspace to build the contract")
	case lerr != nil:
		if hasDeps(yamlPath) {
			fail("buf.lock", "buf.yaml declares deps but there is no lock -- run `buf dep update`")
		} else {
			ui.OK("buf.lock", "not needed (no declared deps)")
		}
	case ls.ModTime().Before(ys.ModTime()):
		ui.Warn("buf.lock", "older than buf.yaml -- run `buf dep update` if you changed deps")
	default:
		ui.OK("buf.lock", "current")
	}

	// The descriptor set has to build before anything else can be checked.
	im, ierr := p.Image()
	if ierr != nil {
		fail("contract", ierr.Error())
		fmt.Fprintln(ui.Out)
		return failed(1)
	}
	methods, _ := p.Methods()
	ui.OK("contract", fmt.Sprintf("%d descriptor(s), %d RPC(s), %d extension(s)", countLocalFiles(im, p.Local()), len(methods), len(im.Extensions)))

	// Generated output staleness -- reported, not fixed. `ix verify` is the
	// gate; doctor only tells you where you stand.
	if len(p.Cfg.Generate) == 0 {
		ui.OK("generated", "no generators configured")
	} else {
		tmp, terr := os.MkdirTemp("", "ix-doctor-")
		if terr != nil {
			return terr
		}
		defer os.RemoveAll(tmp)
		if gerr := p.generate(gentmpl.Options{OutPrefix: tmp}, g.verbose); gerr != nil {
			ui.Warn("generated", "could not regenerate to compare: "+firstLine(gerr.Error()))
		} else if diffs := p.diffOutputs(tmp); len(diffs) > 0 {
			fail("generated", fmt.Sprintf("stale -- %s (and %d more) · run `ix generate`", diffs[0], len(diffs)-1))
		} else {
			ui.OK("generated", "up to date")
		}
	}

	fmt.Fprintln(ui.Out)
	if bad > 0 {
		return failed(1)
	}
	return nil
}

// openProjectLenient returns a Project even when the config failed to load,
// so doctor can still report on buf and the Go toolchain -- which is exactly
// the situation someone runs doctor in.
func openProjectLenient(g *globals) (*Project, error) {
	p, err := openProject(g)
	if err == nil {
		return p, nil
	}
	return &Project{UI: g.ui, Buf: bufRunner(g, "")}, err
}

func hasDeps(bufYAML string) bool {
	b, err := os.ReadFile(bufYAML)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "deps:") {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
