// Package config reads interchange.yaml -- the one file at the repo root that
// every ix command consults. The schema is docs/10-extensibility.md
// §Configuration; this package models all of it, including the optional auth
// block, which ix reads and reports but takes no position on.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Name is the file every command looks for, walking up from the working
// directory.
const Name = "interchange.yaml"

// ErrNotFound is returned when no interchange.yaml exists anywhere above the
// working directory. `ix doctor` treats it as "not a project yet" rather than
// as a broken project; every other command treats it as fatal, because there
// is nothing to act on.
var ErrNotFound = errors.New("no " + Name + " found")

// Config is interchange.yaml.
type Config struct {
	Version    int        `yaml:"version"`
	Sources    []Source   `yaml:"sources"`
	Transports Transports `yaml:"transports"`
	Generate   []Generate `yaml:"generate"`

	// Managed is buf's managed-mode block. It is not part of the docs/10
	// schema because it is buf's vocabulary rather than interchange's -- but a
	// Go project needs go_package_prefix somewhere, and a second file to keep
	// in sync is a second file that can drift.
	//
	// It is modelled rather than passed through as an opaque node so that an
	// unknown key is rejected here, with the file and line, instead of by buf
	// three steps later.
	Managed *Managed `yaml:"managed,omitempty"`

	// Auth is an optional module's block. ix parses it so `doctor` and
	// `lint` can report what policy is configured; nothing in ix enforces an
	// authorization opinion of its own.
	Auth *Auth `yaml:"auth,omitempty"`

	// Root is the directory holding the file. Every relative path in the
	// config resolves against it, not against the working directory, so a
	// command run from a subdirectory behaves the same.
	Root string `yaml:"-"`

	// Path is the file itself.
	Path string `yaml:"-"`
}

// Source is one input tree and the frontend that reads it.
type Source struct {
	Path     string `yaml:"path"`
	Frontend string `yaml:"frontend"`
	Sidecar  string `yaml:"sidecar,omitempty"`
}

// Transports is the project-wide fan-out default and the drivers in play.
type Transports struct {
	// Default is the road set an RPC with no (transports) annotation takes.
	Default []string `yaml:"default"`

	Drivers []string `yaml:"drivers"`
}

// Generate is one generator invocation. Exactly one of Plugin's two forms is
// used: a remote plugin reference ("buf.build/protocolbuffers/go") or a local
// path ("./bin/protoc-gen-bus").
type Generate struct {
	Plugin   string   `yaml:"plugin"`
	Out      string   `yaml:"out"`
	Opt      []string `yaml:"opt,omitempty"`
	Strategy string   `yaml:"strategy,omitempty"`

	// IncludeImports emits code for the input's dependencies as well as the
	// input itself. A generator whose output names its imports' descriptors
	// at runtime -- protobuf-es does -- produces something that will not
	// typecheck without it. Without this field such a generator has to be
	// kept out of interchange.yaml entirely, and its output then sits outside
	// the drift gate, which is the one place output must never sit.
	IncludeImports bool `yaml:"include_imports,omitempty"`
}

// Local reports whether this generator is a local binary rather than a remote
// plugin. buf distinguishes the two with different template keys.
func (g Generate) Local() bool {
	if strings.HasPrefix(g.Plugin, ".") || strings.HasPrefix(g.Plugin, "/") {
		return true
	}
	// A remote reference is host-qualified: its first path element is a
	// hostname, so it contains a dot. Anything else is a path on disk
	// ("node_modules/.bin/protoc-gen-es") or a binary looked up on PATH.
	//
	// Testing for a slash alone read every relative path as a remote, and
	// buf then reported "the server hosted at that remote is unavailable --
	// are you sure node_modules is a valid remote address?", which is a long
	// way from the actual mistake.
	first, _, hasSlash := strings.Cut(g.Plugin, "/")
	if !hasSlash {
		return true
	}
	return !strings.Contains(first, ".")
}

// Binary is the local plugin's path, or "" for a remote plugin.
func (g Generate) Binary() string {
	if !g.Local() {
		return ""
	}
	return g.Plugin
}

// Managed is buf's managed mode, mirrored field for field.
type Managed struct {
	Enabled  bool              `yaml:"enabled"`
	Disable  []ManagedDisable  `yaml:"disable,omitempty"`
	Override []ManagedOverride `yaml:"override,omitempty"`
}

// ManagedDisable exempts files from managed mode.
type ManagedDisable struct {
	FileOption  string `yaml:"file_option,omitempty"`
	FieldOption string `yaml:"field_option,omitempty"`
	Module      string `yaml:"module,omitempty"`
	Path        string `yaml:"path,omitempty"`
}

// ManagedOverride sets a file or field option across the tree.
type ManagedOverride struct {
	FileOption  string `yaml:"file_option,omitempty"`
	FieldOption string `yaml:"field_option,omitempty"`
	Value       string `yaml:"value"`
	Module      string `yaml:"module,omitempty"`
	Path        string `yaml:"path,omitempty"`
}

// Auth is the optional /auth module's configuration.
type Auth struct {
	Provider string `yaml:"provider"`

	// OnMissingAnnotation is that module's policy, not a framework rule:
	// error | warn | ignore.
	OnMissingAnnotation string `yaml:"on_missing_annotation"`
}

// Find walks up from dir looking for interchange.yaml.
func Find(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(abs, Name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("%w in %s or any parent directory (run `ix init`)", ErrNotFound, dir)
		}
		abs = parent
	}
}

// Load finds and parses interchange.yaml, starting at dir.
func Load(dir string) (*Config, error) {
	p, err := Find(dir)
	if err != nil {
		return nil, err
	}
	return LoadFile(p)
}

// LoadFile parses a specific interchange.yaml.
func LoadFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseBytes(b, path)
}

// ParseBytes parses a config that is not (yet) on disk. `ix plugin` uses it
// to check an edit before overwriting the user's file.
func ParseBytes(b []byte, path string) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	// Unknown keys are an error: a typo'd key that is silently ignored is a
	// setting the user believes is in effect.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Path = abs
	c.Root = filepath.Dir(abs)
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate rejects a config that would produce a confusing failure three
// steps later in buf.
func (c *Config) Validate() error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("version: want 1, got %d", c.Version))
	}
	if len(c.Sources) == 0 {
		errs = append(errs, errors.New("sources: at least one source is required"))
	}
	for i, s := range c.Sources {
		if s.Path == "" {
			errs = append(errs, fmt.Errorf("sources[%d]: path is required", i))
		}
		switch s.Frontend {
		case "proto":
		case "":
			errs = append(errs, fmt.Errorf("sources[%d]: frontend is required", i))
		default:
			// Other frontends are a real extension point; ix cannot run one
			// it has no adapter for, and says so rather than ignoring it.
			errs = append(errs, fmt.Errorf("sources[%d]: frontend %q is not wired into this build of ix (have: proto)", i, s.Frontend))
		}
	}
	for i, g := range c.Generate {
		if g.Plugin == "" {
			errs = append(errs, fmt.Errorf("generate[%d]: plugin is required", i))
		}
		if g.Out == "" {
			errs = append(errs, fmt.Errorf("generate[%d]: out is required", i))
		}
		switch g.Strategy {
		case "", "directory", "all":
		default:
			errs = append(errs, fmt.Errorf("generate[%d]: strategy %q: want \"directory\" or \"all\"", i, g.Strategy))
		}
	}
	for _, t := range c.Transports.Default {
		if !validTransport(t) {
			errs = append(errs, fmt.Errorf("transports.default: unknown transport %q (want rpc, rest, bus, mqtt or ws)", t))
		}
	}
	return errors.Join(errs...)
}

func validTransport(t string) bool {
	switch t {
	case "rpc", "rest", "bus", "mqtt", "ws":
		return true
	}
	return false
}

// ProtoDirs is the set of directories to hand buf as inputs, derived from the
// proto sources' glob patterns. buf takes a directory, not a glob, so
// "api/**/*.proto" becomes "api".
func (c *Config) ProtoDirs() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range c.Sources {
		if s.Frontend != "proto" {
			continue
		}
		d := globRoot(s.Path)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

func globRoot(pattern string) string {
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	var dirs []string
	for _, p := range parts {
		if strings.ContainsAny(p, "*?[") {
			break
		}
		dirs = append(dirs, p)
	}
	if len(dirs) == 0 {
		return "."
	}
	joined := strings.Join(dirs, "/")
	// A pattern with no wildcard at all names a file; its directory is the
	// input.
	if len(dirs) == len(parts) && strings.HasSuffix(joined, ".proto") {
		return filepath.Dir(joined)
	}
	return joined
}

// OutDirs is every generator's output directory, deduplicated and in config
// order. `verify` diffs exactly these.
func (c *Config) OutDirs() []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range c.Generate {
		if g.Out == "" || seen[g.Out] {
			continue
		}
		seen[g.Out] = true
		out = append(out, g.Out)
	}
	return out
}
