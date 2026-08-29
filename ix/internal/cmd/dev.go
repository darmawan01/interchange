package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/darmawan01/interchange/ix/internal/devsrv"
	"github.com/spf13/cobra"
)

const devHelp = "ix dev exercises the CONTRACT, not your business logic.\n\n" +
	"There are no compiled handlers here, so every RPC is answered by a stub that\n" +
	"returns a default-valued response built from the descriptor by reflection.\n" +
	"What that proves is that the contract dispatches: the procedure resolves, the\n" +
	"request decodes against the declared shape, the envelope makes a real round\n" +
	"trip through the real engine and the real interceptor chain, and the response\n" +
	"is the shape the descriptor says it is. It proves nothing about your handler,\n" +
	"because there isn't one.\n\n" +
	"It runs over driver/memory, which is a real driver rather than a mock -- the\n" +
	"same six methods and the same Capabilities every broker driver declares. So\n" +
	"this is the production machinery with the broker removed, not a simulation\n" +
	"of it."

func newDev(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "dev",
		Short: "Local loopback -- exercise a contract with no infrastructure",
		Long: devHelp + "\n\n" +
			"Run `ix dev` to start it, then `ix dev call <rpc> '<json>'` from another\n" +
			"shell. `ix dev call` also works on its own: with no server listening it\n" +
			"starts a loopback of its own, makes the call and exits.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDev(g)
		},
	}
	c.AddCommand(newDevCall(g))
	return c
}

// devSocket keeps the socket beside the project so two projects can run `ix
// dev` at once and neither has to be told where the other put its socket.
//
// A unix socket path is capped by sun_path -- 104 bytes on darwin, 108 on
// linux -- and a deep checkout blows past it. The fallback hashes the project
// root into a short name under the temp directory, which keeps the
// one-socket-per-project property without the length.
func devSocket(p *Project) string {
	sock := filepath.Join(p.Cfg.Root, ".interchange", "dev.sock")
	if len(sock) <= maxSocketPath {
		return sock
	}
	sum := sha256.Sum256([]byte(p.Cfg.Root))
	return filepath.Join(os.TempDir(), fmt.Sprintf("ix-dev-%x.sock", sum[:8]))
}

// maxSocketPath is the smallest sun_path across the platforms this runs on,
// less room for the null terminator. Being conservative costs nothing: the
// fallback path is short either way.
const maxSocketPath = 100

func runDev(g *globals) error {
	p, err := openProject(g)
	if err != nil {
		return err
	}
	im, err := p.Image()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lb, err := devsrv.Start(ctx, im, p.Local())
	if err != nil {
		return err
	}
	defer lb.Close()

	sock := devSocket(p)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return err
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", p.Rel(sock), err)
	}
	defer func() {
		ln.Close()
		os.Remove(sock)
	}()

	fmt.Fprintln(g.ui.Out)
	g.ui.OK("dev", fmt.Sprintf("%d procedure(s) over driver/memory", len(lb.Procedures())))
	for _, s := range lb.Plan() {
		detail := s.Pattern
		if s.Group != "" {
			detail += "  (queue group: " + s.Group + ")"
		}
		fmt.Fprintf(g.ui.Out, "      subscribed  %s\n", detail)
	}
	fmt.Fprintln(g.ui.Out)
	for _, proc := range lb.Procedures() {
		fmt.Fprintf(g.ui.Out, "      %s\n", proc)
	}
	fmt.Fprintf(g.ui.Out, "\n  listening on %s\n", p.Rel(sock))
	fmt.Fprintf(g.ui.Out, "  %s\n\n", g.ui.Dim("stub responses only — this exercises the contract, not your handlers"))

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(g.ui.Out, "  stopped")
				return nil
			}
			return err
		}
		go serveDevConn(ctx, lb, conn)
	}
}

// devRequest and devReply are the loopback's wire format: one JSON object per
// line, in each direction. It is deliberately trivial -- this socket exists
// so `ix dev call` can reach a running `ix dev`, and nothing else.
type devRequest struct {
	Procedure string          `json:"procedure"`
	Body      json.RawMessage `json:"body,omitempty"`
}

type devReply struct {
	Body  json.RawMessage `json:"body,omitempty"`
	Error string          `json:"error,omitempty"`
}

func serveDevConn(ctx context.Context, lb *devsrv.Loopback, conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req devRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			enc.Encode(devReply{Error: err.Error()})
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, err := lb.Call(callCtx, req.Procedure, req.Body)
		cancel()
		if err != nil {
			enc.Encode(devReply{Error: err.Error()})
			continue
		}
		enc.Encode(devReply{Body: out})
	}
}

func newDevCall(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "call <rpc> [json]",
		Short: "Invoke an RPC on the loopback",
		Long: devHelp + "\n\n" +
			"With a running `ix dev` the call goes over its socket; without one, this\n" +
			"starts a loopback, makes the call and exits. Either way the response is a\n" +
			"default-valued message: the request is really decoded and really\n" +
			"dispatched, but nothing computes an answer.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject(g)
			if err != nil {
				return err
			}
			md, err := p.FindMethod(args[0])
			if err != nil {
				return err
			}
			procedure := "/" + string(md.Parent().FullName()) + "/" + string(md.Name())

			var body json.RawMessage
			if len(args) == 2 && args[1] != "" {
				if !json.Valid([]byte(args[1])) {
					return fmt.Errorf("the request argument is not valid JSON: %s", args[1])
				}
				body = json.RawMessage(args[1])
			}

			out, err := callDev(p, procedure, body)
			if err != nil {
				return err
			}
			fmt.Fprintln(g.ui.Out, string(out))
			return nil
		},
	}
}

func callDev(p *Project, procedure string, body json.RawMessage) ([]byte, error) {
	if conn, err := net.DialTimeout("unix", devSocket(p), 2*time.Second); err == nil {
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(devRequest{Procedure: procedure, Body: body}); err != nil {
			return nil, err
		}
		var reply devReply
		if err := json.NewDecoder(conn).Decode(&reply); err != nil {
			return nil, err
		}
		if reply.Error != "" {
			return nil, fmt.Errorf("%s", reply.Error)
		}
		return reply.Body, nil
	}

	im, err := p.Image()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lb, err := devsrv.Start(ctx, im, p.Local())
	if err != nil {
		return nil, err
	}
	defer lb.Close()
	return lb.Call(ctx, procedure, body)
}
