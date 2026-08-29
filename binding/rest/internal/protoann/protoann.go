// Package protoann reads the annotations the REST surface is derived from,
// straight off a method descriptor.
//
// The binding reads Transports off interchange.MethodDesc, which generated
// code has already resolved. The OpenAPI emitter cannot: it runs over a
// FileDescriptorSet at build time, with no registry and no generated Go, so
// it has to resolve the annotation itself. This is that resolution, kept in
// one place so the emitted document and the mounted routes cannot disagree
// about which methods are on the road.
package protoann

import (
	"fmt"
	"slices"

	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// DefaultRoads is what an RPC with no annotation anywhere travels: the two
// synchronous roads. A method reaches a broker only because someone wrote it
// down.
var DefaultRoads = []transportv1.Transport{
	transportv1.Transport_TRANSPORT_RPC,
	transportv1.Transport_TRANSPORT_REST,
}

// Transports resolves the roads a method travels, applying the rule stated in
// transports.proto: a per-method (transports) annotation replaces the
// service-level default entirely rather than merging with it, so a reviewer
// reads exactly one annotation to know where an RPC goes.
//
// An annotation that is present but names no road is an error rather than a
// silent fall-through to the default: the two readings of `(transports) = {
// group: "x" }` are "only change the group" and "expose on nothing", and
// guessing between them is how an internal RPC ends up on a public road.
func Transports(md protoreflect.MethodDescriptor) ([]transportv1.Transport, error) {
	if o := methodOptions(md); o != nil {
		on, err := normalize(o.GetOn())
		if err != nil {
			return nil, fmt.Errorf("%s: (transports): %w", md.FullName(), err)
		}
		return on, nil
	}
	if o := serviceOptions(md); o != nil {
		on, err := normalize(o.GetOn())
		if err != nil {
			return nil, fmt.Errorf("%s: (service_transports): %w", md.Parent().FullName(), err)
		}
		return on, nil
	}
	return slices.Clone(DefaultRoads), nil
}

// ExposedOn reports whether a method travels a road.
func ExposedOn(md protoreflect.MethodDescriptor, t transportv1.Transport) (bool, error) {
	on, err := Transports(md)
	if err != nil {
		return false, err
	}
	return slices.Contains(on, t), nil
}

// Internal reports the (internal) annotation: skipped by every public binding
// and absent from every emitted document.
func Internal(md protoreflect.MethodDescriptor) bool {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	if opts == nil || !proto.HasExtension(opts, transportv1.E_Internal) {
		return false
	}
	b, _ := proto.GetExtension(opts, transportv1.E_Internal).(bool)
	return b
}

// HTTPRule returns the google.api.http rule a REST route is derived from.
func HTTPRule(md protoreflect.MethodDescriptor) (*annotations.HttpRule, bool) {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	if opts == nil || !proto.HasExtension(opts, annotations.E_Http) {
		return nil, false
	}
	rule, ok := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	return rule, ok && rule != nil
}

// Idempotent reports idempotency_level = NO_SIDE_EFFECTS.
func Idempotent(md protoreflect.MethodDescriptor) bool {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	return opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS
}

func methodOptions(md protoreflect.MethodDescriptor) *transportv1.TransportOptions {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	if opts == nil || !proto.HasExtension(opts, transportv1.E_Transports) {
		return nil
	}
	o, _ := proto.GetExtension(opts, transportv1.E_Transports).(*transportv1.TransportOptions)
	return o
}

func serviceOptions(md protoreflect.MethodDescriptor) *transportv1.ServiceTransportOptions {
	sd, ok := md.Parent().(protoreflect.ServiceDescriptor)
	if !ok {
		return nil
	}
	opts, _ := sd.Options().(*descriptorpb.ServiceOptions)
	if opts == nil || !proto.HasExtension(opts, transportv1.E_ServiceTransports) {
		return nil
	}
	o, _ := proto.GetExtension(opts, transportv1.E_ServiceTransports).(*transportv1.ServiceTransportOptions)
	return o
}

// normalize sorts and dedupes the road list. It is a set: keeping annotation
// order would make an emitted artifact depend on the order someone happened
// to type, and the drift gate would flap on a cosmetic edit.
func normalize(on []transportv1.Transport) ([]transportv1.Transport, error) {
	if len(on) == 0 {
		return nil, fmt.Errorf("annotation names no transport; remove it to accept the default, or list the roads explicitly")
	}
	out := slices.Clone(on)
	slices.Sort(out)
	out = slices.Compact(out)
	for _, t := range out {
		if t == transportv1.Transport_TRANSPORT_UNSPECIFIED {
			return nil, fmt.Errorf("TRANSPORT_UNSPECIFIED is not a road")
		}
	}
	return out, nil
}
