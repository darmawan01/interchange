// Package authgen is the body of protoc-gen-authz, kept out of main so the
// golden, determinism and build-failure tests can run it in process rather
// than by shelling out to a binary.
package authgen

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/darmawan01/interchange/auth"
	"google.golang.org/protobuf/compiler/protogen"
)

// Options are the plugin's parameters, as protoc passes them after the colon:
//
//	--authz_out=package=authz,on_missing_annotation=error:gen/go/authz
type Options struct {
	// Package is the Go package name of the generated file.
	Package string

	// ImportPath is its full Go import path. protogen needs one; the emitted
	// file has no imports, so it only matters to protoc-gen-go-style
	// tooling that reads the response.
	ImportPath string

	// Filename is the generated file, relative to the plugin's out directory.
	Filename string

	// OnMissingAnnotation is the same policy the interceptor runs under, and
	// the reason the plugin exists: the runtime check cannot see an RPC that
	// nobody called, and this can.
	OnMissingAnnotation auth.Strictness

	// KnownAtoms, when set, is the closed set of permission atoms the tree is
	// allowed to declare. An atom outside it is a typo -- a phantom
	// permission that no role will ever grant, on an RPC that will therefore
	// deny everyone the day it ships.
	KnownAtoms []string

	// Warn receives the warn policy's warnings. Nil means stderr, which is
	// where protoc and buf surface a plugin's diagnostics.
	Warn io.Writer
}

// DefaultOptions are the plugin's defaults: strict, one file, package authz.
func DefaultOptions() Options {
	return Options{
		Package:             "authz",
		ImportPath:          "authz",
		Filename:            "permissions.authz.go",
		OnMissingAnnotation: auth.StrictError,
	}
}

// Set applies one key=value plugin parameter. It is the ParamFunc protogen
// calls, so an unknown parameter fails the build rather than being ignored.
func (o *Options) Set(key, value string) error {
	switch key {
	case "package":
		o.Package = value
	case "import_path":
		o.ImportPath = value
	case "filename":
		o.Filename = value
	case "on_missing_annotation":
		s, err := auth.ParseStrictness(value)
		if err != nil {
			return err
		}
		o.OnMissingAnnotation = s
	case "known_atoms":
		// "+"-separated: protoc already claimed the comma as its parameter
		// separator.
		for _, a := range strings.Split(value, "+") {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			if _, err := auth.ParseAtom(a); err != nil {
				return err
			}
			o.KnownAtoms = append(o.KnownAtoms, a)
		}
	default:
		return fmt.Errorf("protoc-gen-authz: unknown parameter %q", key)
	}
	return nil
}

// Rule is one row of the table: what the contract says about one RPC.
type Rule struct {
	Procedure  string
	Permission string
	Public     bool
	Platform   bool
	AuthTypes  []string
}

// Generate walks the files marked for generation, enforces this module's
// policy on their annotations, and emits the permission table.
//
// The plugin is deterministic by construction: rules are sorted by procedure,
// the header carries no timestamp and no version string, and nothing is read
// from the environment. A generated file that changes when nothing changed
// makes the drift gate flap, and a gate nobody trusts is not a gate.
func Generate(p *protogen.Plugin, opts Options) error {
	if opts.Package == "" {
		opts.Package = DefaultOptions().Package
	}
	if opts.Filename == "" {
		opts.Filename = DefaultOptions().Filename
	}
	if opts.ImportPath == "" {
		opts.ImportPath = opts.Package
	}
	warn := opts.Warn
	if warn == nil {
		warn = os.Stderr
	}
	known := map[string]bool{}
	for _, a := range opts.KnownAtoms {
		known[a] = true
	}

	var rules []Rule
	var sources []string
	for _, file := range p.Files {
		// p.Files carries every transitive import. Without this guard the
		// plugin emits rows for descriptor.proto.
		if !file.Generate {
			continue
		}
		sources = append(sources, file.Desc.Path())
		for _, svc := range file.Services {
			for _, m := range svc.Methods {
				ann := auth.AnnotationOf(m.Desc)
				procedure := "/" + string(svc.Desc.FullName()) + "/" + string(m.Desc.Name())

				if !ann.Present {
					switch opts.OnMissingAnnotation {
					case auth.StrictWarn:
						fmt.Fprintf(warn, "protoc-gen-authz: %s: no (interchange.auth.v1.auth) annotation; omitted from the table\n", m.Desc.FullName())
						continue
					case auth.StrictIgnore:
						continue
					default:
						return fmt.Errorf("%s: no (interchange.auth.v1.auth) annotation; a public RPC must say public: true (relax with on_missing_annotation=warn|ignore)", m.Desc.FullName())
					}
				}

				atom := ann.Permission.Atom()
				if !ann.Public {
					// A typo is always an error, whatever the missing-annotation
					// policy says: an annotation that is present and wrong was
					// reviewed and believed.
					if atom == "" {
						return fmt.Errorf("%s: (auth) declares %s, which names no permission atom", m.Desc.FullName(), ann.Permission)
					}
					if len(known) > 0 && !known[atom] {
						return fmt.Errorf("%s: unknown permission %q (known: %s)", m.Desc.FullName(), atom, strings.Join(opts.KnownAtoms, ", "))
					}
				}

				rule := Rule{Procedure: procedure, Permission: atom, Public: ann.Public, Platform: ann.Platform}
				for _, t := range ann.AuthTypes {
					rule.AuthTypes = append(rule.AuthTypes, t.String())
				}
				rules = append(rules, rule)
			}
		}
	}

	// Go map iteration is random and protoc's file order is not ours to
	// depend on. Sort, then emit.
	slices.SortFunc(rules, func(a, b Rule) int { return strings.Compare(a.Procedure, b.Procedure) })
	slices.Sort(sources)

	atoms := map[string]bool{}
	for _, r := range rules {
		if r.Permission != "" {
			atoms[r.Permission] = true
		}
	}
	atomList := make([]string, 0, len(atoms))
	for a := range atoms {
		atomList = append(atomList, a)
	}
	slices.Sort(atomList)

	emit(p.NewGeneratedFile(opts.Filename, protogen.GoImportPath(opts.ImportPath)), opts, sources, rules, atomList)
	return nil
}

func emit(g *protogen.GeneratedFile, opts Options, sources []string, rules []Rule, atoms []string) {
	g.P("// Code generated by protoc-gen-authz. DO NOT EDIT.")
	g.P("//")
	g.P("// The build-time half of \"enforce twice\": this table and the authz")
	g.P("// interceptor read the same (interchange.auth.v1.auth) annotation. The table")
	g.P("// catches a missing or unknown annotation at build time and can be handed to")
	g.P("// an edge gateway; the interceptor decides at runtime, with the message in")
	g.P("// hand. A procedure with no row here has no declaration -- treat it as denied.")
	g.P("//")
	g.P("// sources:")
	for _, s := range sources {
		g.P("//   ", s)
	}
	g.P()
	g.P("package ", opts.Package)
	g.P()
	g.P("// Rule is what the contract declares about one RPC. Treat it as read-only.")
	g.P("type Rule struct {")
	g.P("\t// Procedure is \"/pkg.Service/Method\" -- the same string the")
	g.P("\t// interceptor, the metrics label and the trace span use.")
	g.P("\tProcedure string")
	g.P()
	g.P("\t// Permission is the atom the RPC requires, \"\" on a public RPC.")
	g.P("\tPermission string")
	g.P()
	g.P("\t// Public is the explicit opt-out: absent != public, so this is only")
	g.P("\t// ever true because the contract said so.")
	g.P("\tPublic bool")
	g.P()
	g.P("\t// Platform marks a cross-tenant RPC: its request carries no tenant.")
	g.P("\tPlatform bool")
	g.P()
	g.P("\t// AuthTypes are the credential kinds the RPC accepts; empty means any.")
	g.P("\tAuthTypes []string")
	g.P("}")
	g.P()
	g.P("// rules is sorted by procedure so this file is byte-identical for")
	g.P("// byte-identical input.")
	g.P("var rules = []Rule{")
	for _, r := range rules {
		g.P("\t{")
		g.P("\t\tProcedure: ", strconv.Quote(r.Procedure), ",")
		if r.Permission != "" {
			g.P("\t\tPermission: ", strconv.Quote(r.Permission), ",")
		}
		if r.Public {
			g.P("\t\tPublic: true,")
		}
		if r.Platform {
			g.P("\t\tPlatform: true,")
		}
		if len(r.AuthTypes) > 0 {
			g.P("\t\tAuthTypes: []string{", quoteList(r.AuthTypes), "},")
		}
		g.P("\t},")
	}
	g.P("}")
	g.P()
	g.P("// atoms is every permission the contract declares, sorted. A role table")
	g.P("// that grants an atom outside this set grants nothing.")
	g.P("var atoms = []string{", quoteList(atoms), "}")
	g.P()
	g.P("// Rules returns every declared rule, sorted by procedure.")
	g.P("func Rules() []Rule { return append([]Rule(nil), rules...) }")
	g.P()
	g.P("// Permissions returns the procedure -> rule table. An absent procedure has")
	g.P("// no declaration; a gateway reading this must deny it rather than pass it.")
	g.P("func Permissions() map[string]Rule {")
	g.P("\tout := make(map[string]Rule, len(rules))")
	g.P("\tfor _, r := range rules {")
	g.P("\t\tout[r.Procedure] = r")
	g.P("\t}")
	g.P("\treturn out")
	g.P("}")
	g.P()
	g.P("// Atoms returns every permission atom the contract declares, sorted.")
	g.P("func Atoms() []string { return append([]string(nil), atoms...) }")
}

func quoteList(in []string) string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strconv.Quote(s)
	}
	return strings.Join(out, ", ")
}
