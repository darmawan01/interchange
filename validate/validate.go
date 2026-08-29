// Package validate is the optional field-validation module.
//
// Declarative rules on the message, enforced by one interceptor: written once
// in the contract, applied on every transport (§06). The alternative is the
// same rule implemented three times in three languages with three different
// error messages, and the one place it is missing is the road nobody tested.
//
// Core guarantees the half that cannot be got any other way: chain symmetry
// means that if this stage is installed, it cannot run on HTTP and skip the
// bus. This module supplies the other half -- the rules themselves, and their
// projection onto interchange.Error.
package validate

import (
	"context"
	"strconv"
	"strings"

	"buf.build/go/protovalidate"
	"github.com/darmawan01/interchange"
)

// StageName is the anchor other stages are inserted around, the way core's
// StageTelemetry is. Chains extend by name, not by position.
const StageName = "validate"

// DefaultReason is the reason attached to a rule violation. It is the stock
// /errors taxonomy's INVALID_ARGUMENT, written as a string rather than
// imported: this module does not depend on that one, and core takes no
// position on either (§06). If you run a different taxonomy, pass WithReason.
const DefaultReason = "INVALID_ARGUMENT"

// Metadata keys carrying the field-level detail. They are ordinary metadata,
// so they reach a caller the same way on every road: as HTTP headers on the
// Connect binding, in Response.metadata on a bus.
const (
	// MetaCount holds the number of violations detected, which may be larger
	// than the number reported -- see WithMaxDetails.
	MetaCount = "ix-violations"

	metaPrefix = "ix-violation-"
)

type config struct {
	reason     string
	maxDetails int
}

// Option configures the interceptor.
type Option func(*config)

// WithReason overrides the reason on a validation failure.
func WithReason(reason string) Option { return func(c *config) { c.reason = reason } }

// WithMaxDetails caps how many violations travel in the metadata. A message
// with a hundred bad fields would otherwise produce a header block a proxy
// rejects; the count in MetaCount stays exact.
func WithMaxDetails(n int) Option { return func(c *config) { c.maxDetails = n } }

// DefaultMaxDetails is the cap when none is set.
const DefaultMaxDetails = 8

// Stage returns the interceptor as a named chain stage:
//
//	interchange.DefaultChain(cfg).Append(validate.Stage(v))
//
// Where it sits matters only relative to stages that read the message: put
// authorization after it if the decision reads fields, so a permission check
// never runs against a message that was never checked.
func Stage(v protovalidate.Validator, opts ...Option) interchange.Stage {
	return interchange.Named(StageName, Interceptor(v, opts...))
}

// Interceptor is Stage without the name.
//
// A nil validator means protovalidate.GlobalValidator, which is the shared
// instance with the shared rule cache. Pass your own -- built with
// protovalidate.New and whatever options you need -- when you want fail-fast,
// custom CEL functions, or a private extension resolver.
func Interceptor(v protovalidate.Validator, opts ...Option) interchange.Interceptor {
	cfg := config{reason: DefaultReason, maxDetails: DefaultMaxDetails}
	for _, o := range opts {
		o(&cfg)
	}
	if v == nil {
		v = protovalidate.GlobalValidator
	}

	return func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			// Dispatch decodes into Msg before the chain runs, which is what
			// makes one validator work for every transport: there is no
			// second, transport-shaped copy of the request to check.
			if req.Msg == nil {
				return next(ctx, req)
			}
			if err := v.Validate(req.Msg); err != nil {
				return nil, cfg.toError(req.Procedure, err)
			}
			return next(ctx, req)
		}
	}
}

func (c config) toError(procedure string, err error) error {
	var ve *protovalidate.ValidationError
	if !asValidationError(err, &ve) {
		// A rule that will not compile, or a CEL expression that blew up, is
		// a defect in the contract rather than in the request. Reporting it
		// as invalid_argument would send a caller chasing their own payload.
		return interchange.WrapError(interchange.CodeInternal, err).WithReason("INTERNAL")
	}

	violations := Violations(ve)
	md := interchange.Metadata{}
	md.Set(MetaCount, strconv.Itoa(len(violations)))

	reported := violations
	if c.maxDetails > 0 && len(reported) > c.maxDetails {
		reported = reported[:c.maxDetails]
	}
	var summary strings.Builder
	for i, v := range reported {
		n := strconv.Itoa(i + 1)
		md.Set(metaPrefix+n+"-field", v.Field)
		md.Set(metaPrefix+n+"-rule", v.Rule)
		md.Set(metaPrefix+n+"-message", v.Message)
		if i > 0 {
			summary.WriteString("; ")
		}
		summary.WriteString(v.String())
	}
	if len(violations) > len(reported) {
		summary.WriteString("; and ")
		summary.WriteString(strconv.Itoa(len(violations) - len(reported)))
		summary.WriteString(" more")
	}

	return interchange.Errorf(interchange.CodeInvalidArgument, "%s", summary.String()).
		WithReason(c.reason).
		WithMeta(md)
}

// Violation is one unmet rule. It is a plain struct rather than the
// protovalidate type because it has to survive a transport: the same three
// strings are what travel in the metadata and what ViolationsFrom reads back.
type Violation struct {
	// Field is the path to the offending field, "provider.contacts[0].email".
	Field string

	// Rule identifies the rule that was not met, "string.min_len". A client
	// branches on this; Message is prose.
	Rule string

	// Message is the human-readable explanation.
	Message string
}

// String renders "field: message", the form used in the error's summary.
func (v Violation) String() string {
	if v.Field == "" {
		return v.Message
	}
	return v.Field + ": " + v.Message
}

// Violations converts a protovalidate error into the transportable form.
func Violations(ve *protovalidate.ValidationError) []Violation {
	if ve == nil {
		return nil
	}
	out := make([]Violation, 0, len(ve.Violations))
	for _, v := range ve.Violations {
		if v == nil || v.Proto == nil {
			continue
		}
		out = append(out, Violation{
			Field:   sanitize(protovalidate.FieldPathString(v.Proto.GetField())),
			Rule:    sanitize(v.Proto.GetRuleId()),
			Message: sanitize(v.Proto.GetMessage()),
		})
	}
	return out
}

// ViolationsOf reads the field-level detail back off an error, whichever road
// it arrived on. This is the client-side half of the "written once, applied
// everywhere" claim: the same call works on the error a Connect client
// reconstructs from headers and on the one a bus client builds from
// Response.metadata.
func ViolationsOf(err error) []Violation { return ViolationsFrom(interchange.MetaOf(err)) }

// ViolationsFrom reads the detail out of raw metadata, for a caller holding
// headers rather than an error.
func ViolationsFrom(md interchange.Metadata) []Violation {
	var out []Violation
	for i := 1; ; i++ {
		n := strconv.Itoa(i)
		field := md.Get(metaPrefix + n + "-field")
		rule := md.Get(metaPrefix + n + "-rule")
		message := md.Get(metaPrefix + n + "-message")
		if field == "" && rule == "" && message == "" {
			return out
		}
		out = append(out, Violation{Field: field, Rule: rule, Message: message})
	}
}

// Count reports how many violations the message had, which can exceed
// len(ViolationsOf(err)) when WithMaxDetails truncated the list.
func Count(err error) int {
	n, cerr := strconv.Atoi(interchange.MetaOf(err).Get(MetaCount))
	if cerr != nil {
		return 0
	}
	return n
}

// sanitize keeps a value legal as an HTTP header: a rule message is
// author-supplied CEL output, and a newline in it would be rejected at
// header-write time on one road while travelling fine on another.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	const max = 256
	if len(s) > max {
		return s[:max]
	}
	return s
}

// asValidationError is errors.As specialised, kept local so this file does
// not shadow the stdlib name in a package a reader expects to be about rules.
func asValidationError(err error, target **protovalidate.ValidationError) bool {
	for err != nil {
		if ve, ok := err.(*protovalidate.ValidationError); ok {
			*target = ve
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
