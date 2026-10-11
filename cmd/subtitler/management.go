package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"

	"github.com/Pedro-Revez-Silva/subtitler/internal/config"
	sublang "github.com/Pedro-Revez-Silva/subtitler/internal/language"
	"github.com/Pedro-Revez-Silva/subtitler/internal/ownership"
)

func runManagement(ctx context.Context, action string, args []string) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	language := fs.String("lang", "", "configured subtitle language (required when multiple languages are managed)")
	reason := fs.String("reason", "out_of_sync", "out_of_sync, wrong_text, wrong_language, or missing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if action == "report" && fs.NArg() != 1 {
		return fmt.Errorf("usage: subtitler report -lang <configured-language> -reason out_of_sync /path/to/video.mkv")
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
	if action == "report" {
		if *language == "" {
			if len(cfg.Subtitles.RequiredLanguages) != 1 {
				return fmt.Errorf("report requires -lang when multiple subtitle languages are configured")
			}
			*language = cfg.Subtitles.RequiredLanguages[0]
		}
		code, err := sublang.Normalize(*language)
		if err != nil || !slices.Contains(cfg.Subtitles.RequiredLanguages, code) {
			return fmt.Errorf("report language must be one of subtitles.required_languages")
		}
		*language = code
	}
	feedback := map[string]string{"language": *language, "reason": *reason}
	if fs.NArg() == 1 {
		feedback["path"] = fs.Arg(0)
	}
	return ownership.Run(ctx, cfg, action, nil, feedback, os.Stdout)
}
