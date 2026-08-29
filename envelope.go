// Package interchange is the core of the Interchange framework: the runtime
// envelope, the interceptor chain, dispatch, and the five extension-point
// interfaces (Frontend, Driver, Codec, Interceptor, plus protoc plugins as
// generators).
//
// Core depends on interfaces. Adapters depend on core. Nothing depends on a
// concrete adapter. There is no broker client, no HTTP router, no policy
// engine and no auth module in this module's dependency graph -- a CI check
// asserts it (see hack/depcheck.sh).
package interchange

import (
	"context"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
)

// Metadata is the transport-neutral metadata map: credentials, trace context,
// tenant hint, idempotency key. Keys are canonicalised to lower case so a
// header-based transport and an envelope-based one agree on lookups.
type Metadata map[string]string

// NewMetadata returns Metadata with every key canonicalised.
func NewMetadata(kv map[string]string) Metadata {
	md := make(Metadata, len(kv))
	for k, v := range kv {
		md.Set(k, v)
	}
	return md
}

// Get returns the value for key, case-insensitively.
func (m Metadata) Get(key string) string {
	if m == nil {
		return ""
	}
	return m[strings.ToLower(key)]
}

// Set stores a value under the canonical form of key.
func (m Metadata) Set(key, value string) { m[strings.ToLower(key)] = value }

// Del removes a key.
func (m Metadata) Del(key string) { delete(m, strings.ToLower(key)) }

// Clone returns a copy; the zero Metadata clones to an empty, usable map.
func (m Metadata) Clone() Metadata {
	out := make(Metadata, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// AsMap returns the underlying map for marshalling into the wire envelope.
func (m Metadata) AsMap() map[string]string { return map[string]string(m) }

// Reserved metadata keys. The engine uses these to carry, on transports that
// lack the native facility, what HTTP supplies for free.
const (
	// MetaReplyTo is where a response goes when Capabilities.NativeReply is
	// false. Set by the engine, never by a handler.
	MetaReplyTo = "ix-reply-to"

	// MetaCodec names the codec of a chunked payload's reassembled body.
	MetaCodec = "ix-codec"
)

// Envelope is what the interceptor chain sees. It is deliberately NOT the
// wire message interchange.transport.v1.Request: over HTTP the wire envelope
// is never serialised, and an interceptor needs the decoded message that the
// wire form only carries as bytes.
//
// Drivers and the message engine speak the wire proto. Dispatch converts
// between the two, and it is the only place that does.
type Envelope struct {
	// Procedure is "/pkg.Service/Method" -- identical to the Connect
	// procedure string, so authz checks, metric labels and span names are
	// the same string on every road.
	Procedure string

	// Metadata is the transport-neutral metadata map.
	Metadata Metadata

	// Payload is the encoded message. It may be nil when Msg is set: a
	// binding that already holds a decoded message need not encode it for
	// dispatch to decode it again.
	Payload []byte

	// Codec names the encoding of Payload ("proto", "json").
	Codec string

	// CorrelationID is free in HTTP and mandatory everywhere else.
	CorrelationID string

	// Deadline is the zero time when the caller set none.
	Deadline time.Time

	// Msg is the decoded message. Dispatch populates it from Payload before
	// the chain runs, which is what lets an interceptor make a
	// resource-aware decision (§06).
	Msg proto.Message

	// Code, Message and Reason are the response status. Core moves them; it
	// assigns them no meaning beyond Code's dispatch-level use.
	Code    Code
	Message string
	Reason  string
}

// NewEnvelope returns a request envelope with usable maps.
func NewEnvelope(procedure string) *Envelope {
	return &Envelope{Procedure: procedure, Metadata: Metadata{}}
}

// Header returns Metadata, allocating it if nil.
func (e *Envelope) Header() Metadata {
	if e.Metadata == nil {
		e.Metadata = Metadata{}
	}
	return e.Metadata
}

// Service returns the fully-qualified service name from Procedure.
func (e *Envelope) Service() string { return ServiceOf(e.Procedure) }

// Method returns the bare method name from Procedure.
func (e *Envelope) Method() string { return MethodOf(e.Procedure) }

// ServiceOf splits "/pkg.Service/Method" into "pkg.Service".
func ServiceOf(procedure string) string {
	p := strings.TrimPrefix(procedure, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return p
}

// MethodOf splits "/pkg.Service/Method" into "Method".
func MethodOf(procedure string) string {
	if i := strings.LastIndex(procedure, "/"); i >= 0 {
		return procedure[i+1:]
	}
	return procedure
}

// Procedure builds the canonical procedure string for a service and method.
func Procedure(service, method string) string { return "/" + service + "/" + method }

// UnaryFunc is one step of the chain: an envelope in, an envelope out.
type UnaryFunc func(ctx context.Context, req *Envelope) (*Envelope, error)

// Interceptor wraps a UnaryFunc. This signature is stable API and it is
// core's entire contribution to cross-cutting concerns.
type Interceptor func(next UnaryFunc) UnaryFunc
