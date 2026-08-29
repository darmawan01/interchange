package nolink_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	interchange "github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/frontend/openapi"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const doc = `openapi: 3.0.3
info: {title: Things, version: "1"}
paths:
  /v1/things:
    get:
      x-interchange-auth:
        auth_types: [SESSION]
        permission: {resource: things, verb: READ}
      responses:
        "204": {description: ok}
`

func sources() interchange.Sources {
	return interchange.Sources{
		Root:    ".",
		Paths:   []string{"things.yaml"},
		Content: map[string][]byte{"things.yaml": []byte(doc)},
	}
}

var opt = interchange.Options{Package: "things.v1"}

// Without the optional module's descriptors -- neither linked in nor passed
// in Options.Deps -- an auth annotation is a loud failure at the annotation,
// not a silently dropped security posture and not a compiler error against
// source the author never wrote.
func TestAuthAnnotationNeedsItsDescriptors(t *testing.T) {
	set, diags, err := openapi.New().Parse(context.Background(), sources(), opt)
	if err == nil {
		t.Fatal("Parse succeeded without the auth descriptors")
	}
	if set != nil {
		t.Error("Parse returned descriptors alongside an error")
	}
	for _, d := range diags {
		if d.Severity != interchange.SeverityError || !strings.Contains(d.Message, "interchange/auth/v1/auth.proto") {
			continue
		}
		if d.Path != "things.yaml" || d.Line == 0 || d.Col == 0 {
			t.Errorf("wrong location: %s", d)
		}
		if !strings.Contains(d.Hint, "Options.Deps") {
			t.Errorf("hint = %q", d.Hint)
		}
		return
	}
	t.Fatalf("no diagnostic naming the missing auth descriptors; got:\n%v", diags)
}

// Core's transports annotation is a different case: it is core's own, this
// module already depends on core, and it stays resolvable with no Deps. So is
// google.api.http, which every import derives.
func TestCoreAnnotationsNeedNoDeps(t *testing.T) {
	body := strings.Replace(doc, `      x-interchange-auth:
        auth_types: [SESSION]
        permission: {resource: things, verb: READ}
`, "      x-interchange-transports: [RPC, BUS]\n", 1)

	in := sources()
	in.Content["things.yaml"] = []byte(body)
	quiet := opt
	quiet.Params = map[string]string{"on_missing_auth": "ignore"}
	if _, diags, err := openapi.New().Parse(context.Background(), in, quiet); err != nil {
		t.Fatalf("Parse: %v\n%v", err, diags)
	}
}

// The other half of the claim: with the real annotation protos supplied as
// descriptors, the same document imports cleanly. This is the production path
// -- `ix` hands over the annotation protos of every module installed -- and
// nothing in this binary links /auth.
func TestAuthAnnotationFromDeps(t *testing.T) {
	deps := compileFromTree(t,
		"auth/api", "interchange/auth/v1/auth.proto",
		"tools/api", "interchange/cli/v1/cli.proto")

	withDeps := opt
	withDeps.Deps = deps
	set, diags, err := openapi.New().Parse(context.Background(), sources(), withDeps)
	if err != nil {
		t.Fatalf("Parse with Deps: %v\n%v", err, diags)
	}
	if set == nil {
		t.Fatal("no descriptors")
	}
	var found bool
	for _, f := range set.GetFile() {
		if f.GetName() != "interchange/auth/v1/auth.proto" {
			continue
		}
		found = true
	}
	if !found {
		t.Error("the auth descriptors Deps supplied are not in the emitted set")
	}
}

// compileFromTree compiles proto files out of the repo's proto trees. A test
// may read the filesystem; the frontend may not, which is the whole reason
// Options.Deps exists.
func compileFromTree(t *testing.T, pairs ...string) *descriptorpb.FileDescriptorSet {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(self), "..", "..", "..", "..")

	sources := map[string][]byte{}
	var names []string
	for i := 0; i+1 < len(pairs); i += 2 {
		dir, name := pairs[i], pairs[i+1]
		b, err := os.ReadFile(filepath.Join(root, dir, name))
		if err != nil {
			t.Fatalf("reading the annotation protos out of the repo: %v", err)
		}
		sources[name] = b
		names = append(names, name)
		// auth.proto imports permissions.proto from the same tree.
		if name == "interchange/auth/v1/auth.proto" {
			p := "interchange/auth/v1/permissions.proto"
			b, err := os.ReadFile(filepath.Join(root, dir, p))
			if err != nil {
				t.Fatal(err)
			}
			sources[p] = b
		}
	}

	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(protocompile.ResolverFunc(
			func(path string) (protocompile.SearchResult, error) {
				b, ok := sources[path]
				if !ok {
					return protocompile.SearchResult{}, os.ErrNotExist
				}
				return protocompile.SearchResult{Source: strings.NewReader(string(b))}, nil
			})),
	}
	files, err := c.Compile(context.Background(), names...)
	if err != nil {
		t.Fatalf("compiling the annotation protos: %v", err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(fd protoreflect.FileDescriptor)
	add = func(fd protoreflect.FileDescriptor) {
		if seen[fd.Path()] {
			return
		}
		seen[fd.Path()] = true
		for i := range fd.Imports().Len() {
			add(fd.Imports().Get(i).FileDescriptor)
		}
		set.File = append(set.File, protodesc.ToFileDescriptorProto(fd))
	}
	for _, fd := range files {
		add(fd)
	}
	return set
}
