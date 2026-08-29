package cmd

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/gentmpl"
	"github.com/darmawan01/interchange/ix/internal/image"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// `ix verify` is the one command CI runs, and it is the whole reason the
// contract cannot drift. Generated code is committed and reviewable; this is
// what makes that true rather than aspirational.
func newVerify(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "verify",
		Short: "The drift gate: regenerate and fail if the tree moved",
		Long: "Regenerates every configured target into a temporary tree and compares it\n" +
			"byte for byte with the committed output. It touches nothing in the working\n" +
			"copy, so it is safe to run on a dirty checkout and safe to run in CI.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			return p.verify(g)
		},
	}
	return c
}

func (p *Project) verify(g *globals) error {
	ui := g.ui
	fmt.Fprintln(ui.Out)

	im, err := p.Image()
	if err != nil {
		ui.Fail("frontends", err.Error())
		return failed(1)
	}
	nfiles := countLocalFiles(im, p.Local())
	ui.OK("frontends", fmt.Sprintf("%d sources → %d descriptors", len(p.Cfg.Sources), nfiles))

	methods, err := p.Methods()
	if err != nil {
		ui.Fail("annotations", err.Error())
		return failed(1)
	}
	annotated, public := 0, 0
	for _, m := range methods {
		if m.TransportsFrom == "method" || m.TransportsFrom == "service" {
			annotated++
		}
		if m.Auth != nil && m.Auth.Public {
			public++
		}
	}
	ui.OK("annotations", fmt.Sprintf("%d RPCs, %d annotated, %d public (reviewed)", len(methods), annotated, public))
	ui.OK("generators", fmt.Sprintf("%d targets", len(p.Cfg.Generate)))

	// The committed buf.gen.yaml exists so `buf generate` works without ix.
	// If it has drifted from interchange.yaml, the two files disagree about
	// what CI generates -- which is the same failure the gate exists to stop.
	if want, err := gentmpl.Build(p.Cfg, gentmpl.Options{}); err == nil {
		path := filepath.Join(p.Cfg.Root, "buf.gen.yaml")
		if got, err := os.ReadFile(path); err == nil && !bytes.Equal(got, want) {
			ui.Fail("template", "buf.gen.yaml no longer matches interchange.yaml")
			fmt.Fprintln(ui.Out)
			fmt.Fprintf(ui.Out, "  run `ix plugin sync` to rewrite buf.gen.yaml from interchange.yaml\n")
			return failed(1)
		}
	}

	tmp, err := os.MkdirTemp("", "ix-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := p.generate(gentmpl.Options{OutPrefix: tmp}, g.verbose); err != nil {
		ui.Fail("drift", "regeneration failed")
		fmt.Fprintln(ui.Out)
		return fail(1, err)
	}

	diffs := p.diffOutputs(tmp)
	if len(diffs) == 0 {
		ui.OK("drift", "generated output matches the contract")
		fmt.Fprintln(ui.Out)
		return nil
	}
	ui.Fail("drift", diffs[0])
	for _, d := range diffs[1:] {
		fmt.Fprintf(ui.Out, "  %s%s\n", strings.Repeat(" ", checkWidth+2), d)
	}
	fmt.Fprintln(ui.Out)
	fmt.Fprintf(ui.Out, "  generated output is stale — run `ix generate`\n")
	fmt.Fprintln(ui.Out)
	return failed(1)
}

func countLocalFiles(im *image.Image, local func(string) bool) int {
	n := 0
	im.Files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if local(fd.Path()) {
			n++
		}
		return true
	})
	return n
}

// diffOutputs compares the regenerated tree with the committed one, listing
// the differences in a stable order. The first line is what the ✗ carries;
// the rest follow, because "one file differs" is rarely the whole story.
func (p *Project) diffOutputs(tmp string) []string {
	var out []string
	for _, dir := range p.Cfg.OutDirs() {
		want := filepath.Join(tmp, dir)
		got := filepath.Join(p.Cfg.Root, dir)

		wantFiles := listFiles(want)
		gotFiles := listFiles(got)

		names := map[string]bool{}
		for n := range wantFiles {
			names[n] = true
		}
		for n := range gotFiles {
			names[n] = true
		}
		var sorted []string
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)

		for _, n := range sorted {
			rel := filepath.ToSlash(filepath.Join(dir, n))
			w, inWant := wantFiles[n]
			gg, inGot := gotFiles[n]
			switch {
			case inWant && !inGot:
				out = append(out, rel+" is missing")
			case !inWant && inGot:
				out = append(out, rel+" is no longer generated")
			case !bytes.Equal(w, gg):
				out = append(out, rel+" differs")
			}
		}
	}
	return out
}

func listFiles(root string) map[string][]byte {
	out := map[string][]byte{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	return out
}
