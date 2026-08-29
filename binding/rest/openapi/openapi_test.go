package openapi_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darmawan01/interchange/binding/rest/internal/testfixture"
	"github.com/darmawan01/interchange/binding/rest/openapi"
)

var update = flag.Bool("update", false, "rewrite the golden document")

const golden = "testdata/probe.openapi.json"

func emit(t *testing.T) []byte {
	t.Helper()
	fds, err := testfixture.FileDescriptorSet()
	if err != nil {
		t.Fatal(err)
	}
	out, err := openapi.FromFileDescriptorSet(fds, openapi.Options{
		Title:       "Probe API",
		Version:     "1.0.0",
		Description: "The fixture service, as a partner sees it.",
		Servers:     []string{"https://api.example.com"},
		Files:       []string{testfixture.FilePath},
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestGolden is the drift gate in miniature: the emitted document is a
// committed artifact, so a change to it is a change somebody has to look at.
func TestGolden(t *testing.T) {
	got := emit(t)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run `go test ./openapi -update` to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the emitted document drifted from %s:\n%s", golden, got)
	}
}

// TestDeterministic: same input, same bytes. Without this the drift gate
// flaps and nobody trusts it.
func TestDeterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		if a, b := emit(t), emit(t); !bytes.Equal(a, b) {
			t.Fatalf("two emissions differ:\n%s\n---\n%s", a, b)
		}
	}
}

// TestOffRoadMethodsAreAbsent: the document is what partners see. A method
// that does not declare the REST road has no URI to publish, and an internal
// one appearing here is a leak.
func TestOffRoadMethodsAreAbsent(t *testing.T) {
	got := emit(t)

	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/v1/rpc-only/{probe_id}", "/v1/reconcile"} {
		if _, ok := doc.Paths[path]; ok {
			t.Errorf("%s is in the document", path)
		}
	}
	for _, name := range []string{"RpcOnlyProbe", "ReconcileProbes"} {
		if strings.Contains(string(got), name) {
			t.Errorf("%s is named somewhere in the document", name)
		}
	}

	for _, path := range []string{"/v1/probes", "/v1/probes/{probe_id}", "/v1/probes/{probe_id}/failure"} {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("%s is missing", path)
		}
	}
	if _, ok := doc.Components.Schemas["interchange.common.v1.Problem"]; !ok {
		t.Error("the problem+json schema is not in components")
	}
}

// TestPathAndQueryParameters: the parameters come from the http rule and the
// request message, not from a second description of the API.
func TestPathAndQueryParameters(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
			RequestBody *struct {
				Content map[string]struct {
					Schema struct {
						Properties map[string]json.RawMessage `json:"properties"`
						Ref        string                     `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(emit(t), &doc); err != nil {
		t.Fatal(err)
	}

	get := doc.Paths["/v1/probes/{probe_id}"]["get"]
	var names []string
	for _, p := range get.Parameters {
		names = append(names, p.In+":"+p.Name)
	}
	for _, want := range []string{"path:probe_id", "query:page.page_size", "query:page.page_token"} {
		if !contains(names, want) {
			t.Errorf("%s is missing from %v", want, names)
		}
	}
	if get.RequestBody != nil {
		t.Error("a GET was given a request body")
	}

	post := doc.Paths["/v1/probes"]["post"]
	if post.RequestBody == nil {
		t.Fatal("the POST has no request body")
	}
	body := post.RequestBody.Content["application/json"].Schema
	if body.Ref != "#/components/schemas/rest.test.v1.CreateProbeRequest" {
		t.Errorf("body schema is %+v", body)
	}
}

// TestSnakeCaseProperties: the document honours the same casing decision the
// surface does. A spec that says probeId over a wire that says probe_id is
// worse than no spec.
func TestSnakeCaseProperties(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(emit(t), &doc); err != nil {
		t.Fatal(err)
	}
	probe := doc.Components.Schemas["rest.test.v1.Probe"]
	for _, name := range []string{"probe_id", "display_name", "attempt_count", "created_at"} {
		if _, ok := probe.Properties[name]; !ok {
			t.Errorf("Probe has no %s property", name)
		}
	}
	for _, name := range []string{"probeId", "displayName", "attemptCount", "createdAt"} {
		if _, ok := probe.Properties[name]; ok {
			t.Errorf("Probe carries camelCase %s", name)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestEveryRefResolves: a document with a dangling $ref generates a client
// that does not compile, and the emitter is the only thing standing between a
// partner and that.
func TestEveryRefResolves(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	raw := emit(t)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch t2 := v.(type) {
		case map[string]any:
			for k, child := range t2 {
				if k == "$ref" {
					name := strings.TrimPrefix(child.(string), "#/components/schemas/")
					if _, ok := doc.Components.Schemas[name]; !ok {
						t.Errorf("dangling $ref to %s", name)
					}
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range t2 {
				walk(child)
			}
		}
	}
	var any1 any
	if err := json.Unmarshal(raw, &any1); err != nil {
		t.Fatal(err)
	}
	walk(any1)
}
