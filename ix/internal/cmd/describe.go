package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/darmawan01/interchange/ix/internal/address"
	"github.com/darmawan01/interchange/ix/internal/annot"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The proposal's claim is that the fan-out is reviewable. `ix describe` is
// what makes that concrete: one command answers "what does this method
// actually expose, and who can reach it?", so "should this be on the public
// bus?" stops being a question nobody thought to ask.
func newDescribe(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "describe <rpc>",
		Short: "Show what an RPC resolves to on every transport",
		Long: "Print the procedure, the request and response shape, the address on every\n" +
			"road, the authorization annotation if one is present, and the CLI mount.\n\n" +
			"The RPC may be named three ways:\n" +
			"  Service.Method            unambiguous suffix match\n" +
			"  pkg.Service.Method        fully qualified\n" +
			"  /pkg.Service/Method       the procedure string itself",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			md, err := p.FindMethod(args[0])
			if err != nil {
				return err
			}
			m := annot.ForMethod(md, p.Cfg.Transports.Default)
			writeDescribe(g.ui.Out, m)
			return nil
		},
	}
	return c
}

const (
	headWidth = 12 // "procedure   " and friends
	roadWidth = 10 // "rpc       "
	authWidth = 13 // "permission   "
)

func writeDescribe(w io.Writer, m *annot.Method) {
	md := m.Desc
	service := string(md.Parent().FullName())
	method := string(md.Name())

	// The request and response type names are aligned with each other so the
	// two field lists read as one column.
	tw := max(len(md.Input().Name()), len(md.Output().Name()))

	fmt.Fprintln(w)
	line(w, 2, headWidth, "procedure", address.Procedure(service, method))
	line(w, 2, headWidth, "request", typeLine(md.Input(), tw))
	line(w, 2, headWidth, "response", typeLine(md.Output(), tw))
	if md.IsStreamingClient() || md.IsStreamingServer() {
		line(w, 2, headWidth, "streaming", streamKind(md))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  TRANSPORTS")
	for _, road := range annot.Roads {
		if !m.ExposedOn(road) {
			line(w, 4, roadWidth, road, "not exposed")
			continue
		}
		line(w, 4, roadWidth, road, address.For(road, service, method, m.HTTPMethod, m.HTTPPath))
		if d := address.Detail(road, m.Group); d != "" {
			fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", 4+roadWidth+2), d)
		}
	}
	if m.Internal {
		fmt.Fprintf(w, "%s(internal) is set: every public binding skips this RPC; mTLS-only\n", strings.Repeat(" ", 4))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  AUTHORIZATION")
	if m.Auth == nil {
		// Core takes no position on authorization: the annotation lives in an
		// optional module, and its absence is a legitimate configuration
		// rather than a finding.
		line(w, 4, authWidth, "not declared", "(the (auth) annotation is an optional module)")
	} else {
		if m.Auth.Permission != "" {
			line(w, 4, authWidth, "permission", m.Auth.Permission)
		}
		if len(m.Auth.AuthTypes) > 0 {
			line(w, 4, authWidth, "accepts", strings.Join(m.Auth.AuthTypes, ", "))
		}
		line(w, 4, authWidth, "public", yesNo(m.Auth.Public))
		if m.Auth.Platform {
			line(w, 4, authWidth, "platform", "yes (cross-tenant: the request carries no tenant)")
		} else if f, declared := annot.TenantField(md); f != "" {
			suffix := "(convention)"
			if declared {
				suffix = "(declared)"
			}
			line(w, 4, authWidth, "tenant field", f+" "+suffix)
		}
	}

	fmt.Fprintln(w)
	if m.CLISkip {
		line(w, 2, authWidth, "CLI", "skipped")
	} else if len(m.CLI) > 0 {
		line(w, 2, authWidth, "CLI", strings.Join(m.CLI, " "))
	} else {
		line(w, 2, authWidth, "CLI", "not mounted")
	}
	if m.Idempotent {
		line(w, 2, authWidth, "idempotent", "yes (NO_SIDE_EFFECTS)")
	} else {
		line(w, 2, authWidth, "idempotent", "no")
	}
	if m.Internal {
		line(w, 2, authWidth, "internal", "yes")
	}
	fmt.Fprintln(w)
}

func line(w io.Writer, indent, width int, label, value string) {
	fmt.Fprintf(w, "%s%s%s\n", strings.Repeat(" ", indent), pad(label, width), value)
}

func typeLine(md protoreflect.MessageDescriptor, width int) string {
	name := string(md.Name()) + strings.Repeat(" ", width-len(md.Name())+1)
	fields := annot.FieldNames(md)
	if len(fields) == 0 {
		return name + "(no fields)"
	}
	return name + "(" + strings.Join(fields, ", ") + ")"
}

func streamKind(md protoreflect.MethodDescriptor) string {
	switch {
	case md.IsStreamingClient() && md.IsStreamingServer():
		return "bidirectional"
	case md.IsStreamingServer():
		return "server"
	default:
		return "client"
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
