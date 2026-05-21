package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Martin-Hausleitner/beeper-matrix-proxy/beepersource"
)

func main() {
	dbPath := flag.String("db", "beeper-source.db", "SQLite WAL database path")
	once := flag.Bool("once", false, "run one reconcile pass and exit")
	interval := flag.Duration("interval", 30*time.Second, "reconcile interval")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := beepersource.DefaultConfig()
	beeperToken, err := cfg.BeeperToken()
	exitIfErr("load Beeper token", err)
	matrixToken, err := cfg.MatrixToken()
	exitIfErr("load Matrix token", err)

	store, err := beepersource.OpenStore(ctx, *dbPath)
	exitIfErr("open store", err)
	defer store.Close()

	api := beepersource.NewDesktopAPIAdapter(cfg, beeperToken)
	matrix, err := beepersource.NewMatrixClientSink(cfg, store, matrixToken)
	exitIfErr("create Matrix sink", err)
	svc := beepersource.NewService(cfg, store, api, matrix)
	matrixSource := beepersource.NewMatrixClientSource(cfg, store, matrixToken)

	run := func() {
		if err := svc.ReconcileOnce(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile failed: %v\n", err)
			return
		}
		if err := matrixSource.SyncOnce(ctx, svc); err != nil {
			fmt.Fprintf(os.Stderr, "matrix sync failed: %v\n", err)
			return
		}
		fmt.Fprintln(os.Stderr, "reconcile completed")
	}
	run()
	if *once {
		return
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func exitIfErr(label string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
		os.Exit(1)
	}
}
