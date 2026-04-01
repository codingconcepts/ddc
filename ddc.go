package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/codingconcepts/ddc/pkg/config"
	"github.com/codingconcepts/ddc/pkg/runner"
)

func main() {
	log.SetFlags(0)

	configPath := flag.String("config", "~/.ddc/config.yaml", "absolute or relative path to the dcc config file")
	quiet := flag.Bool("quiet", false, "execute deletions without prompting (default is preview mode)")
	debug := flag.Bool("debug", false, "enable verbose debug logging")
	interactive := flag.Bool("interactive", false, "prompt before each deletion")
	flag.Parse()

	if *configPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	if *debug {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	resolved, err := config.ExpandPath(*configPath)
	if err != nil {
		log.Fatalf("error resolving config path: %v", err)
	}
	*configPath = resolved

	if err := config.Ensure(*configPath); err != nil {
		log.Fatalf("error ensuring config: %v", err)
	}

	cfg, err := config.Parse(*configPath)
	if err != nil {
		log.Fatalf("error parsing config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *debug {
		cfg.Print()
	}

	if !*quiet {
		log.SetPrefix("[dry-run] ")
	}

	r := runner.New(cfg, *quiet, *debug, *interactive)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("received interrupt, stopping...")
		if err := r.Stop(); err != nil {
			log.Printf("error stopping runner: %v", err)
		}
		cancel()
	}()

	if err := r.Start(ctx); err != nil {
		log.Fatalf("error running: %v", err)
	}
}
