package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/gentmpl"
	"github.com/spf13/cobra"
)

func newGenerate(g *globals) *cobra.Command {
	var only []string
	c := &cobra.Command{
		Use:   "generate",
		Short: "Run every configured generator",
		Long: "Build any local plugins, then run buf generate with a template synthesized\n" +
			"from interchange.yaml's generate[] block.\n\n" +
			"A local plugin is one whose `plugin:` is a path. If ./cmd/<name> exists in\n" +
			"the project, ix builds it with `go build -o <plugin> ./cmd/<name>` first --\n" +
			"a generator that is not rebuilt before it runs is a generator that emits\n" +
			"yesterday's output.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			return p.generate(gentmpl.Options{Only: only}, g.verbose)
		},
	}
	c.Flags().StringSliceVar(&only, "only", nil, "run only the named generators (by their plugin: value)")
	return c
}

// generate is shared by `ix generate` and the drift gate; the gate passes an
// OutPrefix so the same code path writes into a temp tree.
func (p *Project) generate(o gentmpl.Options, verbose bool) error {
	if err := p.buildLocalPlugins(verbose); err != nil {
		return err
	}
	tmpl, err := gentmpl.Build(p.Cfg, o)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "ix-buf-gen-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(tmpl); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if verbose {
		p.UI.Errf("--- template %s ---\n%s", f.Name(), tmpl)
	}
	return p.Buf.Run("generate", "--template", f.Name())
}

// buildLocalPlugins compiles every local generator that has a ./cmd source
// tree. A local plugin without one is assumed to be built by other means and
// only checked for existence.
func (p *Project) buildLocalPlugins(verbose bool) error {
	for _, gen := range p.Cfg.Generate {
		if !gen.Local() {
			continue
		}
		bin := gen.Binary()
		src := filepath.Join(p.Cfg.Root, "cmd", filepath.Base(bin))
		if st, err := os.Stat(src); err != nil || !st.IsDir() {
			if !strings.ContainsAny(bin, "/\\") {
				continue // a plugin resolved from PATH
			}
			if _, err := os.Stat(filepath.Join(p.Cfg.Root, bin)); err != nil {
				return fmt.Errorf("local plugin %s: not built and no %s to build it from", bin, p.Rel(src))
			}
			continue
		}
		out := filepath.Join(p.Cfg.Root, bin)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rel := "./" + filepath.ToSlash(filepath.Join("cmd", filepath.Base(bin)))
		cmd := exec.Command("go", "build", "-o", out, rel)
		cmd.Dir = p.Cfg.Root
		cmd.Stdout = p.UI.Err
		cmd.Stderr = p.UI.Err
		if verbose {
			p.UI.Errf("+ go build -o %s %s\n", out, rel)
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("building local plugin %s: %w", bin, err)
		}
	}
	return nil
}
