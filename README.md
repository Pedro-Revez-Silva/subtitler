<div align="center">

<img src="docs/favicon.png" alt="Subtitler" width="120" />

# Subtitler

**Automatic subtitles for your media library.**

Self-hosted and headless — it finds what your library is missing, generates clean
`.srt` subtitles, and integrates with Sonarr &amp; Radarr to know where your files live.

[![Status](https://img.shields.io/badge/status-experimental-f0852b)](#current-status)
[![License](https://img.shields.io/badge/license-GPL--3.0-1f3a6d)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![Docker](https://img.shields.io/badge/Docker-GHCR-2496ED?logo=docker&logoColor=white)](#-quick-start-docker)
[![Powered by OpenAI](https://img.shields.io/badge/transcription-OpenAI-10a37f)](#model-note)

[Quick start](#-quick-start-docker) · [How it works](#-how-it-works) · [Configuration](#-configuration) · [Standalone mode](#-standalone-file-mode) · [Telemetry](#-telemetry)

</div>

---

Subtitler periodically asks Sonarr and Radarr where your media lives, checks what subtitles
already exist, and generates **only what's missing**. It transcribes audio with OpenAI, can
translate while preserving timestamps, and writes standard `.srt` sidecars next to each file —
the kind Jellyfin, Plex, and friends read natively.

It is built to be **safe and economical**: dry-run by default, a per-scan job limit to control
cost on first runs, and it never reaches for the paid API when a cheaper option exists.

## ✨ Features

- 🎯 **Generates only what's missing** — transcribes with OpenAI and writes an `.srt` per language you require
- 🔌 **Sonarr &amp; Radarr discovery** — no separate media-folder config; it reads the paths the \*arr apps already manage
- 🎙️ **Audio-first timing** — ties subtitle timings to a preferred source audio track for accurate sync
- 🌍 **Optional translation** — translate generated subtitles to extra languages while preserving timestamps
- 🧹 **Subtitle cleanup policies** — `keep`, `quarantine`, or `delete` external subtitle clutter
- 🐢 **Controlled rollout** — dry-run mode, `missing_only` strategy, and a per-scan job cap keep first runs predictable
- 🗺️ **Path mapping** — translate ARR-reported paths to your container mounts when they differ
- 🐳 **Docker-native** — the image bundles `ffmpeg` and `ffprobe`; the host only needs Docker
- 🧍 **Standalone mode** — run it on a single file without Sonarr/Radarr or a config file

## 🔭 How it works

| | Step | What happens |
|---|---|---|
| 1 | **Locate media** | Asks the Sonarr and Radarr APIs for the canonical path of every file |
| 2 | **Inspect** | Checks existing sidecars and embedded streams against your required languages |
| 3 | **Generate** | Builds a timed transcript from a source audio track, then translates to the other languages |
| 4 | **Write** | Saves standard `.srt` sidecars next to the video and records state for retries |

## Current status

This is an early, working implementation — useful today, still evolving:

- Sonarr/Radarr API discovery and per-container path mapping
- external subtitle cleanup: `keep`, `quarantine`, or `delete`
- generation strategies: `missing_only`, `generated_only`, `force`
- MKV/MP4 audio stream inspection with `ffprobe`
- audio-first subtitle timing from a preferred source audio track
- optional embedded subtitle extraction when explicitly enabled
- audio extraction/chunking with `ffmpeg`, OpenAI transcription to SRT
- optional SRT translation while preserving timestamps
- JSON state file for generated outputs and failures
- periodic daemon scans that process only missing-subtitle jobs, with a per-scan job limit
- a one-time anonymous installation signal (see [Telemetry](#-telemetry))

Embedded subtitle *removal* is intentionally not implemented because it requires remuxing media
files. Embedded *extraction* is supported, but audio-first generation is the default because it
ties subtitle timings to the audio track being watched.

## 🚀 Quick start (Docker)

Docker is the recommended setup. The image includes `ffmpeg` and `ffprobe`, so the host only
needs Docker and access to the media folders.

### 1. Copy the example files

```bash
cp config.example.yaml config.yaml
cp .env.example .env
```

Edit `.env`:

```env
OPENAI_API_KEY=your-openai-api-key
SONARR_API_KEY=your-sonarr-api-key
RADARR_API_KEY=your-radarr-api-key
PUID=1000
PGID=1000
```

Set `PUID` and `PGID` to the user that owns your media files (`id -u` and `id -g`).

### 2. Configure Sonarr and Radarr

```yaml
sonarr:
  url: http://sonarr:8989
  api_key: ${SONARR_API_KEY}

radarr:
  url: http://radarr:7878
  api_key: ${RADARR_API_KEY}
```

Use URLs reachable from the subtitler container. Container names like `http://sonarr:8989` work
when everything shares a Docker network; otherwise use a reachable host/IP.

### 3. Mount the media paths

Subtitler does not need its own media-folder config — it asks Sonarr and Radarr for paths through
their APIs. The container must read and write those **exact** paths. If Radarr reports
`/mnt/media/movies/Movie Name/Movie.mkv`, mount the same path:

```yaml
volumes:
  - ./config.yaml:/etc/subtitler/config.yaml:ro
  - ./data:/data
  - /mnt/media:/mnt/media
```

If matching paths is impossible, translate them with `path_mappings` in `config.yaml`:

```yaml
subtitles:
  path_mappings:
    - from: /movies
      to: /media/movies
```

### 4. Keep first-run settings safe

The example config starts in dry-run mode and conservative behavior:

```yaml
dry_run: true
subtitles:
  source_audio_languages:
    - en
    - auto
  source_subtitle_language: en
  strategy: missing_only
  cleanup:
    external_subtitles: keep
processing:
  max_jobs_per_scan: 1
```

The first real run will only add missing subtitles, won't remove existing sidecars, and will
process at most one media item per scan.

### 5. Pull and validate

```bash
docker compose --env-file .env -f docker-compose.example.yaml pull
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler doctor -config /etc/subtitler/config.yaml
```

### 6. Run a dry scan

Keep `dry_run: true` (or add `-dry-run` to force a preview):

```bash
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler scan -config /etc/subtitler/config.yaml
```

The scan should find media through Sonarr/Radarr and report what it would generate.

### 7. Run one real job

Set `dry_run: false` and keep `max_jobs_per_scan: 1`, then:

```bash
docker compose --env-file .env -f docker-compose.example.yaml run --rm subtitler scan -config /etc/subtitler/config.yaml
```

Expected result: one missing item gets `.subtitler.en.srt` and/or `.subtitler.pt.srt` sidecars
next to the video.

### 8. Start the daemon

```bash
docker compose --env-file .env -f docker-compose.example.yaml up -d
docker compose --env-file .env -f docker-compose.example.yaml logs -f subtitler
```

The daemon repeats the library scan every `processing.scan_interval`.

## 🔧 Configuration

### Gradual library scanning

```yaml
dry_run: false
subtitles:
  strategy: missing_only
  cleanup:
    external_subtitles: keep
processing:
  scan_interval: 30m
  max_jobs_per_scan: 1
```

`max_jobs_per_scan` limits media items that actually need subtitle work. Files that already have
the required sidecars do not consume that budget. Set it to `0` only when you are comfortable
letting the scanner process every missing item it finds in one scan.

### Important choices

```yaml
subtitles:
  required_languages: [en, pt-PT]
  source_audio_languages: [en, auto]
  source_subtitle_language: en
  strategy: missing_only
  embedded:
    action: ignore
  cleanup:
    external_subtitles: keep
```

`source_audio_languages` controls which audio track is used to create the timed source transcript.
`source_subtitle_language` is the intermediate subtitle language written from that audio before
translating to the other required languages. With the example above, Subtitler prefers English
audio, writes English timed cues, then translates those cues to Portuguese.

`missing_only` writes required languages only when it cannot find matching sidecars. Embedded
subtitle extraction is available only when `embedded.action: extract` is enabled — keep it disabled
for audio-first timing.

To let generated subtitles replace clutter, use `generated_only` with `quarantine` first. Once you
trust the matching behavior:

```yaml
cleanup:
  external_subtitles: delete
```

### Model note

Timestamped subtitle generation currently uses OpenAI `whisper-1` with `response_format=srt`. Newer
transcription models can produce stronger transcripts, but the current implementation requires
native SRT timestamps for usable timing.

### Packaging note

The intended setup-and-forget deployment is Docker; the image installs `ffmpeg` and `ffprobe`. If
you run the Go binary directly on a host, those tools must be installed or configured:

```yaml
tools:
  ffmpeg: /path/to/ffmpeg
  ffprobe: /path/to/ffprobe
```

## 🧍 Standalone file mode

Subtitler can run as a plain executable without Sonarr/Radarr and without a config file — handy
when you just want subtitles for one file.

```bash
export OPENAI_API_KEY=...
go build -o subtitler ./cmd/subtitler
./subtitler generate -langs en,pt-PT "/path/to/movie.mkv"
```

If `config.yaml` is not present and `-config` is not passed, `generate` uses safe defaults:

- reads `OPENAI_API_KEY` from the environment
- writes `.subtitler.<language>.srt` next to the video
- uses `whisper-1` for timestamped SRT output
- uses `gpt-4o-mini` only when translation is needed
- keeps local state in `subtitler-state.json`

Passing `-config custom.yaml` makes the config explicit; if that file is missing, the command fails
instead of silently falling back.

## 📡 Telemetry

Telemetry is enabled by default and sends **one** anonymous `subtitler.installed` message to Sentry
the first time an installation starts. The marker is stored in the state file, so normal restarts
do not send another event. It exists to answer basic maintainer questions like installation count
and most-used version.

Disable it any time:

```yaml
telemetry:
  enabled: false
```

You can also set `SUBTITLER_TELEMETRY=off`. `SENTRY_DSN` (or `telemetry.sentry_dsn`) overrides the
built-in DSN for private builds.

It sends a locally generated random installation ID, plus app version, OS, architecture, and command
mode. It **never** sends media paths, titles, subtitle text, OpenAI prompts, API keys, ARR URLs, or
hostnames. Sentry error/panic capture is off by default because arbitrary errors can contain file paths.

## 🛠️ Local development

You need Go 1.25.11 or newer plus `ffmpeg`/`ffprobe` on the host for local non-Docker runs.

```bash
export OPENAI_API_KEY=...
export SONARR_API_KEY=...
export RADARR_API_KEY=...
go run ./cmd/subtitler doctor -config config.yaml
go run ./cmd/subtitler scan -config config.yaml -dry-run
```

Process one file manually, or run continuously:

```bash
go run ./cmd/subtitler generate -config config.yaml "/path/to/movie.mkv"
go run ./cmd/subtitler daemon -config config.yaml
```

### Quality gate

```bash
bin/quality fast          # fast local check
bin/quality commit        # format, tests, and 95% core coverage
bin/quality push          # commit gate, vet, Docker build, govulncheck
bin/quality coverage      # coverage only
bin/quality coverage-all  # whole-repo coverage report (informational)
```

CI runs on pull requests and pushes to `main`/`dev`: the local quality gate, `go vet`, a binary
build, a Docker build check, and an advisory `govulncheck` scan. The enforced coverage gate is 95%
for the deterministic core packages `internal/config` and `internal/subtitle`.

The static project website lives under [`docs/`](docs/) and can be served by GitHub Pages or any
static host. Brand assets live under [`assets/`](assets/).

## 📦 Releases

Public releases are scheduled rather than created on every merge. The release workflow runs every
12 hours (and can be started manually); it creates a release only when `main` has changed since the
latest release tag.

Release tags use date-based versions — `vYYYY.MM.DD.N` (e.g. `v2026.06.05.1`, then `v2026.06.05.2`
later the same day). Each release publishes Docker images to GitHub Container Registry:

```text
ghcr.io/pedro-revez-silva/subtitler:latest
ghcr.io/pedro-revez-silva/subtitler:vYYYY.MM.DD.N
```

## 📄 License

[GPL-3.0](LICENSE).
