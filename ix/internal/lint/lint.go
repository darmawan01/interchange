// Package lint checks what buf cannot: the naming rules the derivation
// depends on, and the annotation band.
//
// The naming rules are not style. The URL, the NATS subject, the CLI command
// and the SDK method are all *derived* from the RPC and message names
// (docs/02), so a service that does not end in `Service` or a field that is
// not snake_case produces a derived artifact that is wrong rather than ugly.
//
// The band check is the one that prevents a silent failure: two annotations
// at the same extension number is a collision the descriptor happily parses,
// with one of the two options simply gone. docs/annotation-band.md is the
// register, and this is what makes it load-bearing.
package lint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/annot"
	"github.com/darmawan01/interchange/ix/internal/band"
	"github.com/darmawan01/interchange/ix/internal/image"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Severity is how loudly a finding is reported.
type Severity int

const (
	Warn Severity = iota
	Error
)

func (s Severity) String() string {
	if s == Error {
		return "error"
	}
	return "warning"
}

// Finding is one lint result, in the compiler form every editor and CI
// annotator already parses.
type Finding struct {
	Pos      string
	Rule     string
	Severity Severity
	Message  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s: %s (%s)", f.Pos, f.Severity, f.Message, f.Rule)
}

// Options configures a run.
type Options struct {
	// Band is the annotation band table. Required.
	Band *band.Table

	// Local decides which descriptor files belong to this project.
	Local func(string) bool

	// ConfigDefault is transports.default from interchange.yaml.
	ConfigDefault []string

	// OnMissingAuth is the /auth module's policy: "error", "warn" or
	// "ignore" (the default). Core takes no position on authorization, so ix
	// only has an opinion when the project's config expresses one.
	OnMissingAuth string
}

// Run applies every ix rule to an image.
func Run(im *image.Image, o Options) []Finding {
	var fs []Finding
	fs = append(fs, checkBand(im, o)...)

	local := o.Local
	if local == nil {
		local = func(string) bool { return true }
	}
	im.Files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if !local(fd.Path()) {
			return true
		}
		fs = append(fs, checkFile(fd, o)...)
		return true
	})

	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Pos != fs[j].Pos {
			return fs[i].Pos < fs[j].Pos
		}
		return fs[i].Rule < fs[j].Rule
	})
	return fs
}

// Errors reports whether any finding is fatal.
func Errors(fs []Finding) int {
	n := 0
	for _, f := range fs {
		if f.Severity == Error {
			n++
		}
	}
	return n
}

// --- the band ------------------------------------------------------------

func checkBand(im *image.Image, o Options) []Finding {
	var fs []Finding
	seen := map[string]image.Extension{}
	for _, x := range im.Extensions {
		if !band.InBand(x.Number) {
			continue
		}
		pos := x.File
		if x.Line > 0 {
			pos = fmt.Sprintf("%s:%d", x.File, x.Line)
		}
		k := fmt.Sprintf("%s#%d", x.Extendee, x.Number)
		if prev, dup := seen[k]; dup && prev.FullName != x.FullName {
			fs = append(fs, Finding{
				Pos: pos, Rule: "BAND_COLLISION", Severity: Error,
				Message: fmt.Sprintf("%s and %s both extend %s at %d -- one of the two annotations is silently dropped on every descriptor",
					prev.FullName, x.FullName, x.Extendee, x.Number),
			})
			continue
		}
		seen[k] = x

		row, ok := o.Band.Lookup(string(x.Extendee), x.Number)
		if !ok {
			fs = append(fs, Finding{
				Pos: pos, Rule: "BAND_UNREGISTERED", Severity: Error,
				Message: fmt.Sprintf("extension %s uses %d on %s, which has no row in %s -- claim the number in the table first",
					x.FullName, x.Number, short(string(x.Extendee)), o.Band.Source),
			})
			continue
		}
		if row.Name != string(x.FullName) {
			fs = append(fs, Finding{
				Pos: pos, Rule: "BAND_MISMATCH", Severity: Error,
				Message: fmt.Sprintf("%s on %s is registered to %s in %s, not to %s",
					fmt.Sprint(x.Number), short(string(x.Extendee)), row.Name, o.Band.Source, x.FullName),
			})
		}
	}
	return fs
}

func short(full string) string {
	if i := strings.LastIndex(full, "."); i >= 0 {
		return full[i+1:]
	}
	return full
}

// --- naming --------------------------------------------------------------

func checkFile(fd protoreflect.FileDescriptor, o Options) []Finding {
	var fs []Finding

	ss := fd.Services()
	for i := 0; i < ss.Len(); i++ {
		sd := ss.Get(i)
		if !strings.HasSuffix(string(sd.Name()), "Service") {
			fs = append(fs, Finding{
				Pos: image.Pos(sd), Rule: "SERVICE_SUFFIX", Severity: Error,
				Message: fmt.Sprintf("service %s does not end in \"Service\" -- the CLI command and the bus subject are derived from this name", sd.Name()),
			})
		}
		ms := sd.Methods()
		for j := 0; j < ms.Len(); j++ {
			fs = append(fs, checkMethod(ms.Get(j), o)...)
		}
	}

	var walkMsgs func(protoreflect.MessageDescriptors)
	walkMsgs = func(md protoreflect.MessageDescriptors) {
		for i := 0; i < md.Len(); i++ {
			m := md.Get(i)
			if m.IsMapEntry() {
				continue
			}
			fs = append(fs, checkMessage(m)...)
			walkMsgs(m.Messages())
			fs = append(fs, checkEnums(m.Enums())...)
		}
	}
	walkMsgs(fd.Messages())
	fs = append(fs, checkEnums(fd.Enums())...)
	return fs
}

func checkMethod(md protoreflect.MethodDescriptor, o Options) []Finding {
	var fs []Finding
	m := annot.ForMethod(md, o.ConfigDefault)

	if m.Internal && (m.ExposedOn("rest") || m.ExposedOn("bus") || m.ExposedOn("mqtt") || m.ExposedOn("ws")) {
		fs = append(fs, Finding{
			Pos: image.Pos(md), Rule: "INTERNAL_EXPOSED", Severity: Error,
			Message: fmt.Sprintf("%s sets (internal) but declares %s -- (internal) means every public binding skips it, so the two annotations contradict each other",
				md.Name(), strings.Join(m.Transports, ", ")),
		})
	}
	if m.ExposedOn("rest") && m.HTTPPath == "" {
		fs = append(fs, Finding{
			Pos: image.Pos(md), Rule: "REST_NO_HTTP_RULE", Severity: Warn,
			Message: fmt.Sprintf("%s travels rest but declares no (google.api.http) rule -- the REST address cannot be derived", md.Name()),
		})
	}
	if m.Group != "" && !m.ExposedOn("bus") && !m.ExposedOn("mqtt") {
		fs = append(fs, Finding{
			Pos: image.Pos(md), Rule: "GROUP_WITHOUT_BUS", Severity: Warn,
			Message: fmt.Sprintf("%s sets a competing-consumer group but travels no message transport", md.Name()),
		})
	}

	switch o.OnMissingAuth {
	case "error", "warn":
		if m.Auth == nil {
			sev := Error
			if o.OnMissingAuth == "warn" {
				sev = Warn
			}
			fs = append(fs, Finding{
				Pos: image.Pos(md), Rule: "AUTH_MISSING", Severity: sev,
				Message: fmt.Sprintf("%s has no (auth) annotation, and auth.on_missing_annotation is %q", md.Name(), o.OnMissingAuth),
			})
		}
	}
	return fs
}

func checkMessage(md protoreflect.MessageDescriptor) []Finding {
	var fs []Finding
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		name := string(f.Name())

		if !isSnakeCase(name) {
			fs = append(fs, Finding{
				Pos: image.Pos(f), Rule: "FIELD_SNAKE_CASE", Severity: Error,
				Message: fmt.Sprintf("field %s.%s is not lower_snake_case -- the JSON name, the URL template and the CLI flag are all derived from it", md.Name(), name),
			})
		}
		if isIDField(name) && f.Kind() != protoreflect.StringKind {
			fs = append(fs, Finding{
				Pos: image.Pos(f), Rule: "ID_IS_STRING", Severity: Error,
				Message: fmt.Sprintf("field %s.%s is an id but is %s -- ids are strings (ULID or KSUID), never integers", md.Name(), name, f.Kind()),
			})
		}
		if isTimestamp(f) && !strings.HasSuffix(name, "_at") {
			fs = append(fs, Finding{
				Pos: image.Pos(f), Rule: "TIMESTAMP_SUFFIX", Severity: Warn,
				Message: fmt.Sprintf("field %s.%s is a Timestamp and should end in _at", md.Name(), name),
			})
		}
		if needsUnit(f, name) {
			fs = append(fs, Finding{
				Pos: image.Pos(f), Rule: "DURATION_UNIT", Severity: Warn,
				Message: fmt.Sprintf("field %s.%s is a scalar duration and carries no unit -- name it _ms, _seconds or _minutes", md.Name(), name),
			})
		}
	}
	return fs
}

func checkEnums(eds protoreflect.EnumDescriptors) []Finding {
	var fs []Finding
	for i := 0; i < eds.Len(); i++ {
		ed := eds.Get(i)
		want := strings.ToUpper(camelToSnake(string(ed.Name()))) + "_UNSPECIFIED"
		zero := ed.Values().ByNumber(0)
		switch {
		case zero == nil:
			fs = append(fs, Finding{
				Pos: image.Pos(ed), Rule: "ENUM_ZERO_UNSPECIFIED", Severity: Error,
				Message: fmt.Sprintf("enum %s has no zero value -- proto3 gives an unset field the zero value, so %s = 0 is what distinguishes \"unset\" from a real choice", ed.Name(), want),
			})
		case string(zero.Name()) != want:
			fs = append(fs, Finding{
				Pos: image.Pos(zero), Rule: "ENUM_ZERO_UNSPECIFIED", Severity: Error,
				Message: fmt.Sprintf("enum %s zero value is %s, want %s", ed.Name(), zero.Name(), want),
			})
		}
		if opts, _ := ed.Options().(*descriptorpb.EnumOptions); opts.GetAllowAlias() {
			fs = append(fs, Finding{
				Pos: image.Pos(ed), Rule: "ENUM_APPEND_ONLY", Severity: Error,
				Message: fmt.Sprintf("enum %s sets allow_alias -- an aliased value cannot be appended to safely, because a reader cannot tell which name a number meant", ed.Name()),
			})
		}
		if gap := firstGap(ed); gap > 0 {
			fs = append(fs, Finding{
				Pos: image.Pos(ed), Rule: "ENUM_APPEND_ONLY", Severity: Warn,
				Message: fmt.Sprintf("enum %s skips %d without reserving it -- a removed value that is not reserved will be silently reused by the next append", ed.Name(), gap),
			})
		}
	}
	return fs
}

// firstGap finds the first missing, unreserved number in an enum. A gap that
// is not reserved is a number the next append will reuse, which turns an
// append-only enum into a renumbering.
func firstGap(ed protoreflect.EnumDescriptor) int32 {
	vs := ed.Values()
	present := map[int32]bool{}
	var maxN int32
	for i := 0; i < vs.Len(); i++ {
		n := int32(vs.Get(i).Number())
		present[n] = true
		if n > maxN {
			maxN = n
		}
	}
	reserved := func(n int32) bool {
		rs := ed.ReservedRanges()
		for i := 0; i < rs.Len(); i++ {
			r := rs.Get(i)
			if protoreflect.EnumNumber(n) >= r[0] && protoreflect.EnumNumber(n) < r[1] {
				return true
			}
		}
		return false
	}
	for n := int32(0); n < maxN; n++ {
		if !present[n] && !reserved(n) {
			return n
		}
	}
	return -1
}

func isSnakeCase(s string) bool {
	if s == "" {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	prevUnderscore := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			prevUnderscore = false
		case c == '_':
			if prevUnderscore || i == len(s)-1 {
				return false
			}
			prevUnderscore = true
		default:
			return false
		}
	}
	return true
}

func isIDField(name string) bool {
	return name == "id" || strings.HasSuffix(name, "_id") || strings.HasSuffix(name, "_ids")
}

func isTimestamp(f protoreflect.FieldDescriptor) bool {
	return f.Kind() == protoreflect.MessageKind && f.Message().FullName() == "google.protobuf.Timestamp"
}

var unitSuffixes = []string{"_ms", "_us", "_ns", "_seconds", "_secs", "_minutes", "_hours", "_days", "_millis"}

// needsUnit flags a numeric field that reads as a duration but does not say
// which one. A message field is exempt: google.protobuf.Duration carries its
// own unit.
func needsUnit(f protoreflect.FieldDescriptor, name string) bool {
	switch f.Kind() {
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind,
		protoreflect.Uint64Kind, protoreflect.DoubleKind, protoreflect.FloatKind:
	default:
		return false
	}
	looksDuration := false
	for _, s := range []string{"_duration", "_timeout", "_interval", "_ttl", "_delay", "_age", "_elapsed"} {
		if strings.HasSuffix(name, s) {
			looksDuration = true
		}
	}
	if !looksDuration {
		return false
	}
	for _, s := range unitSuffixes {
		if strings.HasSuffix(name, s) {
			return false
		}
	}
	return true
}

func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
