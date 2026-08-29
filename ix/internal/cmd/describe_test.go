package cmd

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darmawan01/interchange/ix/internal/annot"
	"github.com/darmawan01/interchange/ix/internal/bufx"
	"github.com/darmawan01/interchange/ix/internal/image"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// requireBuf skips rather than fails when buf is absent: ix drives buf, and a
// machine without it cannot run these tests at all.
func requireBuf(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf is not on PATH; ix shells out to it for every descriptor")
	}
}

func testdata(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureImage(t *testing.T, dir string) *image.Image {
	t.Helper()
	requireBuf(t)
	im, err := image.Build(&bufx.Runner{Dir: dir}, "api")
	if err != nil {
		t.Fatalf("building the fixture image: %v", err)
	}
	return im
}

func localFixture(path string) bool {
	return !strings.HasPrefix(path, "google/")
}

// The describe output is a documented contract in docs/11: it is what a
// reviewer reads to answer "what does this method expose, and who can reach
// it?". Golden files are how that stays true.
func TestDescribeGolden(t *testing.T) {
	im := fixtureImage(t, testdata(t, "fixture"))

	cases := []struct {
		name   string
		ref    string
		golden string
	}{
		{"every annotation", "CatalogService.ListProviders", "list_providers"},
		{"no annotation at all", "platform.catalog.v1.CatalogService.GetProvider", "get_provider"},
		{"bus only", "/platform.catalog.v1.CatalogService/ProviderChanged", "provider_changed"},
		{"internal", "CatalogService.DrainProvider", "drain_provider"},
		{"service-level default", "EventService.Publish", "publish"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md, err := im.FindMethod(tc.ref, localFixture)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			writeDescribe(&buf, annot.ForMethod(md, []string{"rpc", "rest"}))
			compareGolden(t, "describe_"+tc.golden+".txt", buf.Bytes())
		})
	}
}

// All three reference forms must resolve to the same RPC: a user types
// whichever one they have in front of them.
func TestFindMethodForms(t *testing.T) {
	im := fixtureImage(t, testdata(t, "fixture"))
	for _, ref := range []string{
		"CatalogService.ListProviders",
		"platform.catalog.v1.CatalogService.ListProviders",
		"/platform.catalog.v1.CatalogService/ListProviders",
	} {
		md, err := im.FindMethod(ref, localFixture)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if got := string(md.FullName()); got != "platform.catalog.v1.CatalogService.ListProviders" {
			t.Errorf("%s resolved to %s", ref, got)
		}
	}
	if _, err := im.FindMethod("CatalogService.NoSuchThing", localFixture); err == nil {
		t.Error("an unknown RPC resolved")
	}
	if _, err := im.FindMethod("ListProviders", localFixture); err == nil {
		t.Error("a bare method name resolved; it names no service")
	}
}

// The auth annotation belongs to an optional module ix does not import. If
// this test passes, ix read it out of the descriptor by number and field
// name, which is the whole mechanism.
func TestAuthAnnotationReadWithoutTheAuthModule(t *testing.T) {
	im := fixtureImage(t, testdata(t, "fixture"))
	md, err := im.FindMethod("CatalogService.ListProviders", localFixture)
	if err != nil {
		t.Fatal(err)
	}
	m := annot.ForMethod(md, nil)
	if m.Auth == nil {
		t.Fatal("the (auth) annotation was not read")
	}
	if m.Auth.Permission != "providers.read" {
		t.Errorf("permission = %q, want providers.read", m.Auth.Permission)
	}
	if strings.Join(m.Auth.AuthTypes, ",") != "SESSION,API_KEY,WORKLOAD" {
		t.Errorf("auth types = %v", m.Auth.AuthTypes)
	}
	if f, declared := annot.TenantField(md); f != "tenant_id" || !declared {
		t.Errorf("tenant field = %q declared=%v, want tenant_id declared", f, declared)
	}
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./... -update` to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("golden %s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
