package telemetry

import (
	"runtime"
	"strconv"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
	"github.com/getsentry/sentry-go"
)

type Client struct {
	enabled bool
}

type ScanSummary struct {
	Mode           string
	FoundItems     int
	ProcessedJobs  int
	MaxJobsPerScan int
	DryRun         bool
}

func Init(cfg config.TelemetryConfig, installationID, version string) (*Client, error) {
	if !cfg.Enabled {
		return &Client{}, nil
	}

	release := cfg.Release
	if release == "" {
		release = version
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:            cfg.SentryDSN,
		Release:        release,
		Environment:    cfg.Environment,
		ServerName:     "subtitler",
		SendDefaultPII: false,
	}); err != nil {
		return nil, err
	}

	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetUser(sentry.User{ID: installationID})
		scope.SetTag("app", "subtitler")
		scope.SetTag("version", release)
		scope.SetTag("os", runtime.GOOS)
		scope.SetTag("arch", runtime.GOARCH)
	})

	return &Client{enabled: true}, nil
}

func (c *Client) Started(mode string) {
	if !c.enabled {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("telemetry.event", "started")
		scope.SetTag("mode", mode)
		sentry.CaptureMessage("subtitler.started")
	})
}

func (c *Client) ScanFinished(summary ScanSummary) {
	if !c.enabled {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("telemetry.event", "scan_finished")
		scope.SetTag("mode", summary.Mode)
		scope.SetTag("dry_run", strconv.FormatBool(summary.DryRun))
		scope.SetContext("scan", map[string]any{
			"found_items":       summary.FoundItems,
			"processed_jobs":    summary.ProcessedJobs,
			"max_jobs_per_scan": summary.MaxJobsPerScan,
		})
		sentry.CaptureMessage("subtitler.scan_finished")
	})
}

func (c *Client) Flush(timeout time.Duration) {
	if c.enabled {
		sentry.Flush(timeout)
	}
}
