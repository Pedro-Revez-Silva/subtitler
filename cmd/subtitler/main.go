package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/app"
	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
	"github.com/Pedro-Revez-Silva/subtitler/internal/state"
	"github.com/Pedro-Revez-Silva/subtitler/internal/telemetry"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "daemon":
		if err := runDaemon(ctx, logger, os.Args[2:]); err != nil {
			logger.Error("daemon failed", "error", err)
			os.Exit(1)
		}
	case "scan":
		if err := runScan(ctx, logger, os.Args[2:]); err != nil {
			logger.Error("scan failed", "error", err)
			os.Exit(1)
		}
	case "generate":
		if err := runGenerate(ctx, logger, os.Args[2:]); err != nil {
			logger.Error("generate failed", "error", err)
			os.Exit(1)
		}
	case "inspect":
		if err := runInspect(ctx, logger, os.Args[2:]); err != nil {
			logger.Error("inspect failed", "error", err)
			os.Exit(1)
		}
	case "doctor":
		if err := runDoctor(ctx, logger, os.Args[2:]); err != nil {
			logger.Error("doctor failed", "error", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func runDaemon(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	telemetryClient := initTelemetry(logger, cfg, "daemon")
	defer telemetryClient.Flush(2 * time.Second)

	service := app.New(cfg, logger, telemetryClient, "daemon")
	interval := cfg.Processing.ScanInterval
	if interval <= 0 {
		interval = 30 * time.Minute
	}

	logger.Info("subtitler daemon started", "scan_interval", interval, "dry_run", cfg.DryRun)
	for {
		if err := service.ScanAndProcess(ctx); err != nil {
			logger.Error("scan cycle failed", "error", err)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info("subtitler daemon stopped")
			return nil
		case <-timer.C:
		}
	}
}

func runScan(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	process := fs.Bool("process", false, "override config and process files")
	dryRun := fs.Bool("dry-run", false, "override config and only list planned work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *process && *dryRun {
		return fmt.Errorf("scan cannot use both -process and -dry-run")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *process {
		cfg.DryRun = false
	}
	if *dryRun {
		cfg.DryRun = true
	}
	telemetryClient := initTelemetry(logger, cfg, "scan")
	defer telemetryClient.Flush(2 * time.Second)
	return app.New(cfg, logger, telemetryClient, "scan").ScanAndProcess(ctx)
}

func runGenerate(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	langs := fs.String("langs", "", "comma-separated output subtitle languages; defaults to config")
	contextHint := fs.String("context", "", "extra transcription context")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: subtitler generate [flags] /path/to/video.mkv")
	}

	cfg, err := loadGenerateConfig(*configPath, flagWasPassed(fs, "config"))
	if err != nil {
		return err
	}
	if *langs != "" {
		cfg.Subtitles.RequiredLanguages = splitCSV(*langs)
	}
	if *contextHint != "" {
		cfg.OpenAI.Context = *contextHint
	}
	telemetryClient := initTelemetry(logger, cfg, "generate")
	defer telemetryClient.Flush(2 * time.Second)

	return app.New(cfg, logger, telemetryClient, "generate").GenerateOne(ctx, fs.Arg(0))
}

func loadGenerateConfig(path string, required bool) (config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if required || !os.IsNotExist(err) {
		return config.Config{}, err
	}
	return config.Default(), nil
}

func runInspect(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: subtitler inspect [flags] /path/to/video.mkv")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return app.New(cfg, logger, nil, "inspect").Inspect(ctx, fs.Arg(0))
}

func runDoctor(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return app.New(cfg, logger, nil, "doctor").Doctor(ctx)
}

func initTelemetry(logger *slog.Logger, cfg config.Config, mode string) *telemetry.Client {
	if !cfg.Telemetry.EnabledValue() {
		return &telemetry.Client{}
	}
	store, err := state.Open(cfg.StatePath)
	if err != nil {
		logger.Warn("telemetry disabled because state could not be opened", "error", err)
		return &telemetry.Client{}
	}
	if store.TelemetryInstalledSent() {
		return &telemetry.Client{}
	}
	installationID, err := store.InstallationID()
	if err != nil {
		logger.Warn("telemetry disabled because installation id could not be created", "error", err)
		return &telemetry.Client{}
	}
	client, err := telemetry.Init(cfg.Telemetry, installationID, version)
	if err != nil {
		logger.Warn("telemetry disabled because Sentry could not initialize", "error", err)
		return &telemetry.Client{}
	}
	store.MarkTelemetryInstalledSent()
	if err := store.Save(); err != nil {
		logger.Warn("telemetry disabled because install marker could not be saved", "error", err)
		return &telemetry.Client{}
	}
	client.Installed(mode)
	return client
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func flagWasPassed(fs *flag.FlagSet, name string) bool {
	passed := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			passed = true
		}
	})
	return passed
}

func usage() {
	fmt.Fprintln(os.Stderr, `subtitler

Usage:
  subtitler daemon   -config config.yaml
  subtitler scan     -config config.yaml [-process|-dry-run]
  subtitler generate [-config config.yaml] [-langs en,pt-PT] /path/to/video.mkv
  subtitler inspect  -config config.yaml /path/to/video.mkv
  subtitler doctor   -config config.yaml
  subtitler version`)
}
