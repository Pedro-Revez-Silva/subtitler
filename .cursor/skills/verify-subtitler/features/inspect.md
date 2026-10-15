# Inspect a file

Inspect prints the audio streams in one video and any sidecar subtitle files beside it.

## Sub-features

- `inspect-audio` prints each audio stream.
- `inspect-sidecar` prints each sidecar subtitle beside the video.
- `inspect-missing-config` fails when the config path does not exist.

## How to get to it (user POV)

- Run `subtitler inspect -config config.yaml /path/to/video.mkv`.

## Driving it with control-cli

Preconditions:

- Doctor has exited 0 and stdout contains `media tools available`.
- `$WORK/tone.mkv` exists.
- `$WORK/config.yaml` exists, with `dry_run: true` and `telemetry.enabled: false`.
- The current directory is `$WORK`.

- **Inspect the tone file.** Deliver `"$WORK/subtitler" inspect -config "$WORK/config.yaml" "$WORK/tone.mkv"` through `control-cli`. The exit code is 0. Stdout contains `audio stream` and `codec=aac`.
- **Missing config.** Deliver `"$WORK/subtitler" inspect -config "$WORK/missing-config.yaml" "$WORK/tone.mkv"` through `control-cli`. The exit code is 1. Stdout contains `no such file or directory`.
- **Proof.** Save the command, the exit code, stdout, and stderr under `/tmp/verify-subtitler-evidence`. Run `find "$WORK" -print`. The tone file is still present, and no `.srt` appears.

## Gotchas

- Inspect does not fall back when `-config` names a missing file. Generate falls back only when the default `config.yaml` is missing and `-config` was not passed.
- A failure is a slog line on stdout. The line contains `level=ERROR`.
- The tone file has no sidecar. An empty sidecar list is the end state for that file.
