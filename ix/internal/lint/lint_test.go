package lint_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/darmawan01/interchange/ix/internal/band"
	"github.com/darmawan01/interchange/ix/internal/bufx"
	"github.com/darmawan01/interchange/ix/internal/image"
	"github.com/darmawan01/interchange/ix/internal/lint"
)

func build(t *testing.T, dir string) *image.Image {
	t.Helper()
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf is not on PATH; ix shells out to it for every descriptor")
	}
	im, err := image.Build(&bufx.Runner{Dir: dir}, "api")
	if err != nil {
		t.Fatalf("building %s: %v", dir, err)
	}
	return im
}

func notWellKnown(path string) bool { return !strings.HasPrefix(path, "google/") }

// An extension number inside the reserved band with no row in the table is an
// error, not a warning. Two annotations at one number is a collision the
// descriptor parses happily, with one of the two options simply gone -- so
// the register has to be enforced before the second annotation exists.
func TestUnregisteredBandNumberIsAnError(t *testing.T) {
	im := build(t, "../../testdata/badband")
	fs := lint.Run(im, lint.Options{Band: band.Builtin(), Local: notWellKnown})

	var hit *lint.Finding
	for i := range fs {
		if fs[i].Rule == "BAND_UNREGISTERED" {
			hit = &fs[i]
		}
	}
	if hit == nil {
		t.Fatalf("no BAND_UNREGISTERED finding; got %v", fs)
	}
	if hit.Severity != lint.Error {
		t.Errorf("severity = %v, want error", hit.Severity)
	}
	if !strings.Contains(hit.Message, "50005") {
		t.Errorf("the finding does not name the number: %s", hit.Message)
	}
	if !strings.Contains(hit.Pos, "rogue.proto:") {
		t.Errorf("the finding has no source location: %s", hit.Pos)
	}
	if lint.Errors(fs) == 0 {
		t.Error("the run reported no errors")
	}
}

// Every annotation the project actually ships has a row, so the fixture that
// uses all of them must lint clean on the band rules.
func TestRegisteredAnnotationsPass(t *testing.T) {
	im := build(t, "../../testdata/fixture")
	fs := lint.Run(im, lint.Options{Band: band.Builtin(), Local: notWellKnown})
	for _, f := range fs {
		if strings.HasPrefix(f.Rule, "BAND_") {
			t.Errorf("unexpected band finding: %s", f)
		}
	}
}

// Authorization is an optional module, so ix has no opinion until the
// project's config expresses one.
func TestMissingAuthIsSilentUntilConfigured(t *testing.T) {
	im := build(t, "../../testdata/fixture")

	quiet := lint.Run(im, lint.Options{Band: band.Builtin(), Local: notWellKnown})
	for _, f := range quiet {
		if f.Rule == "AUTH_MISSING" {
			t.Fatalf("ix took a position on authorization with no auth block: %s", f)
		}
	}

	loud := lint.Run(im, lint.Options{Band: band.Builtin(), Local: notWellKnown, OnMissingAuth: "error"})
	n := 0
	for _, f := range loud {
		if f.Rule == "AUTH_MISSING" {
			n++
		}
	}
	if n == 0 {
		t.Error("on_missing_annotation: error produced no findings")
	}
}

func TestBandTableParses(t *testing.T) {
	tbl := band.Builtin()
	for _, tc := range []struct {
		extendee string
		number   int32
		name     string
	}{
		{"google.protobuf.MethodOptions", 50001, "interchange.auth.v1.auth"},
		{"google.protobuf.MethodOptions", 50002, "interchange.transport.v1.transports"},
		{"google.protobuf.ServiceOptions", 50002, "interchange.transport.v1.service_transports"},
		{"google.protobuf.MethodOptions", 50003, "interchange.transport.v1.internal"},
		{"google.protobuf.MethodOptions", 50004, "interchange.cli.v1.command"},
		{"google.protobuf.FieldOptions", 50007, "interchange.auth.v1.tenant_id_field"},
	} {
		e, ok := tbl.Lookup(tc.extendee, tc.number)
		if !ok {
			t.Errorf("%d on %s has no row", tc.number, tc.extendee)
			continue
		}
		if e.Name != tc.name {
			t.Errorf("%d on %s = %s, want %s", tc.number, tc.extendee, e.Name, tc.name)
		}
	}
	// 50002 is transports on MethodOptions and service_transports on
	// ServiceOptions. The extendee is part of the identity, which is the rule
	// that lets them share a number.
	if _, ok := tbl.Lookup("google.protobuf.MethodOptions", 50005); ok {
		t.Error("50005 is listed as free but has a row")
	}
}
