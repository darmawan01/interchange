package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/config"
	"github.com/darmawan01/interchange/ix/internal/gentmpl"
	"github.com/darmawan01/interchange/ix/internal/scaffold"
	"github.com/spf13/cobra"
)

func newInit(g *globals) *cobra.Command {
	var o scaffold.Options
	var dryRun bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a project",
		Long: "Writes interchange.yaml, a buf workspace, a starter service, a Makefile and\n" +
			"a CI workflow -- everything needed to get from nothing to a generated,\n" +
			"typed client without knowing protobuf first.\n\n" +
			"The interchange annotation protos are written into api/ rather than\n" +
			"fetched, so the new project builds and lints with nothing but buf on PATH.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := g.dir
			if len(args) == 1 {
				dir = args[0]
			}
			if dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				dir = wd
			}
			o.Dir = dir
			if o.GoModule == "" {
				o.GoModule = detectGoModule(dir)
			}
			if o.GoModule == "" {
				// Managed mode needs a go_package_prefix or the Go
				// generators refuse to run at all. A placeholder the user
				// edits beats a scaffold that cannot generate.
				o.GoModule = "example.com/" + cfgNameOrDefault(o.Name)
			}

			if dryRun {
				files, err := scaffold.Plan(o)
				if err != nil {
					return err
				}
				for _, f := range files {
					fmt.Fprintf(g.ui.Out, "  %s  (%d bytes)\n", f.Path, len(f.Body))
				}
				return nil
			}

			written, err := scaffold.Write(o)
			if err != nil {
				return err
			}

			// buf.gen.yaml is derived from interchange.yaml rather than
			// templated, so the two can never disagree on day one -- and
			// `ix verify` keeps checking that they still agree.
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			tmpl, err := gentmpl.Build(cfg, gentmpl.Options{})
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "buf.gen.yaml"), tmpl, 0o644); err != nil {
				return err
			}
			written = append(written, "buf.gen.yaml")

			fmt.Fprintln(g.ui.Out)
			for _, f := range written {
				fmt.Fprintf(g.ui.Out, "  %s %s\n", g.ui.green("+"), f)
			}
			entity := titleCase(o.Name)
			fmt.Fprintf(g.ui.Out, `
  next:
    ix describe %sService.List%ss    what the contract already exposes, on every road
    ix lint         the naming rules and the annotation band
    ix generate     typed clients for every configured target
    ix dev          exercise the contract with no infrastructure
`, entity, entity)
			fmt.Fprintln(g.ui.Out)
			return nil
		},
	}
	c.Flags().StringVar(&o.Name, "name", "example", "proto package and service prefix (lower_snake_case)")
	c.Flags().StringVar(&o.GoModule, "go-module", "", "Go module path generated code lives under (default: read from go.mod)")
	c.Flags().BoolVar(&o.Force, "force", false, "overwrite existing files")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "list the files that would be written")
	return c
}

func cfgNameOrDefault(n string) string {
	if n == "" {
		return "example"
	}
	return n
}

// titleCase turns a proto package segment into the entity name the scaffold
// used, so the "next" hints name real commands.
func titleCase(n string) string {
	if n == "" {
		n = "example"
	}
	var b strings.Builder
	up := true
	for _, r := range n {
		if r == '_' {
			up = true
			continue
		}
		if up {
			b.WriteString(strings.ToUpper(string(r)))
			up = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// detectGoModule reads the module path out of go.mod so managed mode points
// generated Go at an import path that actually resolves.
func detectGoModule(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
