// Package engine is the message engine: the single machine that turns a
// channel/message transport into the same dispatch a request/response
// transport gets for free.
//
// Correlation, deadlines, metadata fallback, chunking, replay suppression and
// frame ordering live here, once, for every broker. A driver supplies six
// methods and a capability struct; everything else is inherited.
//
// The engine contains no switch on transport type. All variation comes from
// interchange.Capabilities.
package engine

import (
	"encoding/binary"
	"fmt"

	transportv1 "github.com/darmawan01/interchange/gen/go/interchange/transport/v1"
	"google.golang.org/protobuf/proto"
)

// The wire is a one-byte discriminator followed by a marshalled envelope
// message. The envelope shapes in §03 are fixed and carry no self-describing
// tag, and a receiver must be able to tell a whole Request from a chunk of one
// on a transport that offers no headers -- so the discriminator lives outside
// the proto rather than inside it.
const (
	kindRequest  byte = 1
	kindResponse byte = 2
	kindFrame    byte = 3
)

func frame(kind byte, m proto.Message) ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(b)+1)
	out = append(out, kind)
	return append(out, b...), nil
}

func unframe(b []byte) (byte, []byte, error) {
	if len(b) == 0 {
		return 0, nil, fmt.Errorf("engine: empty frame")
	}
	switch b[0] {
	case kindRequest, kindResponse, kindFrame:
		return b[0], b[1:], nil
	default:
		return 0, nil, fmt.Errorf("engine: unknown wire kind %d", b[0])
	}
}

// chunk splits an already-framed body into Frames small enough for a
// transport with a payload ceiling. The frames carry the framed body, so the
// receiver reassembles bytes it can unframe exactly as if they had arrived
// whole -- which is why chunking needs no special case anywhere else.
//
// A zero or negative max means no ceiling.
func chunk(correlationID string, framed []byte, max int) ([][]byte, error) {
	if max <= 0 || len(framed) <= max {
		return [][]byte{framed}, nil
	}
	// Budget for the Frame wrapper itself: worst-case field overhead plus the
	// discriminator byte. Measured, not guessed, so a driver reporting a hard
	// broker limit is not overshot by a byte.
	probe := &transportv1.Frame{
		CorrelationId: correlationID,
		Kind:          transportv1.Frame_KIND_MESSAGE,
		Sequence:      ^uint64(0),
	}
	head, err := proto.Marshal(probe)
	if err != nil {
		return nil, err
	}
	overhead := len(head) + 1 /* discriminator */ + 1 /* payload tag */ + binary.MaxVarintLen32
	size := max - overhead
	if size <= 0 {
		return nil, fmt.Errorf("engine: MaxPayload %d is too small to carry a frame header of %d bytes", max, overhead)
	}

	var out [][]byte
	var seq uint64
	for off := 0; off < len(framed); off += size {
		end := min(off+size, len(framed))
		b, err := frame(kindFrame, &transportv1.Frame{
			CorrelationId: correlationID,
			Kind:          transportv1.Frame_KIND_MESSAGE,
			Sequence:      seq,
			Payload:       framed[off:end],
		})
		if err != nil {
			return nil, err
		}
		out = append(out, b)
		seq++
	}
	end, err := frame(kindFrame, &transportv1.Frame{
		CorrelationId: correlationID,
		Kind:          transportv1.Frame_KIND_END,
		Sequence:      seq,
	})
	if err != nil {
		return nil, err
	}
	return append(out, end), nil
}
