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

- **Inspect the tone file.** Deliver `"$CAPTURE" inspect inspect-audio "$WORK/subtitler" inspect -config "$WORK/config.yaml" "$WORK/tone.mkv"`. The exit code is 0. Stdout contains `audio stream` and `codec=aac`.
- **No sidecar.** Read `$EVIDENCE/inspect/inspect-audio.stdout`. It contains no `sidecar subtitle` line.
- **Missing config.** Deliver `"$CAPTURE" inspect inspect-missing-config "$WORK/subtitler" inspect -config "$WORK/missing-config.yaml" "$WORK/tone.mkv"`. The exit code is 1. Stdout contains `level=ERROR msg="inspect failed"` and `no such file or directory`.
- **Proof.** `capture.sh` saved both commands as `$EVIDENCE/inspect/inspect-audio.*` and `$EVIDENCE/inspect/inspect-missing-config.*`. Each `.find` listing still has `$WORK/tone.mkv` and has no `.srt`.

## Gotchas

- Inspect does not fall back when `-config` names a missing file. Generate falls back only when the default `config.yaml` is missing and `-config` was not passed.
- A failure is a slog line on stdout. The line contains `level=ERROR`.
- The tone file has no sidecar. An empty sidecar list is the end state for that file.
