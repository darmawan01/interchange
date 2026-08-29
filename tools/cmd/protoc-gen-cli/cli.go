package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/darmawan01/interchange"
	cliv1 "github.com/darmawan01/interchange/tools/gen/go/interchange/cli/v1"
	"github.com/darmawan01/interchange/tools/internal/genutil"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const requestJSONFlag = "request-json"

const (
	cobraPkg      = protogen.GoImportPath("github.com/spf13/cobra")
	clisupportPkg = protogen.GoImportPath("github.com/darmawan01/interchange/tools/clisupport")
)

type service struct {
	name     string
	goName   string
	fullName string
	commands []command
	covered  []string
	skipped  []string
	missing  []string
}

type command struct {
	goName    string
	procedure string
	pathSpec  []string
	short     string
	long      string
	input     protogen.GoIdent
	output    protogen.GoIdent
	args      []string // proto field names, in positional order
	flags     []flag
}

type flag struct {
	protoName string
	flagName  string
	goName    string
	varName   string
	kind      protoreflect.Kind
	optional  bool
}

func generate(p *protogen.Plugin, cfg *config) error {
	files := make([]*protogen.File, 0, len(p.Files))
	for _, f := range p.Files {
		if f.Generate {
			files = append(files, f)
		}
	}
	slices.SortFunc(files, func(a, b *protogen.File) int {
		return strings.Compare(a.Desc.Path(), b.Desc.Path())
	})
	for _, f := range files {
		if len(f.Services) == 0 {
			continue
		}
		svcs, err := model(f, cfg)
		if err != nil {
			return err
		}
		emit(p, f, svcs)
	}
	return nil
}

func model(f *protogen.File, cfg *config) ([]service, error) {
	out := make([]service, 0, len(f.Services))
	for _, s := range f.Services {
		svc := service{
			name:     string(s.Desc.Name()),
			goName:   s.GoName,
			fullName: string(s.Desc.FullName()),
		}
		for _, m := range s.Methods {
			loc := genutil.SourceLoc(f, m.Location)
			procedure := "/" + svc.fullName + "/" + string(m.Desc.Name())
			// Core resolves the extension: a descriptor that a schema
			// frontend built carries dynamicpb values, against which
			// proto.GetExtension reads a present annotation as absent.
			ann := commandOptions(interchange.MethodOptions(m.Desc))

			streaming := m.Desc.IsStreamingClient() || m.Desc.IsStreamingServer()
			switch {
			case ann == nil:
				// A streaming RPC has no command form to be missing, so it
				// does not count against coverage either.
				if streaming {
					continue
				}
				if cfg.requireAnnotation {
					return nil, fmt.Errorf("%s: %s has no (interchange.cli.v1.command) annotation and require_annotation=true; annotate it, or mark it skip: true to say the omission is deliberate",
						loc, m.Desc.FullName())
				}
				svc.missing = append(svc.missing, procedure)
				continue
			case ann.GetSkip():
				svc.skipped = append(svc.skipped, procedure)
				continue
			case streaming:
				return nil, fmt.Errorf("%s: %s is a streaming RPC and cannot be a command; interchange is unary only",
					loc, m.Desc.FullName())
			}

			cmd, err := buildCommand(m, procedure, ann)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", loc, m.Desc.FullName(), err)
			}
			svc.covered = append(svc.covered, procedure)
			svc.commands = append(svc.commands, cmd)
		}
		slices.SortFunc(svc.commands, func(a, b command) int {
			return strings.Compare(strings.Join(a.pathSpec, " "), strings.Join(b.pathSpec, " "))
		})
		slices.Sort(svc.covered)
		slices.Sort(svc.skipped)
		slices.Sort(svc.missing)
		out = append(out, svc)
	}
	slices.SortFunc(out, func(a, b service) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

func buildCommand(m *protogen.Method, procedure string, ann *cliv1.CommandOptions) (command, error) {
	if len(ann.GetPath()) == 0 {
		return command{}, fmt.Errorf("(command) has no path: a command with no place to mount is not a command")
	}
	cmd := command{
		goName:    m.GoName,
		procedure: procedure,
		pathSpec:  slices.Clone(ann.GetPath()),
		short:     ann.GetShort(),
		long:      ann.GetLong(),
		input:     m.Input.GoIdent,
		output:    m.Output.GoIdent,
		args:      slices.Clone(ann.GetArgs()),
	}
	if cmd.short == "" {
		cmd.short = firstLine(string(m.Comments.Leading))
	}

	fields := m.Input.Desc.Fields()
	for _, name := range cmd.args {
		fd := fields.ByName(protoreflect.Name(name))
		if fd == nil {
			return command{}, fmt.Errorf("(command).args names %q, which %s has no field called", name, m.Input.Desc.FullName())
		}
		if !settable(fd) {
			return command{}, fmt.Errorf("(command).args names %q, which is %s: a positional argument must be a scalar", name, describe(fd))
		}
	}

	for _, fld := range m.Input.Fields {
		fd := fld.Desc
		if slices.Contains(cmd.args, string(fd.Name())) {
			continue // already a positional argument
		}
		if fd.ContainingOneof() != nil && !fd.ContainingOneof().IsSynthetic() {
			continue // a oneof is a choice, not a flag
		}
		if !settable(fd) {
			continue // reachable through --request-json
		}
		if kebab(string(fd.Name())) == requestJSONFlag {
			return command{}, fmt.Errorf("field %q collides with the built-in --%s flag", fd.Name(), requestJSONFlag)
		}
		cmd.flags = append(cmd.flags, flag{
			protoName: string(fd.Name()),
			flagName:  kebab(string(fd.Name())),
			goName:    fld.GoName,
			varName:   "flag" + fld.GoName,
			kind:      fd.Kind(),
			optional:  fd.HasPresence() && fd.Kind() != protoreflect.EnumKind,
		})
	}
	slices.SortFunc(cmd.flags, func(a, b flag) int { return strings.Compare(a.flagName, b.flagName) })
	return cmd, nil
}

// settable reports whether a field can be carried by one command-line token.
// Everything else -- repeated, map, message, group -- goes through
// --request-json, which is what keeps a generated command a superset of the
// RPC rather than a lossy subset of it.
func settable(fd protoreflect.FieldDescriptor) bool {
	if fd.IsList() || fd.IsMap() {
		return false
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return false
	}
	return true
}

func describe(fd protoreflect.FieldDescriptor) string {
	switch {
	case fd.IsMap():
		return "a map"
	case fd.IsList():
		return "repeated"
	default:
		return "of kind " + fd.Kind().String()
	}
}

func commandOptions(opts *descriptorpb.MethodOptions) *cliv1.CommandOptions {
	if opts == nil || !proto.HasExtension(opts, cliv1.E_Command) {
		return nil
	}
	o, _ := proto.GetExtension(opts, cliv1.E_Command).(*cliv1.CommandOptions)
	return o
}

func kebab(s string) string { return strings.ReplaceAll(s, "_", "-") }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
