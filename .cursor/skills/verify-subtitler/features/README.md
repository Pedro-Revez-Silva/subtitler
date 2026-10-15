# Subtitler verification map

This directory is the maintained source for verifying the Subtitler CLI. Read this index, then follow the matching feature file.

## Baseline preconditions

- `WORK` is an empty directory outside the repo root, and the current directory for each command is `$WORK`.
- Launch has built `$WORK/subtitler`, written `$WORK/tone.mkv`, and written `$WORK/config.yaml`.
- `GOTOOLCHAIN` is `go1.26.9`. `SUBTITLER_TELEMETRY` is `off`. `OPENAI_API_KEY` is unset.
- `ffmpeg` and `ffprobe` resolve on `PATH`.
- Doctor has exited 0, and stdout contains `media tools available`.
- Deliver commands through `control-cli`. Do not drive a work directory this run did not create.

## Driving conventions

- Start every recipe from the baseline state unless its preconditions say otherwise.
- Treat every command as literal. Keep the paths and flags unchanged.
- Errors from the CLI are slog lines on stdout.
- Read-only diagnostics are the exit code, stdout, stderr, and `find` of `$WORK`.
- Cleanup may delete the tone file, the config, the binary, and the state file. It must leave `/tmp/verify-subtitler-evidence` in place.

## Proof and failure reporting

- Capture the command and the files it changed, not only the exit code.
- CLI proof includes the command, stdout, stderr, and the exit code.
- Record the feature file with every artifact.
- Report an unreachable command with the command you ran and the unmet precondition.
- A missing precondition is a failed journey. Do not report that journey as skipped, and do not report it as passed through a different command.

## Feature entry contract

Each feature file starts with an H1 and one paragraph. It then uses these four H2 headings, in order.

1. `Sub-features`
2. `How to get to it (user POV)`
3. `Driving it with control-cli`
4. `Gotchas`

## Features

- [Inspect a file](./inspect.md) prints audio streams and sidecar subtitles for one video.
- [Run doctor](./doctor.md) checks ffmpeg, ffprobe, the API key, and ARR URLs.
- [Generate a subtitle](./generate.md) records the no-key failure and the state file it writes.
- [Scan the library](./scan.md) is the dry-run scan. The boot config cannot meet its precondition.
- [Read status](./status.md) is the owner status command. The boot config cannot meet its precondition.
