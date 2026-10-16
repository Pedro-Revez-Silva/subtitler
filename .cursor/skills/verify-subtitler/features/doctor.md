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

- `$WORK/subtitler` exists and `$EVIDENCE/launch/version.rc` is 0.
- `$WORK/config.yaml` exists, with `dry_run: true`, `telemetry.enabled: false`, and no Sonarr URL or Radarr URL.
- `OPENAI_API_KEY` is unset.

- **Run doctor.** Deliver `"$CAPTURE" doctor doctor-tools "$WORK/subtitler" doctor -config "$WORK/config.yaml"`. The exit code is 0. Stdout contains `media tools available`.
- **Empty key.** Read `$EVIDENCE/doctor/doctor-tools.stdout`. It contains `openai.api_key is empty; dry runs can work, generation cannot`.
- **No ARR URL.** Read that same file. It contains `no Sonarr/Radarr URL configured; daemon scan will not have a media source`.
- **Proof.** `capture.sh` saved the command, the exit code, stdout, stderr, and the `find` listing as `$EVIDENCE/doctor/doctor-tools.*`.

## Gotchas

- Doctor exits 1 when the config file is missing. Create `$WORK/config.yaml` in Launch before this command.
- The empty-key warning is expected. It does not make the instance unfit for inspect. It does mean generate cannot finish a subtitle.
- `version` is a separate command. Ready still requires the Launch `version` capture to print a version and exit 0.
- A second doctor run in the same `EVIDENCE` needs a new sub-feature name, such as `doctor-tools-rerun`. `capture.sh` will not overwrite the first.
