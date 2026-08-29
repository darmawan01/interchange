package clisupport_test

import (
	"strings"
	"testing"

	"github.com/darmawan01/interchange/tools/clisupport"
	fixturev1 "github.com/darmawan01/interchange/tools/testdata/gen/interchange/fixture/v1"
	"github.com/spf13/cobra"
)

func TestSetField(t *testing.T) {
	for _, tc := range []struct {
		field   string
		raw     string
		check   func(*fixturev1.GetItemRequest) bool
		wantErr string
	}{
		{field: "id", raw: "abc", check: func(r *fixturev1.GetItemRequest) bool { return r.GetId() == "abc" }},
		{field: "limit", raw: "12", check: func(r *fixturev1.GetItemRequest) bool { return r.GetLimit() == 12 }},
		{field: "include_archived", raw: "true", check: func(r *fixturev1.GetItemRequest) bool { return r.GetIncludeArchived() }},
		{field: "kind", raw: "KIND_FILM", check: func(r *fixturev1.GetItemRequest) bool { return r.GetKind() == fixturev1.Kind_KIND_FILM }},
		{field: "kind", raw: "2", check: func(r *fixturev1.GetItemRequest) bool { return r.GetKind() == fixturev1.Kind_KIND_FILM }},
		{field: "cursor", raw: "c1", check: func(r *fixturev1.GetItemRequest) bool { return r.GetCursor() == "c1" }},
		{field: "limit", raw: "many", wantErr: "not a 32-bit integer"},
		{field: "kind", raw: "KIND_NOPE", wantErr: "KIND_BOOK"},
		{field: "tags", raw: "a", wantErr: "--request-json"},
		{field: "nope", raw: "a", wantErr: "no field"},
	} {
		t.Run(tc.field+"="+tc.raw, func(t *testing.T) {
			req := &fixturev1.GetItemRequest{}
			err := clisupport.SetField(req, tc.field, tc.raw)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want an error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(req) {
				t.Errorf("field not set: %v", req)
			}
		})
	}
}

func TestApplyRequestJSON(t *testing.T) {
	req := &fixturev1.GetItemRequest{}
	if err := clisupport.ApplyRequestJSON(req, `{"tags":["a"],"nested":{"note":"n"}}`); err != nil {
		t.Fatal(err)
	}
	if len(req.GetTags()) != 1 || req.GetNested().GetNote() != "n" {
		t.Errorf("json did not reach the request: %v", req)
	}
	if err := clisupport.ApplyRequestJSON(req, `{"bogus":1}`); err == nil {
		t.Error("an unknown field must fail rather than be dropped silently")
	}
}

// TestPrintJSONIsStable: protojson varies its own whitespace between calls,
// which would make every test that reads CLI output flake.
func TestPrintJSONIsStable(t *testing.T) {
	msg := &fixturev1.GetItemResponse{Id: "1", Kind: fixturev1.Kind_KIND_BOOK}
	var first strings.Builder
	if err := clisupport.PrintJSON(&first, msg); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		var next strings.Builder
		if err := clisupport.PrintJSON(&next, msg); err != nil {
			t.Fatal(err)
		}
		if next.String() != first.String() {
			t.Fatalf("output varies between calls:\n%q\n%q", first.String(), next.String())
		}
	}
	if !strings.Contains(first.String(), "\n  \"id\": \"1\"") {
		t.Errorf("want indented JSON, got %q", first.String())
	}
}

func TestEnsurePathReusesParents(t *testing.T) {
	root := &cobra.Command{Use: "ix"}
	a := clisupport.EnsurePath(root, "one", "two")
	b := clisupport.EnsurePath(root, "one", "two")
	if a != b {
		t.Fatal("EnsurePath built a second parent for the same path")
	}
	if n := len(root.Commands()); n != 1 {
		t.Errorf("root has %d children, want 1", n)
	}
}

func TestCoverageReport(t *testing.T) {
	c := clisupport.Coverage{
		Service: "a.v1.S",
		Covered: []string{"/a.v1.S/A"},
		Skipped: []string{"/a.v1.S/B"},
		Missing: []string{"/a.v1.S/C"},
	}
	if c.Complete() {
		t.Error("a service with an unannotated RPC is not complete")
	}
	got := c.String()
	if !strings.Contains(got, "1/3 covered") || !strings.Contains(got, "unannotated: /a.v1.S/C") {
		t.Errorf("report reads badly: %s", got)
	}
	if !(clisupport.Coverage{Service: "a.v1.S"}).Complete() {
		t.Error("a service with no holes is complete")
	}
}
