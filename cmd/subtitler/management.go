package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
	"github.com/Pedro-Revez-Silva/subtitler/internal/ownership"
)

func runManagement(ctx context.Context, action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	language := fs.String("lang", "pt-PT", "subtitle language")
	reason := fs.String("reason", "out_of_sync", "out_of_sync, wrong_text, wrong_language, or missing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if action == "report" && fs.NArg() != 1 {
		return fmt.Errorf("usage: subtitler report -lang pt-PT -reason out_of_sync /path/to/video.mkv")
	}
	if action == "status" && fs.NArg() > 0 {
		return fmt.Errorf("status takes no file arguments")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !cfg.Management.Enabled {
		return fmt.Errorf("management is not enabled")
	}
	feedback := map[string]string{"language": *language, "reason": *reason}
	if fs.NArg() == 1 {
		feedback["path"] = fs.Arg(0)
	}
	return ownership.Run(ctx, cfg, action, nil, feedback, os.Stdout)
}
