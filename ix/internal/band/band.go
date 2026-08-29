// Package band reads the annotation band table.
//
// Two annotations at the same number is a silent, undebuggable collision --
// the descriptor still parses and one of the two options is simply gone. The
// table in docs/annotation-band.md is the register that prevents it, and
// `ix lint` is what makes the register load-bearing rather than a document
// people forget to update.
package band

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Low and High bound the reserved private range.
const (
	Low  = 50000
	High = 59999
)

// builtin mirrors docs/annotation-band.md so ix can lint a project that has
// no copy of the doc. A project's own file wins when it has one.
//
//go:embed annotation-band.md
var builtin string

// Entry is one row.
type Entry struct {
	Number   int32
	Extendee string // "MethodOptions"
	Name     string // "interchange.transport.v1.transports"
	Module   string
}

// Table is a parsed band table, indexed by (extendee, number) -- the pair is
// the identity, which is why 50002 can be `transports` on MethodOptions and
// `service_transports` on ServiceOptions without colliding.
type Table struct {
	// Source is where the table came from, for diagnostics.
	Source string

	entries []Entry
	byKey   map[string]Entry
}

var rowRE = regexp.MustCompile(`^\|\s*(\d{4,5})\s*\|\s*` + "`" + `?([A-Za-z]+)` + "`" + `?\s*\|\s*` + "`" + `?([A-Za-z0-9_.]+)` + "`" + `?\s*\|\s*([^|]*)\|`)

// Parse reads a markdown band table.
func Parse(src, name string) (*Table, error) {
	t := &Table{Source: name, byKey: map[string]Entry{}}
	for _, line := range strings.Split(src, "\n") {
		m := rowRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		e := Entry{
			Number:   int32(n),
			Extendee: m[2],
			Name:     m[3],
			Module:   strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(m[4], "`", ""), "*", "")),
		}
		t.entries = append(t.entries, e)
		t.byKey[key(e.Extendee, e.Number)] = e
	}
	if len(t.entries) == 0 {
		return nil, fmt.Errorf("%s: no annotation-band rows found -- the table must have rows of the form | 50001 | MethodOptions | pkg.name | module | consumer |", name)
	}
	sort.Slice(t.entries, func(i, j int) bool {
		if t.entries[i].Number != t.entries[j].Number {
			return t.entries[i].Number < t.entries[j].Number
		}
		return t.entries[i].Extendee < t.entries[j].Extendee
	})
	return t, nil
}

// Builtin is the table ix ships with.
func Builtin() *Table {
	t, err := Parse(builtin, "ix builtin annotation band")
	if err != nil {
		panic("ix: embedded annotation-band.md is unparseable: " + err.Error())
	}
	return t
}

// Load finds the band table for a project: docs/annotation-band.md under the
// root if it exists, otherwise the builtin copy.
func Load(root string) *Table {
	for _, rel := range []string{"docs/annotation-band.md", "annotation-band.md"} {
		p := filepath.Join(root, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if t, err := Parse(string(b), p); err == nil {
			return t
		}
	}
	return Builtin()
}

// LoadFile reads a specific table, for tests and for --band.
func LoadFile(path string) (*Table, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(string(b), path)
}

// Lookup finds the row claiming a number on an extendee.
func (t *Table) Lookup(extendee string, number int32) (Entry, bool) {
	e, ok := t.byKey[key(shortName(extendee), number)]
	return e, ok
}

// Entries lists every row, sorted.
func (t *Table) Entries() []Entry { return t.entries }

// InBand reports whether a number falls inside the reserved range.
func InBand(n int32) bool { return n >= Low && n <= High }

func key(extendee string, n int32) string { return extendee + "#" + strconv.Itoa(int(n)) }

// shortName turns "google.protobuf.MethodOptions" into "MethodOptions",
// which is how the table writes it.
func shortName(full string) string {
	if i := strings.LastIndex(full, "."); i >= 0 {
		return full[i+1:]
	}
	return full
}
