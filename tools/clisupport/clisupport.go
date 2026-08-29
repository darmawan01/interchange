// Package clisupport is the runtime half of protoc-gen-cli: the pieces a
// generated command tree needs that are the same for every service.
//
// The generated tree calls through Invoker rather than a concrete client, so
// the same commands run over Connect, over a bus, or against a fake in a
// test. A CLI that could only speak one transport would be the one place in
// the project where a contract picks a road.
package clisupport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Invoker calls one procedure. Anything that can carry a request to a server
// satisfies it: rpc.Client wrapped with a MethodDesc, engine.Client.Invoke
// directly, or a stub.
type Invoker interface {
	Invoke(ctx context.Context, procedure string, in, out proto.Message) error
}

// InvokerFunc adapts a function to Invoker.
type InvokerFunc func(ctx context.Context, procedure string, in, out proto.Message) error

// Invoke implements Invoker.
func (f InvokerFunc) Invoke(ctx context.Context, procedure string, in, out proto.Message) error {
	return f(ctx, procedure, in, out)
}

// EnsurePath walks or creates the command path under root and returns the
// command the leaf should be attached to. Two services mounting under the
// same prefix share the parent rather than fighting over it.
func EnsurePath(root *cobra.Command, path ...string) *cobra.Command {
	cur := root
	for _, name := range path {
		next := child(cur, name)
		if next == nil {
			next = &cobra.Command{
				Use:   name,
				Short: fmt.Sprintf("%s commands", name),
			}
			cur.AddCommand(next)
		}
		cur = next
	}
	return cur
}

func child(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// SetField parses raw into the named field of msg. It is how a positional
// argument and a --request-json override reach a request without the
// generator emitting a parser per field.
func SetField(msg proto.Message, name string, raw string) error {
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return fmt.Errorf("%s has no field %q", m.Descriptor().FullName(), name)
	}
	if fd.IsList() || fd.IsMap() {
		return fmt.Errorf("%s.%s is repeated: set it with --request-json", m.Descriptor().FullName(), name)
	}
	v, err := parse(fd, raw)
	if err != nil {
		return fmt.Errorf("field %s: %w", name, err)
	}
	m.Set(fd, v)
	return nil
}

func parse(fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	var zero protoreflect.Value
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte(raw)), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return zero, fmt.Errorf("%q is not a boolean", raw)
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return zero, fmt.Errorf("%q is not a 32-bit integer", raw)
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return zero, fmt.Errorf("%q is not a 64-bit integer", raw)
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return zero, fmt.Errorf("%q is not a 32-bit unsigned integer", raw)
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return zero, fmt.Errorf("%q is not a 64-bit unsigned integer", raw)
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return zero, fmt.Errorf("%q is not a number", raw)
		}
		return protoreflect.ValueOfFloat32(float32(f)), nil
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return zero, fmt.Errorf("%q is not a number", raw)
		}
		return protoreflect.ValueOfFloat64(f), nil
	case protoreflect.EnumKind:
		return parseEnum(fd, raw)
	default:
		return zero, fmt.Errorf("kind %s is not settable from a string: use --request-json", fd.Kind())
	}
}

func parseEnum(fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	values := fd.Enum().Values()
	if v := values.ByName(protoreflect.Name(raw)); v != nil {
		return protoreflect.ValueOfEnum(v.Number()), nil
	}
	if v := values.ByName(protoreflect.Name(strings.ToUpper(raw))); v != nil {
		return protoreflect.ValueOfEnum(v.Number()), nil
	}
	if n, err := strconv.ParseInt(raw, 10, 32); err == nil {
		return protoreflect.ValueOfEnum(protoreflect.EnumNumber(n)), nil
	}
	names := make([]string, 0, values.Len())
	for i := 0; i < values.Len(); i++ {
		names = append(names, string(values.Get(i).Name()))
	}
	return protoreflect.Value{}, fmt.Errorf("%q is not one of %s", raw, strings.Join(names, ", "))
}

// ApplyRequestJSON merges a JSON document into req. It is the escape hatch
// for fields no flag can carry -- repeated, nested, map -- so a generated
// command is never a strictly smaller surface than the RPC it fronts.
func ApplyRequestJSON(req proto.Message, doc string) error {
	if doc == "" {
		return nil
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal([]byte(doc), req); err != nil {
		return fmt.Errorf("--request-json: %w", err)
	}
	return nil
}

// PrintJSON writes a response as indented JSON. protojson deliberately
// varies its own whitespace, so the bytes go through encoding/json before
// they reach a terminal or a test.
func PrintJSON(w io.Writer, msg proto.Message) error {
	raw, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, buf.String())
	return err
}

// Coverage reports how much of a service the generated command tree actually
// covers. A CLI that silently fronts 80% of a service is worse than no CLI:
// the missing 20% is invisible until someone needs it.
type Coverage struct {
	// Service is the fully-qualified proto service name.
	Service string

	// Covered lists the procedures with a (command) annotation.
	Covered []string

	// Skipped lists the procedures annotated skip: true -- deliberate.
	Skipped []string

	// Missing lists the procedures with no annotation at all. Building with
	// require_annotation=true turns this list into a build failure.
	Missing []string
}

// Complete reports whether every RPC is either covered or deliberately
// skipped.
func (c Coverage) Complete() bool { return len(c.Missing) == 0 }

// String renders the report `ix` prints.
func (c Coverage) String() string {
	var b strings.Builder
	total := len(c.Covered) + len(c.Skipped) + len(c.Missing)
	fmt.Fprintf(&b, "%s: %d/%d covered, %d skipped, %d unannotated",
		c.Service, len(c.Covered), total, len(c.Skipped), len(c.Missing))
	for _, p := range c.Missing {
		fmt.Fprintf(&b, "\n  unannotated: %s", p)
	}
	return b.String()
}
