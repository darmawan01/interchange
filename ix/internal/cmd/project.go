package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/darmawan01/interchange/ix/internal/annot"
	"github.com/darmawan01/interchange/ix/internal/bufx"
	"github.com/darmawan01/interchange/ix/internal/config"
	"github.com/darmawan01/interchange/ix/internal/image"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Project is one loaded interchange.yaml plus the tools that act on it.
// Every command that reads the contract goes through here, so "which
// directory, which buf, which descriptor set" is answered once.
type Project struct {
	Cfg *config.Config
	Buf *bufx.Runner
	UI  *UI

	img *image.Image
}

func openProject(g *globals) (*Project, error) {
	dir := g.dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = wd
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	return &Project{Cfg: cfg, Buf: bufRunner(g, cfg.Root), UI: g.ui}, nil
}

func bufRunner(g *globals, dir string) *bufx.Runner {
	return &bufx.Runner{
		Bin: g.buf, Dir: dir, Verbose: g.verbose,
		Stdout: g.ui.Out, Stderr: g.ui.Err,
	}
}

// Image builds the descriptor set once per process. Every command that reads
// annotations reads them from here, which is what makes `ix describe` and the
// runtime agree: same descriptors, same options.
func (p *Project) Image() (*image.Image, error) {
	if p.img != nil {
		return p.img, nil
	}
	inputs := p.Cfg.ProtoDirs()
	var im *image.Image
	var err error
	switch len(inputs) {
	case 0:
		return nil, fmt.Errorf("no proto sources in %s", p.Cfg.Path)
	case 1:
		im, err = image.Build(p.Buf, inputs[0])
	default:
		// Several roots means the project is a buf workspace; buf builds the
		// workspace, and the roots only narrow what ix treats as local.
		im, err = image.Build(p.Buf)
	}
	if err != nil {
		return nil, err
	}
	p.img = im
	return im, nil
}

// Local reports whether a descriptor file belongs to this project rather than
// to a dependency it imported.
func (p *Project) Local() func(string) bool {
	roots := p.Cfg.ProtoDirs()
	if len(roots) == 1 {
		// A single input was handed to buf directly, so paths in the image
		// are already relative to it.
		return func(path string) bool { return !isDep(path) }
	}
	return image.LocalFiles(nil, roots)
}

func isDep(path string) bool {
	for _, p := range []string{"google/", "buf/", "grpc/", "protoc-gen-", "validate/"} {
		if len(path) >= len(p) && path[:len(p)] == p {
			return true
		}
	}
	return false
}

// Methods is every RPC in the project's own protos, with annotations
// resolved.
func (p *Project) Methods() ([]*annot.Method, error) {
	im, err := p.Image()
	if err != nil {
		return nil, err
	}
	local := p.Local()
	var out []*annot.Method
	for _, md := range im.Methods(local) {
		out = append(out, annot.ForMethod(md, p.Cfg.Transports.Default))
	}
	return out, nil
}

// FindMethod resolves an RPC reference against the project.
func (p *Project) FindMethod(ref string) (protoreflect.MethodDescriptor, error) {
	im, err := p.Image()
	if err != nil {
		return nil, err
	}
	return im.FindMethod(ref, p.Local())
}

// Rel renders a path relative to the project root, which is how every
// diagnostic names a file.
func (p *Project) Rel(path string) string {
	if r, err := filepath.Rel(p.Cfg.Root, path); err == nil {
		return r
	}
	return path
}
