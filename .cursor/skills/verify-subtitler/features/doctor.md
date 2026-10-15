# Run doctor

Doctor reports whether ffmpeg and ffprobe are available, and whether an API key and an ARR URL are set.

## Sub-features

- `doctor-tools` reports ffmpeg and ffprobe.
- `doctor-empty-key` warns when the API key is empty.
- `doctor-no-arr` warns when no Sonarr URL and no Radarr URL are set.

## How to get to it (user POV)

- Run `subtitler doctor -config config.yaml`.

## Driving it with control-cli

Preconditions:

- `$WORK/subtitler` exists and `version` has exited 0.
- `$WORK/config.yaml` exists, with `dry_run: true`, `telemetry.enabled: false`, and no Sonarr URL or Radarr URL.
- `OPENAI_API_KEY` is unset.
- The current directory is `$WORK`.

- **Run doctor.** Deliver `"$WORK/subtitler" doctor -config "$WORK/config.yaml"` through `control-cli`. The exit code is 0. Stdout contains `media tools available`.
- **Empty key.** Read that same stdout. It contains `openai.api_key is empty; dry runs can work, generation cannot`.
- **No ARR URL.** Read that same stdout. It contains `no Sonarr/Radarr URL configured; daemon scan will not have a media source`.
- **Proof.** Save the command, the exit code, stdout, and stderr under `/tmp/verify-subtitler-evidence`.

## Gotchas

- Doctor exits 1 when the config file is missing. Create `$WORK/config.yaml` in Launch before this command.
- The empty-key warning is expected. It does not make the instance unfit for inspect. It does mean generate cannot finish.
- `version` is a separate command. Ready still requires `"$WORK/subtitler" version` to print a version and exit 0.
