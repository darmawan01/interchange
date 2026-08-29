package interchange_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/darmawan01/interchange"
)

func recorder(name string, log *[]string) interchange.Stage {
	return interchange.Named(name, func(next interchange.UnaryFunc) interchange.UnaryFunc {
		return func(ctx context.Context, req *interchange.Envelope) (*interchange.Envelope, error) {
			*log = append(*log, name)
			return next(ctx, req)
		}
	})
}

func TestChainOrderIsOutermostFirst(t *testing.T) {
	var log []string
	c := interchange.Chain(recorder("a", &log), recorder("b", &log), recorder("c", &log))
	f, err := c.Wrap(func(context.Context, *interchange.Envelope) (*interchange.Envelope, error) {
		log = append(log, "handler")
		return &interchange.Envelope{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f(context.Background(), interchange.NewEnvelope("/p/M")); err != nil {
		t.Fatal(err)
	}
	if got, want := log, []string{"a", "b", "c", "handler"}; !slices.Equal(got, want) {
		t.Fatalf("chain ran %v, want %v", got, want)
	}
}

func TestChainNamedAnchors(t *testing.T) {
	var log []string
	c := interchange.Chain(recorder("telemetry", &log), recorder("recover", &log), recorder("deadline", &log)).
		After("deadline", recorder("tenant", &log)).
		Before("recover", recorder("idempotency", &log)).
		Replace("telemetry", recorder("otel", &log))
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"otel", "idempotency", "recover", "deadline", "tenant"}
	if got := c.Names(); !slices.Equal(got, want) {
		t.Fatalf("chain is %v, want %v", got, want)
	}
}

func TestChainMissingAnchorIsLoud(t *testing.T) {
	var log []string
	c := interchange.Chain(recorder("a", &log)).After("nope", recorder("b", &log))
	if c.Err() == nil {
		t.Fatal("inserting after a missing anchor must fail loudly, not silently append")
	}
	if _, err := c.Wrap(nil); err == nil {
		t.Fatal("Wrap must refuse a chain that failed to build")
	}
}

func TestChainRejectsDuplicateAndNilStages(t *testing.T) {
	var log []string
	if err := interchange.Chain(recorder("a", &log), recorder("a", &log)).Err(); err == nil {
		t.Fatal("a duplicate stage name must be an error: the anchors stop meaning anything")
	}
	if err := interchange.Chain(interchange.Named("a", nil)).Err(); err == nil {
		t.Fatal("a nil interceptor must be an error")
	}
	if err := interchange.Chain(interchange.Named("", func(n interchange.UnaryFunc) interchange.UnaryFunc { return n })).Err(); err == nil {
		t.Fatal("an unnamed stage must be an error")
	}
}

func TestEmptyChainIsValid(t *testing.T) {
	c := interchange.Chain()
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	f, err := c.Wrap(func(context.Context, *interchange.Envelope) (*interchange.Envelope, error) {
		return &interchange.Envelope{Message: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f(context.Background(), interchange.NewEnvelope("/p/M"))
	if err != nil || resp.Message != "ok" {
		t.Fatalf("empty chain must be transparent: %v %v", resp, err)
	}
}

func TestChainIsImmutable(t *testing.T) {
	var log []string
	base := interchange.Chain(recorder("a", &log), recorder("b", &log))
	derived := base.After("a", recorder("x", &log))
	if strings.Join(base.Names(), ",") != "a,b" {
		t.Fatalf("deriving mutated the base chain: %v", base.Names())
	}
	if strings.Join(derived.Names(), ",") != "a,x,b" {
		t.Fatalf("derived chain is %v", derived.Names())
	}
}
