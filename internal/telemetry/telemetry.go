package telemetry

import (
	"runtime"
	"time"

	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
	"github.com/getsentry/sentry-go"
)

type Client struct {
	enabled bool
}

func Init(cfg config.TelemetryConfig, installationID, version string) (*Client, error) {
	if !cfg.EnabledValue() || cfg.SentryDSN == "" {
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

func (c *Client) Installed(mode string) {
	if !c.enabled {
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("telemetry.event", "installed")
		scope.SetTag("mode", mode)
		sentry.CaptureMessage("subtitler.installed")
	})
}

func (c *Client) Flush(timeout time.Duration) {
	if c.enabled {
		sentry.Flush(timeout)
	}
}
