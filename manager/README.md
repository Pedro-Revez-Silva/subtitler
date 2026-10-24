# Subtitle ownership

Enable `management.enabled` to replace the legacy generation-only scanner.
The daemon reconciles Radarr/Sonarr inventory, detects media imports and deleted
or edited captions, and maintains a SQLite catalogue outside the library.
Set `subtitles.required_languages` to the languages this installation manages:

```yaml
subtitles:
  required_languages: [en, pt-PT]
```

Other installations can choose, for example, `[fr, es, de]`, `[pt-BR]` or `[ja]`.
Use ISO language tags with an optional script/country, such as `zh-Hant` or
`fr-CA`. Regional requests require explicit matching evidence: generic `pt`
accepts Portuguese variants, while `pt-BR` and generic `pt` cannot satisfy
`pt-PT`. This setting drives existing-subtitle selection, provider requests,
verification, generation, filenames, cleanup and reporting. Changing it requeues
previously checked media while preserving the catalogue and paid ledger. Restart
the daemon after saving configuration changes.

For each language, processing follows this order:

1. Inspect existing sidecars and extract embedded text subtitles. Forced tracks
   do not satisfy the requirement for a full subtitle. PGS captions are read
   locally with OCR before searching providers.
2. Validate the text/timestamps and try local FFsubsync repair against the selected
   dialogue audio. Repair handles offsets and frame-rate differences. Structural
   and sampled word checks run again after repair. Strong matching words can
   establish offsets and drift; already aligned subtitles keep their timestamps.
3. If no usable local subtitle remains, search the configured providers and check
   every downloaded candidate through the same pipeline. Exact movie identity or
   series/season/episode matching is required; OpenSubtitles requests `pt-pt`.
4. Only after searches for all configured languages finish, consider generation.
   Local Whisper first generates a missing track from matching-language audio.
   An accepted subtitle can supply a translation when the target-language audio
   is absent or local generation fails. Paid transcription is the final fallback,
   enabled explicitly with a daily limit.
   Whisper's audio-translation task outputs English only. Other targets use text
   translation from an accepted subtitle in any language. If no requested source
   exists, the actual dialogue language is verified/cached outside the library;
   an English subtitle is not required in the configured target list.
   Text translations inherit the accepted source's exact timestamps;
   matching-language audio still requires the ordinary word/timing checks.
5. Publish atomically, save provenance and verification results, and quarantine
   superseded/rejected/unwanted captions. Provider outages defer generation.

FFsubsync uses speech/silence patterns and **does not prove that words match**.
For same-language audio, `verify_words` compares text and word timestamps in three
60-second samples using local Whisper base/int8. Mismatches reject a candidate;
inconclusive checks remain queued without triggering paid generation. Untried
provider candidates also defer generation. These are sampled checks, not a proof of every
line. Translated subtitles cannot be verified by literal word comparison against
foreign audio: their catalogue explicitly records `translated_text_unverified`.
Their identity is established by an embedded stream or provider metadata, or by
translation from an accepted source. Existing translated sidecars of unknown
provenance require a provider replacement. Blu-ray PGS bitmap captions use local
Tesseract OCR (English/Portuguese), retaining their original timestamps. OCR
confidence and the ordinary text/timing checks must pass. Unsupported bitmap
formats and cropped PGS compositions remain for review.

Create a private JSON provider file (`chmod 600`) outside the repository:

```json
{
  "providers": ["opensubtitlescom", "gestdown", "yifysubtitles"],
  "credentials": {
    "opensubtitlescom": {"username": "your-account", "password": "your-password"}
  }
}
```

Subliminal's additional supported providers can be configured in the same file.
YIFY compatibility searches match its language label to the configured target;
a generic Portuguese label cannot establish Portugal. Free-provider quotas and unavailable
providers are respected; an outage never causes paid generation.

```sh
subtitler status -config config.yaml
subtitler report -config config.yaml -lang pt-PT -reason out_of_sync /media/Film.mkv
# A unique catalogue title can be used instead of the media path.
subtitler report -config config.yaml -lang pt-PT -reason out_of_sync "Film"
```

Feedback reasons are `out_of_sync`, `wrong_text`, `wrong_language`, and `missing`.
Reports include the current media fingerprint and caption hash. The next scan
quarantines the reported track, rejects its source and both input/output hashes,
and searches for an alternative. Translations derived from a bad source are
invalidated too. A stale report cannot reject a newly imported movie.
`-lang` must name a configured language; it may be omitted when exactly one
language is configured. Status includes the effective target list.

`prune_embedded` performs a copy remux of MKV files, retaining every audio track,
video, metadata, chapters and attachments. Subtitler manages subtitles only:
audio is read to select a dialogue reference, check synchronization and provide
last-resort transcription. It does not remove, obtain or replace audio tracks,
and audio language preferences do not determine whether subtitle ownership is
complete. Legacy `prune_audio` settings are ignored.
Desired full embedded subtitles are removed only once all configured canonical sidecars
exist. Embedded cleanup runs only after every required canonical subtitle exists,
as a lower-priority job inside the file queue. Desired forced tracks are retained.
Ambiguous subtitle tracks are quarantined only after all configured canonical replacements
exist; until then they remain available for review.
Other containers remain for review. A verified backup hardlink is made outside
the library before replacing its media path. Seeding hardlinks keep their original
bytes. This consumes additional storage for remuxed media; the free-space floor
is enforced. MergerFS backups use the source's physical branch when pool link
policies reject a cross-directory link; mount each branch at its host path in
Docker. The pool's mount settings are untouched. Backups are not automatically deleted. Copy remuxes are serialized per physical
disk across both queues, keeping the free-space check and allocation in one
critical section while acquisition and verification remain parallel.

Availability depends on the selected providers and local tools. The bundled
Tesseract image includes English and Portuguese; install the corresponding
Tesseract trained-data package for additional bitmap languages. Unsupported
OCR/speech/text-detection languages remain unresolved instead of being processed
as English or silently accepted. Word and text checks compare the base language;
regional/script identity comes from explicit media/provider metadata or the
specified translation target.

When a dialogue stream has no usable language tag, three short local samples
identify its base language before literal word checks. At least two distinct
samples must agree with 90% confidence and contain speech; conflicting confident
samples remain inconclusive. Observations are cached by media fingerprint,
stream and model. Detection never establishes a regional variant such as pt-PT,
changes audio tracks, bypasses word/timing verification or makes paid requests.
Known language tags and text translations waiting on budget avoid this extra work.

Paid mode uses only `whisper-1` and `gpt-4o-mini` through the official API. Model
prices were checked on 2026-10-11. Every request reserves a conservative upper
bound in SQLite **before** making the call; crashes, failures and restarts cannot
reset the daily UTC allowance. Translation batches preserve cue timestamps and
are cached for resumption. Audio is chunked to stay below upload limits. Unknown
models/endpoints fail closed. This limit applies to this manager, not other apps
sharing the API key. No credentials are written to the catalogue or logs.

The ownership daemon has two persistent queues. `files` acquires, checks and
repairs existing/provider captions; `transcription` performs full local
transcription and last-resort paid transcription/translation. All configured
provider searches must complete first. Uncertain candidates stay in file
recovery instead of authorizing generation. Daily budget waits and slow speech
jobs never hold up the file queue. Queue rows contain paths, fingerprints and
retry state, not audio or credentials. Restarts recover interrupted claims and
retain validated captions, rejected candidates, translation cache and the paid
ledger. A per-media lock also protects against a surviving orphan worker.

Configure `management.queues` using the example YAML. Each queue has its own
worker limit and processes a bounded batch before refreshing inventory. A
positive `processing.max_jobs_per_scan` overrides the queue batch size for each
cycle. Adaptive admission starts conservatively, samples host CPU/load,
available memory (including container limits), and per-physical-disk utilization/latency, and ramps up after three idle
samples. CPU/memory pressure pauses admission globally. A busy disk pauses its
own readers, while text-only translation and other drives remain eligible.
Linux IO pressure is a fallback for devices without counters; IO-wait load does
not suppress text-only work. Each physical disk has at most one media job across
both queues, including when fixed concurrency is selected. Running jobs finish
safely. Worker processes run at reduced CPU priority and local Whisper uses
`worker_threads` per worker. Set `adaptive: false` for fixed concurrency.
Generation polls the persistent queue at least every 30 seconds, independently
of the library scan interval. Status reports both queues and their current
capacity/pressure. Non-MKV cleanup and insufficient-space reviews still require
their existing recovery paths; concurrency does not bypass storage safeguards.

`management.paid_daily_usd` remains a dollar limit shared by all generation
workers. Reservations use a SQLite writer transaction, so concurrent jobs
cannot each spend the same remaining allowance. Increasing the limit wakes
budget-deferred jobs while preserving prior reservations. Paid work still runs
only after existing subtitles, provider searches and local fallback fail.

Native development:

```sh
python3 -m venv /disk/cache/subtitler-venv
/disk/cache/subtitler-venv/bin/pip install -r manager/requirements.txt
SUBTITLER_PYTHON=/disk/cache/subtitler-venv/bin/python bin/quality push
```

Set `management.python` and `management.script` for native execution. In Docker
the defaults are `python3` and `/opt/subtitler/manager.py`. Back up the ownership
SQLite database (including WAL if it is live), provider file and quarantine,
alongside the existing configuration. Keep Bazarr stopped once the manager takes
over, so two services cannot race to replace the same sidecars.

Throughput and recovery:

- Media audio is lazy: initial word checks seek three short samples directly in
  the movie. Full audio is prepared only when synchronization/transcription needs
  it, or alongside an embedded-subtitle extraction that already needs a full read.
  Selected audio and useful subtitle streams share one FFmpeg input/pass.
- `management.media_cache_gb` (default 5) bounds idle extracted audio/subtitle and
  raw word-observation caches on the state disk. Active entries are leased and
  never evicted mid-job; temporary active working sets can exceed the idle bound.
  Media fingerprints, stream IDs, language, model and sample windows fence reuse.
  Reused observations still pass the ordinary text/timing acceptance checks.
- Text translation inherits its accepted source clock without extracting audio.
  Matching-language dialogue still requires word checks. Invalidated source
  captions return to the file queue before generation. Paid uploads check the
  remaining allowance before preparation and still reserve atomically before
  every request. No reservation is refunded or reset by these optimizations.
- Translation yields after the existing 20-call bound and becomes eligible again
  after five seconds, behind jobs that have not had a turn. Transport, provider,
  quality and quota failures keep their error-specific backoff. Budget waits
  resume after the next UTC reset. Validated translation chunks survive restarts.
- Removing embedded tracks remains required when configured, but lower-priority
  cleanup cannot displace subtitle acquisition. Incomplete films keep embedded
  tracks until their canonical replacements exist. The existing free-space floor,
  backup hardlinks and audio/video/metadata validation still guard every remux.
