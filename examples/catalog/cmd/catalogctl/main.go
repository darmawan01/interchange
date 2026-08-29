// Command catalogctl is the generated CLI, mounted.
//
// Every command under it comes from the (cli.command) annotation in
// catalog.proto -- nothing here names a subcommand, a flag or an argument.
// The tree calls through a clisupport.Invoker, so the same commands run over
// Connect or over a bus and --nats is the only difference: a CLI is a caller,
// and a caller does not pick the road.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/binding/rpc"
	natsdriver "github.com/darmawan01/interchange/driver/nats"
	"github.com/darmawan01/interchange/engine"
	catalogv1cli "github.com/darmawan01/interchange/examples/catalog/gen/go/catalog/v1/catalogv1cli"
	"github.com/darmawan01/interchange/tools/clisupport"
	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

func main() {
	var (
		addr    string
		natsURL string
		token   string
		apiKey  string
	)

	root := &cobra.Command{
		Use:           "catalogctl",
		Short:         "Drive catalog.v1.CatalogService from a terminal",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&addr, "addr", "http://localhost:8080", "Connect endpoint")
	root.PersistentFlags().StringVar(&natsURL, "nats", "", "call over NATS instead of HTTP")
	root.PersistentFlags().StringVar(&token, "token", "", "bearer token")
	root.PersistentFlags().StringVar(&apiKey, "api-key", "", "workload API key")

	root.AddCommand(&cobra.Command{
		Use:   "coverage",
		Short: "Report which RPCs this command tree fronts",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), catalogv1cli.CatalogServiceCoverage())
		},
	})

	// The client is built on the first call rather than here, because the
	// flags it reads are not parsed until cobra runs the leaf command.
	catalogv1cli.RegisterCatalogServiceCommands(root, lazyInvoker(func() (clisupport.Invoker, error) {
		md := interchange.Metadata{}
		if token != "" {
			md.Set("authorization", "Bearer "+token)
		}
		if apiKey != "" {
			md.Set("x-api-key", apiKey)
		}
		if natsURL == "" {
			return rpc.NewClient(http.DefaultClient, addr, rpc.WithStaticMetadata(md)), nil
		}
		conn, err := nats.Connect(natsURL)
		if err != nil {
			return nil, err
		}
		drv, err := natsdriver.New(conn)
		if err != nil {
			return nil, err
		}
		return engine.NewClient(context.Background(), drv,
			engine.WithStaticMetadata(md), engine.WithTimeout(30*time.Second))
	}))

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "catalogctl:", err)
		os.Exit(1)
	}
}

// lazyInvoker defers building the client until a command actually calls one.
// A CLI process makes one call, so there is nothing to reuse and nothing to
// pool -- both *rpc.Client and *engine.Client satisfy Invoker as they are.
type lazyInvoker func() (clisupport.Invoker, error)

func (f lazyInvoker) Invoke(ctx context.Context, procedure string, in, out proto.Message) error {
	client, err := f()
	if err != nil {
		return err
	}
	return client.Invoke(ctx, procedure, in, out)
}
