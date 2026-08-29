// Command catalogd serves the catalog on every road it was declared on.
//
// The whole of the wiring is catalog.Chain and catalog.Wire; everything below
// is flags and signal handling. Adding NATS is one more line, and it is a
// driver -- not a second handler, not a second chain, not a second definition
// of any method.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/darmawan01/interchange"
	"github.com/darmawan01/interchange/driver/memory"
	natsdriver "github.com/darmawan01/interchange/driver/nats"
	"github.com/darmawan01/interchange/examples/catalog"
	"github.com/nats-io/nats.go"
)

func main() {
	addr := flag.String("http", ":8080", "address for the Connect binding")
	natsURL := flag.String("nats", "", "NATS URL; empty runs an in-process bus instead")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*addr, *natsURL, log); err != nil {
		log.Error("catalogd", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run(addr, natsURL string, log *slog.Logger) error {
	chain, err := catalog.Chain(interchange.Config{
		Observer: interchange.SlogObserver(log),
		Logger:   log,
		// A bus call carries no deadline unless someone sets one, and a
		// handler nobody is waiting for is work the process should not do.
		DefaultTimeout: 30 * time.Second,
	})
	if err != nil {
		return err
	}

	impl := catalog.NewServer()
	impl.Seed("acme", "stripe", "adyen")

	svc, err := catalog.Wire(impl, chain)
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	drv, err := busDriver(natsURL)
	if err != nil {
		return err
	}
	if _, err := svc.ServeBus(ctx, drv); err != nil {
		return err
	}

	srv := &http.Server{Addr: addr, Handler: svc.RPC.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("serving",
		slog.String("http", addr),
		slog.String("bus", drv.Caps().Name),
		slog.Any("procedures", svc.Registry.Procedures()))

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// busDriver is the only place in this binary that names a broker. Nothing
// above it knows which one it got, which is the seam the engine is built on.
func busDriver(url string) (interchange.Driver, error) {
	if url == "" {
		return memory.New().Driver("catalogd"), nil
	}
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return natsdriver.New(conn)
}
