package interchange

import (
	"slices"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Codec encodes and decodes messages. The envelope carries the codec name per
// message, so a browser-facing binding can stay human-readable while
// service-to-service traffic stays binary -- on the same service, at the same
// time.
type Codec interface {
	// Name is the value carried in the envelope's `codec` field.
	Name() string
	Marshal(proto.Message) ([]byte, error)
	Unmarshal([]byte, proto.Message) error
}

// Codec names that ship in the box.
const (
	CodecProto = "proto"
	CodecJSON  = "json"
)

type protoCodec struct{}

func (protoCodec) Name() string { return CodecProto }

func (protoCodec) Marshal(m proto.Message) ([]byte, error) {
	// Deterministic: two encodings of the same message must be byte-identical,
	// or an idempotency key computed from the payload stops matching itself.
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}

func (protoCodec) Unmarshal(b []byte, m proto.Message) error { return proto.Unmarshal(b, m) }

type jsonCodec struct{}

func (jsonCodec) Name() string { return CodecJSON }

func (jsonCodec) Marshal(m proto.Message) ([]byte, error) {
	// camelCase on the RPC surface; the REST binding overrides this with its
	// own documented casing (§08).
	return protojson.MarshalOptions{}.Marshal(m)
}

func (jsonCodec) Unmarshal(b []byte, m proto.Message) error {
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, m)
}

// ProtoCodec is the binary codec.
func ProtoCodec() Codec { return protoCodec{} }

// JSONCodec is the human-readable codec.
func JSONCodec() Codec { return jsonCodec{} }

var codecs = struct {
	sync.RWMutex
	m map[string]Codec
}{m: map[string]Codec{
	CodecProto: protoCodec{},
	CodecJSON:  jsonCodec{},
}}

// RegisterCodec makes a codec available to every binding by name. Registering
// a second codec under an existing name replaces it, which is how you swap
// the JSON codec for one with different casing.
func RegisterCodec(c Codec) {
	codecs.Lock()
	defer codecs.Unlock()
	codecs.m[c.Name()] = c
}

// CodecFor resolves a codec by name. An empty name means the default,
// "proto".
func CodecFor(name string) (Codec, error) {
	if name == "" {
		name = CodecProto
	}
	codecs.RLock()
	defer codecs.RUnlock()
	c, ok := codecs.m[name]
	if !ok {
		return nil, Errorf(CodeInvalidArgument, "unknown codec %q", name)
	}
	return c, nil
}

// Codecs lists the registered codec names.
func Codecs() []string {
	codecs.RLock()
	defer codecs.RUnlock()
	out := make([]string, 0, len(codecs.m))
	for n := range codecs.m {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
