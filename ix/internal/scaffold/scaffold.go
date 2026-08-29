// Package scaffold writes a new interchange project.
//
// The design goal is `ix init` to a generated, typed client in under a
// minute, with no protobuf knowledge required (docs/11). Two consequences
// shape what is written here.
//
// First, the scaffold carries its own copies of the interchange annotation
// protos. A new project cannot depend on a schema registry module that may
// not exist yet, and an annotation that has to be fetched before the first
// build is an annotation the user meets as an error.
//
// Second, the starter service uses no external proto dependency at all, so
// `ix lint` passes on a machine with no network. Adding google.api.http for
// a REST road is one commented-out block away, and the comment says how.
package scaffold

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"
)

//go:embed protos
var protos embed.FS

// Options describes the project to write.
type Options struct {
	// Dir is the project root.
	Dir string

	// Name is the proto package's first segment and the service's prefix:
	// "todo" gives package todo.v1 and service TodoService.
	Name string

	// GoModule is the Go import path generated code is written under. Empty
	// means the scaffold omits managed mode, which a non-Go project wants.
	GoModule string

	// Force overwrites existing files.
	Force bool
}

// File is one scaffolded file.
type File struct {
	Path string
	Body []byte
	Mode os.FileMode
}

// Plan renders every file without touching the disk, so `ix init --dry-run`
// and the tests see exactly what Write would produce.
func Plan(o Options) ([]File, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	d := newData(o)

	var files []File
	add := func(path, body string) {
		files = append(files, File{Path: path, Body: []byte(body), Mode: 0o644})
	}

	for _, t := range []struct{ path, tmpl string }{
		{"interchange.yaml", interchangeYAML},
		{"buf.yaml", bufYAML},
		{"Makefile", makefile},
		{".github/workflows/interchange.yml", workflow},
		{filepath.Join("api", d.Name, "v1", d.Name+".proto"), starterProto},
	} {
		body, err := render(t.path, t.tmpl, d)
		if err != nil {
			return nil, err
		}
		add(t.path, body)
	}

	// The annotation protos travel with the scaffold. They are the same
	// files core ships; a project that edits them has forked the contract
	// layer, which is why they carry the note that says so.
	err := fs.WalkDir(protos, "protos", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := protos.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "protos/")
		add(filepath.Join("api", filepath.FromSlash(rel)), string(b))
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Write renders and writes the project, returning the paths written.
func Write(o Options) ([]string, error) {
	files, err := Plan(o)
	if err != nil {
		return nil, err
	}
	if !o.Force {
		var clash []string
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(o.Dir, f.Path)); err == nil {
				clash = append(clash, f.Path)
			}
		}
		if len(clash) > 0 {
			return nil, fmt.Errorf("refusing to overwrite: %s (pass --force)", strings.Join(clash, ", "))
		}
	}
	var written []string
	for _, f := range files {
		abs := filepath.Join(o.Dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(abs, f.Body, f.Mode); err != nil {
			return written, err
		}
		written = append(written, f.Path)
	}
	return written, nil
}

type data struct {
	Name    string // "todo"
	Pkg     string // "todo.v1"
	Entity  string // "Todo"
	Service string // "TodoService"

	// EnumPrefix is the UPPER_SNAKE prefix every value of the starter enum
	// carries, which is what buf's ENUM_VALUE_PREFIX rule requires.
	EnumPrefix string // "TODO_STATUS"

	GoModule string
	GenGo    bool
}

func newData(o Options) data {
	entity := title(o.Name)
	return data{
		Name:       o.Name,
		Pkg:        o.Name + ".v1",
		Entity:     entity,
		Service:    entity + "Service",
		EnumPrefix: strings.ToUpper(o.Name) + "_STATUS",
		GoModule:   o.GoModule,
		GenGo:      o.GoModule != "",
	}
}

func (o *Options) validate() error {
	if o.Name == "" {
		o.Name = "example"
	}
	if !isProtoIdent(o.Name) {
		return fmt.Errorf("--name %q: a proto package segment must be lower-case letters, digits and underscores, starting with a letter", o.Name)
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	return nil
}

func isProtoIdent(s string) bool {
	if s == "" || !unicode.IsLower(rune(s[0])) {
		return false
	}
	for _, r := range s {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func title(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		if r == '_' {
			up = true
			continue
		}
		if up {
			b.WriteRune(unicode.ToUpper(r))
			up = false
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func render(name, tmpl string, d data) (string, error) {
	t, err := template.New(name).Parse(tmpl)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, d); err != nil {
		return "", err
	}
	return sb.String(), nil
}
