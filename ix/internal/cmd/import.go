package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/frontend/dsl"
	"github.com/darmawan01/interchange/frontend/openapi"
	"github.com/spf13/cobra"
)

func init() {
	// Registering here rather than in each frontend's init keeps which
	// frontends this build ships an explicit property of ix, greppable in one
	// place, instead of a consequence of the import graph.
	interchange.RegisterFrontend(dsl.New())
	interchange.RegisterFrontend(openapi.New())
}

// `ix import` is the on-ramp: a non-proto source becomes part of the canonical
// tree. The frontend adapters that do the conversion live in their own modules
// (§09); ix links the ones it ships and detects the rest by sniffing, so a
// format nobody has written an adapter for is named rather than mystifying.
//
// It refuses to write a partial tree. A frontend that silently drops what it
// cannot represent produces a contract that lies, which is the exact failure
// this project exists to remove -- so nothing is written until every
// unresolved construct is resolved.
func newImport(g *globals) *cobra.Command {
	var (
		outDir   string
		pkg      string
		sidecar  string
		dryRun   bool
		frontend string
	)
	c := &cobra.Command{
		Use:   "import <file>...",
		Short: "Bring a non-proto source into the canonical tree",
		Long: "Detects the source format, converts it to canonical .proto, and writes it\n" +
			"into the api tree, where it is committed and reviewed like any other\n" +
			"contract.\n\n" +
			"It refuses to emit a partial contract: every construct the frontend cannot\n" +
			"represent is reported with its source location, and nothing is written\n" +
			"until they are resolved.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := interchange.Sources{Content: map[string][]byte{}}
			for _, path := range args {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				src.Paths = append(src.Paths, path)
				src.Content[path] = b
			}
			if sidecar != "" {
				b, err := os.ReadFile(sidecar)
				if err != nil {
					return err
				}
				src.Sidecar, src.SidecarPath = b, sidecar
			}

			first := src.Paths[0]
			head := src.Content[first]
			if len(head) > 4096 {
				head = head[:4096]
			}

			kind, guess := detectFormat(first, head)
			fe, err := resolveFrontend(frontend, first, head)
			fmt.Fprintln(g.ui.Out)
			fmt.Fprintf(g.ui.Out, "  detected   %s\n", kind)
			if err != nil {
				fmt.Fprintf(g.ui.Out, "  frontend   none (%s)\n\n", guess)
				return fail(2, err)
			}
			fmt.Fprintf(g.ui.Out, "  frontend   %s\n", fe.Name())
			fmt.Fprintln(g.ui.Out)

			// The emitted proto is the artifact -- committed, reviewed, and
			// under the same drift gate as generated code. A frontend that
			// can only produce descriptors leaves the IR invisible, which is
			// the thing that rule exists to prevent.
			emitter, ok := fe.(interchange.SourceEmitter)
			if !ok {
				return fail(2, fmt.Errorf("the %s frontend cannot emit .proto source, so `ix import` has nothing reviewable to write", fe.Name()))
			}

			opt := interchange.Options{Package: pkg}
			// Run outside a project and the import still works; it just
			// cannot resolve annotations against the existing tree.
			if p, perr := openProject(g); perr == nil {
				if im, ierr := p.Image(); ierr == nil {
					// The annotation protos reach the frontend this way, so
					// it resolves them without linking the modules that
					// define them.
					opt.Deps = im.FDS
				}
				if outDir == "" {
					if dirs := p.Cfg.ProtoDirs(); len(dirs) > 0 {
						outDir = filepath.Join(p.Cfg.Root, dirs[0])
					}
				}
			}
			if outDir == "" {
				outDir = "api"
			}

			files, diags, perr := emitter.ProtoSources(cmd.Context(), src, opt)
			report(g, diags)

			if perr != nil || diags.HasErrors() {
				n := 0
				for _, d := range diags {
					if d.Severity == interchange.SeverityError {
						n++
					}
				}
				if n > 0 {
					fmt.Fprintf(g.ui.Out, "  nothing written — resolve the %d item(s) above, then re-run\n\n", n)
					return failed(3)
				}
				return fail(3, perr)
			}

			paths := make([]string, 0, len(files))
			for p := range files {
				paths = append(paths, p)
			}
			sort.Strings(paths)

			for _, rel := range paths {
				dest := filepath.Join(outDir, rel)
				if dryRun {
					fmt.Fprintf(g.ui.Out, "  would write %s (%d bytes)\n", dest, len(files[rel]))
					continue
				}
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(dest, files[rel], 0o644); err != nil {
					return err
				}
				fmt.Fprintf(g.ui.Out, "  wrote      %s\n", dest)
			}
			fmt.Fprintln(g.ui.Out)
			if !dryRun {
				fmt.Fprintln(g.ui.Out, "  commit the emitted proto: it is the contract now, and `ix verify` gates it")
				fmt.Fprintln(g.ui.Out)
			}
			return nil
		},
	}
	c.Flags().StringVar(&outDir, "out", "", "directory to write the emitted proto into (default: the configured api root)")
	c.Flags().StringVar(&pkg, "package", "", "proto package for formats that have no equivalent notion")
	c.Flags().StringVar(&sidecar, "sidecar", "", "annotations file, for formats with nowhere to put an annotation")
	c.Flags().StringVar(&frontend, "frontend", "", "force a frontend by name instead of detecting one")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be written without writing it")
	return c
}

func resolveFrontend(name, path string, head []byte) (interchange.Frontend, error) {
	if name != "" {
		fe, ok := interchange.FrontendFor(name)
		if !ok {
			return nil, fmt.Errorf("no frontend named %q (this build ships %s)", name, strings.Join(interchange.Frontends(), ", "))
		}
		return fe, nil
	}
	fe, err := interchange.DetectFrontend(path, head)
	if err != nil {
		return nil, fmt.Errorf("%w -- this build of ix ships the %s frontend(s); name one with --frontend if the format is right but the sniff is wrong",
			err, strings.Join(interchange.Frontends(), ", "))
	}
	return fe, nil
}

// report renders the counts and the unresolved constructs. Counts arrive as
// note-severity diagnostics, which is why a frontend needs no separate
// reporting interface to produce the block in §11.
func report(g *globals, diags interchange.Diagnostics) {
	var notes, problems []interchange.Diagnostic
	for _, d := range diags {
		if d.Severity == interchange.SeverityNote {
			notes = append(notes, d)
			continue
		}
		problems = append(problems, d)
	}
	for _, n := range notes {
		fmt.Fprintf(g.ui.Out, "  ✓ %s\n", n.Message)
	}
	if len(notes) > 0 && len(problems) > 0 {
		fmt.Fprintln(g.ui.Out)
	}
	if len(problems) > 0 {
		fmt.Fprintf(g.ui.Out, "  ⚠ %d construct(s) need a decision:\n\n", len(problems))
	}
	for _, p := range problems {
		loc := p.Path
		if p.Line > 0 {
			loc = fmt.Sprintf("%s:%d", p.Path, p.Line)
			if p.Col > 0 {
				loc = fmt.Sprintf("%s:%d", loc, p.Col)
			}
		}
		fmt.Fprintf(g.ui.Out, "    %s  %s\n", loc, p.Message)
		if p.Hint != "" {
			fmt.Fprintf(g.ui.Out, "                       → %s\n", p.Hint)
		}
		fmt.Fprintln(g.ui.Out)
	}
}

// detectFormat sniffs the source rather than trusting the extension: a .yaml
// file is OpenAPI, an AsyncAPI document or somebody's config, and the
// difference is in the first key. It names formats ix ships no adapter for,
// because "AsyncAPI, no adapter" is a useful answer and "unrecognised" is not.
func detectFormat(path string, b []byte) (kind, frontend string) {
	head := string(b)
	if len(head) > 4096 {
		head = head[:4096]
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case ext == ".proto":
		return "protobuf", "proto"
	case strings.Contains(head, "openapi:") || strings.Contains(head, `"openapi"`):
		return "OpenAPI " + versionAfter(head, "openapi"), "openapi"
	case strings.Contains(head, "swagger:") || strings.Contains(head, `"swagger"`):
		return "Swagger " + versionAfter(head, "swagger"), "openapi"
	case strings.Contains(head, "asyncapi:") || strings.Contains(head, `"asyncapi"`):
		return "AsyncAPI " + versionAfter(head, "asyncapi"), "asyncapi"
	case strings.Contains(head, "$schema"):
		return "JSON Schema", "jsonschema"
	case ext == ".graphql" || ext == ".gql" || strings.Contains(head, "type Query"):
		return "GraphQL SDL", "graphql"
	case ext == ".tsp":
		return "TypeSpec", "typespec"
	case slices.Contains([]string{".ix", ".yaml", ".yml"}, ext) && strings.Contains(head, "interchange:"):
		return "Interchange DSL", "dsl"
	case ext == ".ix":
		return "Interchange DSL", "dsl"
	}
	return "unrecognised", "unknown"
}

func versionAfter(head, key string) string {
	i := strings.Index(head, key)
	if i < 0 {
		return ""
	}
	rest := head[i+len(key):]
	rest = strings.TrimLeft(rest, `":' `)
	end := strings.IndexAny(rest, "\r\n\",' ")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}
