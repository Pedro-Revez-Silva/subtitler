#!/usr/bin/env python3
"""Persistent subtitle ownership; local repair and providers precede paid fallback.

The Go application supplies ARR inventory via stdin. Providers and local tools
are deliberately lazy imports, so status/report never download models or media.
"""
import contextlib
from collections import Counter
import difflib
import errno
import fcntl
import hashlib
import io
import json
import logging
import os
from pathlib import Path
import re
import shutil
import signal
import sqlite3
import statistics
import subprocess
import sys
import tempfile
import time
import traceback
import unicodedata
import uuid
from scheduler import prepare, enqueue, drain, emit as queue_emit
from locales import ambiguous, base_language, language, matches, normalize, text_language_supported

logging.disable(logging.CRITICAL)  # third-party exceptions may contain credentials
SUB_EXTS = {".srt", ".ass", ".ssa", ".vtt", ".sub", ".idx", ".sup"}
TEXT_CODECS = {"subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text"}
REASONS = {"out_of_sync", "wrong_text", "wrong_language", "missing"}


def non_dialogue_audio(stream):
    flags = stream.get("disposition", {})
    return (flags.get("comment") or flags.get("descriptions") or flags.get("visual_impaired")
            or re.search(r"commentary|description|descriptive|isolated.?score|instrumental|music.?and.?effects|\bm\s*&\s*e\b",
                         stream.get("tags", {}).get("title", ""), re.I))


class Review(Exception):
    """A stable, non-secret reason that can be recorded in the catalogue."""
    def __init__(self, reason, diagnostic=None):
        super().__init__(reason)
        self.diagnostic = diagnostic or {}


def diagnostic(error, stage):
    result = {"stage": stage, "exception": type(error).__name__}
    if isinstance(error, Review):
        result.update(error.diagnostic)
    else:
        # Exception messages, local variables and command arguments can contain
        # provider credentials. Retain only code locations and exception types.
        result["frames"] = [{"file": Path(t.filename).name, "function": t.name, "line": t.lineno}
                            for t in traceback.extract_tb(error.__traceback__)[-4:]]
    return result


def media_duration(streams, format_duration):
    values = []
    for stream in streams:
        if stream.get("codec_type") not in {"audio", "video"} or stream.get("disposition", {}).get("attached_pic"):
            continue
        raw = stream.get("tags", {}).get("DURATION", "")
        try:
            hours, minutes, seconds = raw.split(":")
            values.append(int(hours) * 3600 + int(minutes) * 60 + float(seconds))
        except (ValueError, AttributeError):
            try: values.append(float(stream["duration"]))
            except (ValueError, KeyError, TypeError): pass
    return max(values) if values else float(format_duration)


def stream_signature(stream):
    tags, flags = stream.get("tags", {}), stream.get("disposition", {})
    return (stream["codec_type"], stream["codec_name"], language(tags.get("language"), tags.get("title")), tags.get("title", ""),
            tags.get("filename", ""), tags.get("mimetype", ""),
            stream.get("channels"), stream.get("sample_rate"), stream.get("width"), stream.get("height"),
            bool(flags.get("default")), bool(flags.get("forced")), bool(flags.get("attached_pic")))


def recovery_action(detail, status):
    if status == "queued_transcription": return "run_transcription"
    if status == "managed": return "periodic_recheck"
    if status == "managed_cleanup_pending": return "retry_subtitle_cleanup"
    failures = set()
    for value in detail.values():
        if not isinstance(value, dict): continue
        failures.update(value.get("failures", []))
        failures.update(str(value[k]) for k in ("translation_failure", "paid_failure", "error") if k in value)
        cause = value.get("translation_diagnostic", {}).get("cause") or value.get("paid_diagnostic", {}).get("cause")
        if cause in {"authentication", "quota", "access_denied"}: return "provider_configuration_required"
    if "daily_paid_budget_exhausted" in failures: return "wait_for_daily_budget"
    if "candidate_verification_pending" in failures: return "try_other_candidates"
    if "translation_call_limit" in failures or "invalid_paid_translation" in failures: return "resume_translation"
    if "translation_refused" in failures: return "translation_requires_review"
    return "retry_with_diagnostics"


def backing_path(path):
    try:
        physical = Path(os.getxattr(path, "user.mergerfs.fullpath").decode())
        if physical.is_absolute() and physical.is_file():
            return physical
    except OSError:
        pass
    return Path(path)


def fingerprint(path):
    # Read the real inode on a union pool: a direct branch replacement must not
    # look unchanged while the FUSE attribute cache still contains the old inode.
    stat = backing_path(path).stat()
    return f"{stat.st_dev}:{stat.st_ino}:{stat.st_size}:{stat.st_mtime_ns}"


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def canonical(path, lang):
    lang = normalize(lang)
    suffix = ".subtitler." + lang + ("." + base_language(lang) if "-" in lang else "") + ".srt"
    return str(Path(path).with_suffix("")) + suffix


def sidecars(path):
    video = Path(path)
    result = []
    for candidate in sorted(video.parent.iterdir()):
        if candidate.is_symlink() or not candidate.is_file() or candidate.suffix.lower() not in SUB_EXTS:
            continue
        name = candidate.stem
        if name != video.stem and not name.startswith(video.stem + "."):
            continue
        tokens = name[len(video.stem):].strip(".").split(".")
        lang = next((language(t) for t in tokens if language(t)), "")
        result.append({"path": str(candidate), "language": lang,
                       "forced": any(t.lower() in {"forced", "foreign"} for t in tokens)})
    return result


def words(text):
    text = re.sub(r"<[^>]*>|\{[^}]*\}|\[[^]]*\]", " ", text)
    text = unicodedata.normalize("NFKD", text.lower())
    text = "".join(c for c in text if not unicodedata.combining(c))
    # Keep non-Latin writing. CJK scripts do not require word separators, so
    # compare their characters against the same normalized ASR characters.
    text = re.sub(r"([\u3040-\u30ff\u3400-\u9fff\uf900-\ufaff\U00020000-\U0002fa1f])", r" \1 ", text)
    return re.findall(r"[^\W_]+", text)


def validate(cues, duration):
    if len(cues) < 10:
        raise Review("not_a_full_subtitle")
    previous = -1
    total = 0
    repetitions = {}
    for cue in cues:
        if cue.start < 0 or cue.end <= cue.start or cue.end > duration * 1000 + 2000:
            raise Review("invalid_timestamps")
        if cue.start < previous:
            raise Review("unordered_timestamps")
        previous = cue.start
        text = " ".join(words(cue.text))
        if not text:
            continue
        if len(text) >= 20:
            repetitions[text] = repetitions.get(text, 0) + 1
        total += len(text.split())
    if total < 50 or max(repetitions.values(), default=0) > max(8, len(cues) // 5):
        raise Review("empty_or_repetitive_text")
    if cues[-1].end - cues[0].start < duration * 1000 * 0.55 or cues[-1].end < duration * 1000 * 0.75 or cues[0].start > duration * 1000 * 0.25:
        raise Review("partial_or_wrong_cut")
    return {"cues": len(cues), "words": total, "last_cue_seconds": cues[-1].end / 1000}


def word_metrics(cues, recognized, start, end):
    """Compare words AND their timestamps; recognition errors remain inconclusive."""
    expected = []
    for cue in cues:
        if cue.start / 1000 >= start and cue.end / 1000 <= end:
            expected.extend((word, cue.start / 1000, cue.end / 1000) for word in words(cue.text))
    heard = [(word, token[1], token[2]) for token in recognized for word in words(token[0])]
    matcher = difflib.SequenceMatcher(None, [w[0] for w in expected], [w[0] for w in heard], autojunk=False)
    matches = [(expected[i], heard[j]) for a, b, n in matcher.get_matching_blocks()
               if n >= 2 for i, j in zip(range(a, a + n), range(b, b + n))]
    if len(matches) < 12:
        return {"status": "inconclusive", "matched_words": len(matches)}
    in_time = sum(e[1] - 1.2 <= (h[1] + h[2]) / 2 <= e[2] + 1.2 for e, h in matches)
    score = len(matches) / max(1, min(len(expected), len(heard)))
    good = score >= 0.4 and in_time / len(matches) >= 0.8
    status = "inconclusive" if score < 0.4 else ("verified" if good else "mismatch")
    return {"status": status, "matched_words": len(matches),
            "text_match": round(score, 3), "timed_match": round(in_time / len(matches), 3),
            "sample_midpoint": (start + end) / 2,
            "offset_seconds": statistics.median((h[1] + h[2]) / 2 - (e[1] + e[2]) / 2 for e, h in matches)}


def text_alignment(cues, checks):
    anchors = [m for m in checks.get("samples", []) if m.get("text_match", 0) >= 0.4 and m.get("matched_words", 0) >= 12]
    if len(anchors) < 2:
        return None
    slopes = [(b["offset_seconds"] - a["offset_seconds"]) / (b["sample_midpoint"] - a["sample_midpoint"])
              for i, a in enumerate(anchors) for b in anchors[i + 1:] if b["sample_midpoint"] != a["sample_midpoint"]]
    slope = statistics.median(slopes) if slopes else 0
    intercept = statistics.median(m["offset_seconds"] - slope * m["sample_midpoint"] for m in anchors)
    residual = max(abs(m["offset_seconds"] - (intercept + slope * m["sample_midpoint"])) for m in anchors)
    if abs(slope) > 0.05 or abs(intercept) > 60 or residual > 1.0:
        return None
    for cue in cues:
        cue.start = round(cue.start * (1 + slope) + intercept * 1000)
        cue.end = round(cue.end * (1 + slope) + intercept * 1000)
    return {"method": "sampled_text_audio_alignment", "offset_seconds": round(intercept, 3),
            "framerate_scale": 1 + slope, "anchor_residual_seconds": round(residual, 3), "content_proof": "three_samples"}


def run(command, timeout=7200, accepted_codes=(0,)):
    tool = Path(command[0]).name
    try:
        result = subprocess.run(command, capture_output=True, timeout=timeout, check=False)
    except subprocess.TimeoutExpired:
        raise Review("media_tool_timeout", {"tool": tool, "timeout_seconds": timeout}) from None
    if result.returncode not in accepted_codes:
        stderr = result.stderr.decode(errors="replace").lower()
        category = "tool_error"
        for needles, name in [(["no space left"], "disk_full"), (["permission denied"], "permission"),
                              (["timestamp", "non monoton", "unknown time"], "timestamps"),
                              (["not supported", "unsupported", "could not find codec"], "unsupported_stream")]:
            if any(needle in stderr for needle in needles):
                category = name
                break
        raise Review("media_tool_failed", {"tool": tool, "exit_code": result.returncode, "category": category})
    return result.stdout


class Catalogue:
    def __init__(self, root):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)
        self.db = sqlite3.connect(self.root / "ownership.sqlite", timeout=30)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.executescript("""
            CREATE TABLE IF NOT EXISTS media(path TEXT PRIMARY KEY, fingerprint TEXT, item TEXT,
                checked REAL DEFAULT 0, next_try REAL DEFAULT 0, status TEXT DEFAULT 'pending', detail TEXT DEFAULT '{}');
            CREATE TABLE IF NOT EXISTS captions(path TEXT, fingerprint TEXT, language TEXT, sha TEXT, data TEXT,
                PRIMARY KEY(path, language));
            CREATE TABLE IF NOT EXISTS rejected(path TEXT, fingerprint TEXT, language TEXT, sha TEXT, source TEXT,
                reason TEXT, PRIMARY KEY(path, fingerprint, language, sha, source));
            CREATE TABLE IF NOT EXISTS feedback(id INTEGER PRIMARY KEY, path TEXT, fingerprint TEXT,
                language TEXT, sha TEXT, reason TEXT, applied INTEGER DEFAULT 0);
            CREATE TABLE IF NOT EXISTS rescans(path TEXT PRIMARY KEY, item TEXT);
            CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT);
            CREATE TABLE IF NOT EXISTS candidate_reviews(path TEXT, fingerprint TEXT, language TEXT, sha TEXT,
                source TEXT, reason TEXT, retry_after REAL, PRIMARY KEY(path,fingerprint,language,sha,source));
        """)
        self.db.commit()
        prepare(self.db)

    def caption(self, path, fp, lang):
        row = self.db.execute("SELECT sha,data FROM captions WHERE path=? AND fingerprint=? AND language=?",
                              (path, fp, lang)).fetchone()
        return (row[0], json.loads(row[1])) if row else None

    def rejected(self, path, fp, lang, sha="", source=""):
        return self.db.execute("SELECT 1 FROM rejected WHERE path=? AND fingerprint=? AND language=? AND "
                               "((sha<>'' AND sha=?) OR (source<>'' AND source=?))",
                               (path, fp, lang, sha, source)).fetchone() is not None

    def reject(self, path, fp, lang, sha, source, reason):
        self.db.execute("INSERT OR IGNORE INTO rejected VALUES(?,?,?,?,?,?)", (path, fp, lang, sha, source, reason))
        self.db.execute("DELETE FROM candidate_reviews WHERE path=? AND fingerprint=? AND language=? AND "
                        "((sha<>'' AND sha=?) OR (source<>'' AND source=?))", (path, fp, lang, sha, source))
        self.db.commit()

    def defer(self, path, fp, lang, sha, source, reason):
        self.db.execute("INSERT OR REPLACE INTO candidate_reviews VALUES(?,?,?,?,?,?,?)",
                        (path, fp, lang, sha, source, reason, time.time() + 6 * 3600))
        self.db.commit()

    def deferred(self, path, fp, lang, sha="", source=""):
        condition, identity = ("source=?", source) if source else ("source='' AND sha=?", sha)
        return self.db.execute("SELECT 1 FROM candidate_reviews WHERE path=? AND fingerprint=? AND language=? AND "
                               "retry_after>? AND " + condition,
                               (path, fp, lang, time.time(), identity)).fetchone() is not None

    def record(self, path, fp, lang, sha, data):
        self.db.execute("INSERT OR REPLACE INTO captions VALUES(?,?,?,?,?)", (path, fp, lang, sha, json.dumps(data)))
        self.db.commit()

    def report(self, path, lang, reason):
        try:
            lang = normalize(lang)
        except ValueError:
            raise Review("invalid_feedback") from None
        if reason not in REASONS:
            raise Review("invalid_feedback")
        if not Path(path).is_absolute():
            matches = [p for p, item in self.db.execute("SELECT path,item FROM media")
                       if json.loads(item).get("Title", "").casefold() == path.casefold()]
            if len(matches) != 1:
                raise Review("title_missing_or_ambiguous_use_media_path")
            path = matches[0]
        row = self.db.execute("SELECT fingerprint FROM media WHERE path=?", (path,)).fetchone()
        if not row or not Path(path).exists() or row[0] != fingerprint(path):
            raise Review("media_changed_rescan_first")
        caption = self.caption(path, row[0], lang)
        if not caption and reason != "missing":
            raise Review("no_managed_subtitle_for_feedback")
        sha = caption[0] if caption else ""
        if caption and (not Path(caption[1]["output"]).exists() or digest(caption[1]["output"]) != sha):
            raise Review("subtitle_changed_rescan_first")
        self.db.execute("INSERT INTO feedback(path,fingerprint,language,sha,reason) VALUES(?,?,?,?,?)",
                        (path, row[0], lang, sha, reason))
        self.db.execute("UPDATE media SET next_try=0,checked=0 WHERE path=?", (path,))
        self.db.commit()
        return {"feedback": "queued", "path": path, "language": lang, "reason": reason}


class Manager:
    def __init__(self, request):
        self.request = request
        self.cfg = request["management"]
        self.catalogue = Catalogue(self.cfg["state_dir"])
        os.environ.setdefault("HF_HOME", str(self.catalogue.root / "models" / "hub"))
        os.environ.setdefault("HF_HUB_DISABLE_TELEMETRY", "1")
        try:
            configured = request.get("languages", ["en"])
            if not configured:
                raise ValueError("empty_required_subtitle_languages")
            self.languages = list(dict.fromkeys(normalize(v) for v in configured))
        except ValueError:
            raise Review("invalid_required_subtitle_language") from None
        self.ffmpeg = request.get("ffmpeg") or "ffmpeg"
        self.ffprobe = request.get("ffprobe") or "ffprobe"
        self.quarantine = Path(self.cfg["quarantine_dir"])
        self.quarantine.mkdir(parents=True, exist_ok=True)
        self.model = None
        self.models = {}
        self.word_samples = {}

    def emit(self, **record):
        # Both independent coordinators and all workers share this record lock.
        # Large JSON diagnostics must remain one line in the daemon log.
        with (self.catalogue.root / 'output.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            queue_emit(record)

    def probe(self, path):
        result = json.loads(run([self.ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", path], 120))
        duration = media_duration(result["streams"], result["format"]["duration"])
        if duration <= 0:
            raise Review("unknown_duration")
        return result["streams"], duration

    def audio(self, streams, lang, original):
        audios = [s for s in streams if s["codec_type"] == "audio"
                  and not non_dialogue_audio(s)]
        for wanted in [lang, original, *self.languages]:
            for stream in audios:
                tags = stream.get("tags", {})
                if wanted and matches(language(tags.get("language"), tags.get("title")), wanted):
                    return stream
            for stream in audios:
                tags = stream.get("tags", {})
                if wanted and ambiguous(language(tags.get("language"), tags.get("title")), wanted):
                    # A generic same-language track can verify words. Its tag
                    # still cannot establish the locale for generated subtitles.
                    return stream
        if not audios:
            raise Review("no_dialogue_audio")
        return next((s for s in audios if s.get("disposition", {}).get("default")), audios[0])

    def archive(self, path, media, expected=None):
        path = Path(path)
        if not path.exists():
            return None
        sha = digest(path)
        if expected and sha != expected:
            raise Review("subtitle_changed_during_work")
        directory = self.quarantine / "subtitles" / hashlib.sha256(media.encode()).hexdigest()[:20]
        directory.mkdir(parents=True, exist_ok=True)
        target = directory / (sha[:16] + "-" + path.name)
        if not target.exists():
            shutil.copy2(path, target)
        if digest(target) != sha or digest(path) != sha:
            raise Review("archive_verification_failed")
        path.unlink()  # preserve any other hardlinks; never overwrite a seeded inode
        return str(target)

    def feedback(self, path, fp):
        rows = self.catalogue.db.execute("SELECT id,fingerprint,language,sha,reason FROM feedback WHERE path=? AND applied=0", (path,)).fetchall()
        for id_, reported_fp, lang, sha, reason in rows:
            if reported_fp == fp:
                saved = self.catalogue.caption(path, fp, lang)
                if saved and saved[0] == sha:
                    if fingerprint(path) != fp:
                        raise Review("media_changed_during_work")
                    self.archive(saved[1]["output"], path, sha)
                    self.catalogue.reject(path, fp, lang, sha, saved[1].get("source_id", ""), reason)
                    self.catalogue.reject(path, fp, lang, saved[1].get("input_sha", ""), "", reason)
                    # A translated track inherits the source text. Reporting a
                    # bad source invalidates those dependent tracks as well.
                    for derived_lang, derived_sha, raw in self.catalogue.db.execute("SELECT language,sha,data FROM captions WHERE path=? AND fingerprint=?", (path, fp)).fetchall():
                        derived = json.loads(raw)
                        if derived.get("derived_from_sha") == sha:
                            self.archive(derived["output"], path, derived_sha)
                            self.catalogue.reject(path, fp, derived_lang, derived_sha, derived.get("source_id", ""), "bad_translation_source")
                            self.catalogue.db.execute("DELETE FROM captions WHERE path=? AND language=?", (path, derived_lang))
                    self.catalogue.db.execute("DELETE FROM captions WHERE path=? AND language=?", (path, lang))
            self.catalogue.db.execute("UPDATE feedback SET applied=1 WHERE id=?", (id_,))
        self.catalogue.db.commit()

    def load(self, path, duration):
        import pysubs2
        try:
            data = Path(path).read_bytes()
            if len(data) > 10_000_000:
                raise Review("subtitle_too_large")
            try:
                text = data.decode("utf-8-sig")
            except UnicodeDecodeError:
                from charset_normalizer import from_bytes
                best = from_bytes(data).best()
                if best is None:
                    raise Review("subtitle_encoding_unknown")
                text = str(best)
            cues = pysubs2.SSAFile.from_string(text, format_=Path(path).suffix.lstrip("."))
        except Review:
            raise
        except Exception:
            raise Review("invalid_subtitle") from None
        validate(cues, duration)
        return cues

    def sync(self, reference, candidate, output):
        from ffsubsync.ffsubsync import make_parser, run as synchronize
        subtitle_reference = Path(reference).suffix.lower() == ".srt"
        speech_cache = Path(reference).with_suffix(".npz")
        selected_reference = speech_cache if speech_cache.exists() else reference
        arguments = [str(selected_reference), "-i", str(candidate), "-o", str(output),
                                        "--vad", "webrtc", "--skip-sync-on-low-quality",
                                        "--quality-max-offset-seconds", "45", "--max-offset-seconds", "60",
                                        "--max-framerate-deviation", "0.05",
                                        "--ffmpeg-path", self.ffmpeg]
        if not speech_cache.exists() and not subtitle_reference:
            arguments.append("--serialize-speech")
        args = make_parser().parse_args(arguments)
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            result = synchronize(args)
        if not result.get("sync_was_successful") or not output.exists():
            raise Review("timing_alignment_inconclusive", {"method": "ffsubsync_subtitle_reference" if subtitle_reference else "ffsubsync_vad",
                         **{k: result[k] for k in ("offset_seconds", "framerate_scale_factor", "score")
                            if type(result.get(k)) in {int, float}}})
        return {"method": "ffsubsync_subtitle_reference" if subtitle_reference else "ffsubsync_vad", "offset_seconds": result["offset_seconds"],
                "framerate_scale": result["framerate_scale_factor"], "content_proof": False}

    def speech_model(self, name=None):
        name = name or self.cfg.get("verification_model") or "base"
        if name not in self.models:
            from faster_whisper import WhisperModel
            self.models[name] = WhisperModel(name, device="cpu", compute_type="int8", cpu_threads=self.cfg.get("queues", {}).get("worker_threads", 2),
                                            download_root=str(self.catalogue.root / "models"))
        return self.models[name]

    def verify_words(self, reference, cues, lang, work):
        # Only literal word comparisons in the SAME language are evidence. A
        # translated subtitle cannot be semantically verified by audio VAD.
        starts = [c.start / 1000 for c in cues if len(words(c.text)) >= 4]
        if len(starts) < 12:
            return {"status": "inconclusive", "reason": "too_little_dialogue"}
        primary = self.cfg.get("verification_model") or "base"
        fallback = self.cfg.get("verification_fallback_model") or "small"
        attempts = []
        for name in dict.fromkeys([primary, fallback]):
            if name == "none": continue
            seconds = 60 if name == primary else 90
            key = (str(reference), base_language(lang), name)
            if key not in self.word_samples:
                samples = []
                self.emit(stage="verifying_dialogue", language=lang, model=name)
                for fraction in [0.15, 0.5, 0.85]:
                    start = max(0, starts[int((len(starts) - 1) * fraction)] - 3)
                    sample = work / f"sample-{name}-{fraction}.wav"
                    run([self.ffmpeg, "-v", "error", "-y", "-ss", str(start), "-i", str(reference),
                         "-t", str(seconds), "-ar", "16000", "-ac", "1", str(sample)], 120)
                    segments, info = self.speech_model(name).transcribe(str(sample), language=base_language(lang),
                                                                      word_timestamps=True, vad_filter=True, beam_size=5)
                    recognized = [(w.word, w.start + start, w.end + start) for s in segments for w in (s.words or [])]
                    samples.append((start, recognized))
                self.word_samples[key] = samples
            metrics = [word_metrics(cues, recognized, start, start + seconds) for start, recognized in self.word_samples[key]]
            good = sum(m["status"] == "verified" for m in metrics)
            status = "mismatch" if any(m["status"] == "mismatch" for m in metrics) else ("verified_samples" if good >= 2 else "inconclusive")
            attempts.append({"model": name, "status": status})
            result = {"status": status, "samples": metrics, "attempts": attempts,
                      "method": f"local_whisper_{name}_int8", "scope": f"three_{seconds}_second_samples"}
            if status != "inconclusive": return result
        return result

    def local_candidates(self, item, streams, lang, work):
        path = item["Path"]
        cached = self.catalogue.caption(path, fingerprint(path), lang)
        if cached and cached[1].get("internal_source") and lang in self.languages:
            if Path(cached[1]["output"]).exists() and digest(cached[1]["output"]) == cached[0]:
                yield Path(cached[1]["output"]), {**cached[1], "internal_source": False}
        for sidecar in sidecars(path):
            if matches(sidecar["language"], lang) and not sidecar["forced"] and Path(sidecar["path"]).suffix.lower() not in {".idx", ".sup", ".sub"}:
                yield Path(sidecar["path"]), {"source": "sidecar", "source_id": "", "original_path": sidecar["path"]}
        for stream in streams:
            tags = stream.get("tags", {})
            if stream["codec_type"] != "subtitle" or stream.get("disposition", {}).get("forced"):
                continue
            if re.search(r"commentary|forced|foreign|signs(?: and songs)?", tags.get("title", ""), re.I):
                continue
            if not matches(language(tags.get("language"), tags.get("title")), lang):
                continue
            if stream["codec_name"] == "hdmv_pgs_subtitle":
                from pgs import convert
                cache_dir = self.catalogue.root / "ocr"
                cache_dir.mkdir(exist_ok=True)
                key = hashlib.sha256((path + fingerprint(path) + str(stream["index"]) + lang).encode()).hexdigest()
                output = cache_dir / (key + ".srt")
                metadata = cache_dir / (key + ".json")
                try:
                    cached = json.loads(metadata.read_text()) if metadata.exists() else {}
                    if not output.exists() or cached.get("sha") != digest(output):
                        bitmap = work / f"embedded-{stream['index']}.sup"
                        run([self.ffmpeg, "-v", "error", "-y", "-i", path, "-map", f"0:{stream['index']}", "-c:s", "copy", str(bitmap)])
                        _, duration = self.probe(path)
                        staged = work / "ocr.srt"
                        self.emit(path=path, language=lang, stage="reading_existing_bitmap_subtitles")
                        metrics = convert(bitmap, staged, lang, duration)
                        partial = cache_dir / (key + ".part")
                        shutil.copy2(staged, partial)
                        os.replace(partial, output)
                        metrics["sha"] = digest(output)
                        partial.write_text(json.dumps(metrics))
                        os.replace(partial, metadata)
                    else:
                        metrics = {**cached, "cached": True}
                    yield output, {"source": "embedded_ocr", "source_id": f"embedded:{stream['index']}", "ocr": metrics}
                except Exception:
                    continue
                continue
            if stream["codec_name"] not in TEXT_CODECS:
                continue
            output = work / f"embedded-{stream['index']}.srt"
            try:
                run([self.ffmpeg, "-v", "error", "-y", "-i", path, "-map", f"0:{stream['index']}", str(output)])
            except Review:
                continue
            yield output, {"source": "embedded", "source_id": f"embedded:{stream['index']}"}

    def provider_candidates(self, item, lang, work):
        from providers import candidates
        self.provider_health = {}
        fp = fingerprint(item["Path"])
        yield from candidates(item, lang, work, self.cfg.get("providers_file"),
                              lambda sha, source: (self.catalogue.rejected(item["Path"], fp, lang, sha, source)
                                                   or self.catalogue.deferred(item["Path"], fp, lang, sha, source)),
                              self.provider_health)

    def transcribe(self, reference, lang, work, duration, source_language=None, translate=False):
        import pysubs2
        selected = base_language(source_language or lang)
        segments, info = self.speech_model().transcribe(str(reference), language=selected or None,
                                                      task="translate" if translate else "transcribe",
                                                      vad_filter=True, word_timestamps=False, beam_size=5)
        cues = pysubs2.SSAFile()
        for segment in segments:
            if segment.no_speech_prob < 0.6 and segment.avg_logprob > -1.0:
                cues.append(pysubs2.SSAEvent(start=round(segment.start * 1000), end=round(segment.end * 1000), text=segment.text.strip()))
        validate(cues, duration)
        output = work / "local-transcription.srt"
        cues.save(str(output), format_="srt")
        return output, {"source": "local_transcription", "source_id": "local:whisper-base:" + lang,
                        "translated_audio": translate, "source_audio_language": selected}

    def publish(self, item, fp, lang, candidate, provenance, reference, audio, duration, work):
        path = item["Path"]
        original_sha = digest(candidate)
        if self.catalogue.rejected(path, fp, lang, original_sha, provenance.get("source_id", "")):
            raise Review("previously_rejected")
        cues = self.load(candidate, duration)
        from langdetect import DetectorFactory, detect_langs
        DetectorFactory.seed = 0
        text = " ".join(c.plaintext for c in cues)
        detected = detect_langs(text[:30000])
        expected = base_language(lang)
        if not text_language_supported(lang):
            raise Review("subtitle_language_detector_unavailable")
        if not detected or base_language(detected[0].lang) != expected or detected[0].prob < .8:
            raise Review("subtitle_language_mismatch")
        normalized = work / "normalized.srt"
        cues.save(str(normalized), format_="srt")
        aligned = work / "aligned.srt"
        tags = audio.get("tags", {})
        audio_lang = language(tags.get("language"), tags.get("title"))
        same_language = bool(audio_lang) and base_language(audio_lang) == base_language(lang)
        content = {"status": "not_checked" if same_language else "translated_text_unverified"}
        timing = None
        if provenance.get("derived_from_sha"):
            source_lang = provenance["derived_from_language"]
            source = self.catalogue.caption(path, fp, source_lang)
            if not source or source[0] != provenance["derived_from_sha"] or digest(source[1]["output"]) != source[0]:
                raise Review("translation_source_changed_during_work")
            source_cues = self.load(source[1]["output"], duration)
            if [(c.start, c.end) for c in cues] != [(c.start, c.end) for c in source_cues]:
                raise Review("translation_changed_verified_timestamps")
            shutil.copy2(normalized, aligned)
            timing = {"method": "inherited_accepted_source_timestamps", "source_sha": source[0],
                      "source_verification": source[1].get("timing", {}), "content_proof": False}
            content = {"status": "translated_from_accepted_subtitle", "source_language": source_lang,
                       "source_content_verification": source[1].get("content", {}).get("status"), "translation_words_unverified": True}
        if same_language and self.cfg.get("verify_words") and (timing is None or provenance.get("derived_from_sha")):
            try:
                content = self.verify_words(reference, cues, lang, work)
                if content["status"] == "verified_samples":
                    if timing is None:
                        timing = {"method": "sampled_word_timestamps", "offset_seconds": 0, "framerate_scale": 1,
                                  "content_proof": "three_samples"}
                    shutil.copy2(normalized, aligned)
            except Review as error:
                if str(error) != "words_do_not_match_audio_timing":
                    raise
            except Exception:
                raise Review("local_word_verifier_unavailable") from None
        if timing is None:
            if same_language and content["status"] == "mismatch":
                timing = text_alignment(cues, content)
                if timing:
                    cues.save(str(aligned), format_="srt")
            if timing is None:
                try:
                    timing = self.sync(reference, normalized, aligned)
                except Review as error:
                    if str(error) != "timing_alignment_inconclusive" or same_language:
                        raise
                    source = next((self.catalogue.caption(path, fp, wanted) for wanted in self.languages
                                   if wanted != lang and self.catalogue.caption(path, fp, wanted)), None)
                    if not source or not Path(source[1]["output"]).is_file() or digest(source[1]["output"]) != source[0]:
                        raise
                    timing = self.sync(source[1]["output"], normalized, aligned)
                    timing["reference_sha"] = source[0]
            cues = self.load(aligned, duration)
            if same_language and self.cfg.get("verify_words"):
                try:
                    content = self.verify_words(reference, cues, lang, work)
                except Review:
                    raise
                except Exception:
                    raise Review("local_word_verifier_unavailable") from None
        structure = validate(cues, duration)
        if same_language and self.cfg.get("verify_words"):
            if content["status"] == "mismatch":
                raise Review("words_do_not_match_audio_timing", {"verification": content})
            if content["status"] != "verified_samples":
                raise Review("word_verification_inconclusive", {"verification": content})
        elif not same_language and provenance["source"] == "sidecar" and not provenance.get("owned_before"):
            # Unknown local translated tracks require a provider edition match,
            # or a human judgement; VAD alone cannot prove which film they are.
            raise Review("translated_sidecar_identity_unverified")
        internal = lang not in self.languages
        if internal:
            directory = self.catalogue.root / "sources" / hashlib.sha256((path + fp).encode()).hexdigest()[:20]
            directory.mkdir(parents=True, exist_ok=True)
            output = str(directory / (normalize(lang) + ".srt"))
        else:
            output = canonical(path, lang)
        if fingerprint(path) != fp:
            raise Review("media_changed_during_work")
        final_sha = digest(aligned)
        if self.catalogue.rejected(path, fp, lang, final_sha):
            raise Review("previously_rejected")
        original_backup = None
        if Path(output).exists():
            original_backup = self.archive(output, path)
        fd, staged = tempfile.mkstemp(prefix=".subtitler-", suffix=".part", dir=str(Path(output).parent))
        try:
            with os.fdopen(fd, "wb") as handle:
                handle.write(aligned.read_bytes())
                handle.flush()
                os.fsync(handle.fileno())
            os.chmod(staged, 0o644)
            if fingerprint(path) != fp:
                raise Review("media_changed_during_work")
            os.replace(staged, output)
        finally:
            if os.path.exists(staged):
                os.unlink(staged)
        data = {**provenance, "output": output, "internal_source": internal, "input_sha": original_sha, "sha": final_sha,
                "structure": structure, "timing": timing, "content": content,
                "audio_stream": audio["index"], "audio_language": audio_lang,
                "checked_at": time.time(), "backup": original_backup}
        self.catalogue.record(path, fp, lang, final_sha, data)
        return data

    def ensure(self, item, fp, streams, duration, lang, work):
        path = item["Path"]
        saved = self.catalogue.caption(path, fp, lang)
        if (saved and not (saved[1].get("internal_source") and lang in self.languages)
                and Path(saved[1]["output"]).exists() and digest(saved[1]["output"]) == saved[0]):
            selected = self.audio(streams, lang, language(item.get("OriginalLanguage"))) if streams else None
            if selected is None or "audio_stream" not in saved[1] or selected["index"] == saved[1]["audio_stream"]:
                return saved[1]
        self.emit(path=path, language=lang, stage="checking_existing_subtitles")
        original = language(item.get("OriginalLanguage"))
        audio = self.audio(streams, lang, original)
        reference = work / f"audio-{audio['index']}.wav"
        if not reference.exists():
            run([self.ffmpeg, "-v", "error", "-y", "-i", path, "-map", f"0:{audio['index']}",
                 "-vn", "-ac", "1", "-ar", "16000", str(reference)])
        failures = []
        diagnostics = []
        for group in [self.local_candidates, self.provider_candidates]:
            if group == self.provider_candidates:
                self.emit(path=path, language=lang, stage="searching_providers")
            try:
                for candidate, provenance in group(item, streams, lang, work) if group == self.local_candidates else group(item, lang, work):
                    candidate_sha = digest(candidate)
                    candidate_source = provenance.get("source_id") or provenance.get("source", "local") + ":" + candidate_sha
                    if self.catalogue.deferred(path, fp, lang, candidate_sha, candidate_source):
                        failures.append("candidate_verification_pending")
                        continue
                    if saved and provenance.get("original_path") == saved[1]["output"]:
                        provenance["owned_before"] = True
                    try:
                        return self.publish(item, fp, lang, candidate, provenance, reference, audio, duration, work)
                    except Review as error:
                        failures.append(str(error))
                        diagnostics.append({"reason": str(error), **diagnostic(error, "candidate_verification")})
                        if str(error) in {"word_verification_inconclusive", "timing_alignment_inconclusive", "translated_sidecar_identity_unverified"}:
                            self.catalogue.defer(path, fp, lang, candidate_sha, candidate_source, str(error))
                        if str(error) not in {"media_changed_during_work", "local_word_verifier_unavailable", "subtitle_language_detector_unavailable", "word_verification_inconclusive", "timing_alignment_inconclusive", "translated_sidecar_identity_unverified", "previously_rejected"}:
                            self.catalogue.reject(path, fp, lang, digest(candidate), provenance.get("source_id", ""), str(error))
                    except Exception as error:
                        failures.append("candidate_verifier_failed")
                        diagnostics.append(diagnostic(error, "candidate_verification"))
            except Review as error:
                failures.append(str(error))
                diagnostics.append({"reason": str(error), **diagnostic(error, "candidate_search")})
            except Exception as error:
                failures.append("provider_unavailable")
                diagnostics.append(diagnostic(error, "candidate_search"))
        if self.catalogue.db.execute("SELECT 1 FROM candidate_reviews WHERE path=? AND fingerprint=? AND language=?",
                                     (path, fp, lang)).fetchone():
            failures.append("candidate_verification_pending")
        tags = audio.get("tags", {})
        return {"status": "missing_or_unverified", "failures": sorted(set(failures)),
                "providers": getattr(self, "provider_health", {}), "generation_attempted": False, "diagnostics": diagnostics[-10:]}

    def generation_blocker(self, lang, current):
        if not text_language_supported(lang):
            return "subtitle_language_detector_unavailable"
        unavailable = {"local_word_verifier_unavailable", "subtitle_language_detector_unavailable"}.intersection(current.get("failures", []))
        if unavailable:
            return sorted(unavailable)[0]
        uncertain = {"word_verification_inconclusive", "timing_alignment_inconclusive", "translated_sidecar_identity_unverified", "candidate_verification_pending", "candidate_verifier_failed"}
        if uncertain.intersection(current.get("failures", [])):
            return "existing_candidate_needs_review"
        health = current.get("providers", {})
        if any(state != "searched" for state in health.values()) or not health:
            return "provider_search_incomplete"
        return None

    def needs_generation(self, detail):
        return bool(self.cfg.get("local_transcription") or self.cfg.get("paid_fallback")) and any(
            "output" not in detail.get(lang, {}) and not self.generation_blocker(lang, detail.get(lang, {}))
            for lang in self.languages)

    def fallback(self, item, fp, streams, duration, lang, detail, work):
        from paid import Paid, PaidUnavailable
        current = detail[lang]
        if "output" in current:
            return current
        blocker = self.generation_blocker(lang, current)
        if blocker:
            return {**current, "generation_deferred": blocker}
        self.emit(path=item["Path"], language=lang, stage="last_resort_generation")
        audio = self.audio(streams, lang, language(item.get("OriginalLanguage")))
        reference = work / f"audio-{audio['index']}.wav"
        tags = audio.get("tags", {})
        audio_lang = language(tags.get("language"), tags.get("title"))
        target_audio = matches(audio_lang, lang)
        # Whisper's audio-translation task outputs English only. Other targets
        # use transcription in the actual dialogue language or text translation.
        translate_audio = lang == "en" and bool(audio_lang) and base_language(audio_lang) != "en"
        # Free transcription of the actual dialogue precedes any paid operation
        # when it can supply the requested language. Translated text can inherit
        # a verified source's clock after provider searches.
        if (target_audio or translate_audio) and self.cfg.get("local_transcription"):
            try:
                candidate, provenance = self.transcribe(reference, lang, work, duration,
                                                       source_language=audio_lang if translate_audio else None, translate=translate_audio)
                return self.publish(item, fp, lang, candidate, provenance, reference, audio, duration, work)
            except Review as error:
                current = {**current, "local_transcription_failure": str(error)}
            except Exception:
                current = {**current, "local_transcription_failure": "local_model_unavailable"}
        other = next((l for l, d in detail.items() if l != lang and isinstance(d, dict) and "output" in d), "")
        source = detail.get(other, {})
        if ("output" not in source and not target_audio and not translate_audio
                and audio_lang and self.cfg.get("paid_fallback")):
            # A single requested language still works with foreign dialogue.
            # Verify/cache its actual-language source outside the media library,
            # using free subtitles/local transcription before paid operations.
            other = audio_lang
            source = self.ensure(item, fp, streams, duration, other, work)
            if "output" not in source:
                source = self.fallback(item, fp, streams, duration, other, {other: source}, work)
            if "output" not in source:
                return {**current, "generation_deferred": "verified_translation_source_unavailable",
                        "translation_source_failure": source.get("generation_deferred") or source.get("local_transcription_failure") or source.get("paid_failure")}
        # Translation reuses verified cue timing instead of transcribing the
        # same film again. It is available only AFTER target-language searches.
        if "output" in source and self.cfg.get("paid_fallback"):
            source_sha = digest(source["output"])
            source_id = "translate:" + source_sha + ":" + lang
            if not self.catalogue.rejected(item["Path"], fp, lang, source=source_id):
                try:
                    paid = Paid(self.catalogue.db, self.request.get("openai", {}), self.cfg.get("paid_daily_usd", 0))
                    cues = self.load(source["output"], duration)
                    for cue, text in zip(cues, paid.translate(cues, lang, item.get("Context", ""))):
                        cue.text = text
                    output = work / "translated.srt"
                    cues.save(str(output), format_="srt")
                    provenance = {"source": "paid_translation", "source_id": source_id,
                                  "derived_from_sha": source_sha, "derived_from_language": other,
                                  "language_evidence": "explicit_translation_target:" + lang}
                    return self.publish(item, fp, lang, output, provenance, reference, audio, duration, work)
                except PaidUnavailable as error:
                    current = {**current, "translation_failure": str(error), "translation_diagnostic": error.diagnostic}
                    # Budget or provider problems must not trigger a more
                    # expensive transcription of the same known subtitle.
                    return current
                except Review as error:
                    current = {**current, "translation_failure": str(error)}
                    if str(error) != "words_do_not_match_audio_timing":
                        return current
        if not target_audio and not translate_audio:
            return {**current, "generation_deferred": "no_confirmed_target_language_audio_or_verified_translation_source"}
        if not self.cfg.get("paid_fallback"):
            return current
        try:
            import pysubs2
            paid = Paid(self.catalogue.db, self.request.get("openai", {}), self.cfg.get("paid_daily_usd", 0))
            cues = pysubs2.SSAFile()
            for offset in range(0, int(duration) + 1, 1200):
                seconds = min(1200, duration - offset)
                if seconds < 0.1:
                    continue
                clip = work / "paid-audio.mp3"
                run([self.ffmpeg, "-v", "error", "-y", "-ss", str(offset), "-i", str(reference),
                     "-t", str(seconds), "-ac", "1", "-ar", "16000", "-b:a", "64k", str(clip)])
                content = paid.transcribe(clip, seconds, base_language(audio_lang or lang),
                                          item["Path"] + fp + str(offset), translate=translate_audio)
                chunk = pysubs2.SSAFile.from_string(content, format_="srt")
                for cue in chunk:
                    cue.start += offset * 1000
                    cue.end += offset * 1000
                    cues.append(cue)
            validate(cues, duration)
            output = work / "paid-transcription.srt"
            cues.save(str(output), format_="srt")
            return self.publish(item, fp, lang, output, {"source": "paid_transcription", "source_id": "paid:whisper-1:" + lang},
                                reference, audio, duration, work)
        except (PaidUnavailable, Review) as error:
            return {**current, "paid_failure": str(error), "paid_diagnostic": error.diagnostic, "generation_attempted": True}

    def cleanup_sidecars(self, path, fp, complete):
        outputs = {canonical(path, wanted) for wanted in self.languages}
        for sidecar in sidecars(path):
            lang = sidecar["language"]
            permitted = any(matches(lang, wanted) for wanted in self.languages)
            uncertain = not lang or any(ambiguous(lang, wanted) for wanted in self.languages)
            unwanted = not permitted and (complete or not uncertain)
            duplicate = complete and permitted and not sidecar["forced"] and sidecar["path"] not in outputs
            if unwanted or duplicate:
                if fingerprint(path) != fp:
                    raise Review("media_changed_during_work")
                self.archive(sidecar["path"], path)

    def backup_link(self, path, fp, backup):
        try:
            os.link(path, backup)
        except FileExistsError:
            if fingerprint(backup) != fp:
                raise Review("media_backup_collision")
        except OSError as error:
            if error.errno != errno.EXDEV:
                raise
            # MergerFS can reject a cross-directory link when the destination
            # directory lives on another branch. Create it on the source branch
            # without changing the pool's global create/link policy.
            try:
                base = Path(os.getxattr(path, "user.mergerfs.basepath").decode())
                source = Path(os.getxattr(path, "user.mergerfs.fullpath").decode())
                relative = Path(os.getxattr(backup.parent, "user.mergerfs.relpath").decode().lstrip("/"))
                if not source.is_relative_to(base) or ".." in relative.parts or not base.is_dir():
                    raise Review("media_backup_branch_unavailable")
                target = base / relative / backup.name
                target.parent.mkdir(parents=True, exist_ok=True)
                try:
                    os.link(source, target)
                except FileExistsError:
                    pass
                source_stat, pool_stat = source.stat(), Path(path).stat()
                if not os.path.samefile(source, target) or (source_stat.st_size, source_stat.st_mtime_ns) != (pool_stat.st_size, pool_stat.st_mtime_ns):
                    raise Review("media_backup_collision")
                if fingerprint(path) != fp:
                    raise Review("media_changed_during_backup")
                return target
            except OSError:
                raise Review("media_backup_branch_unavailable") from None
        if fingerprint(backup) != fp or fingerprint(path) != fp:
            raise Review("media_changed_during_backup")
        return backup

    def prune(self, item, fp, streams, duration, complete):
        path = item["Path"]
        if not self.cfg.get("prune_embedded"):
            return fp, {"status": "disabled"}
        remove = []
        for stream in streams:
            # Audio is a read-only reference for subtitle verification. Legacy
            # prune_audio settings cannot enable audio deletion.
            if stream["codec_type"] != "subtitle":
                continue
            tags = stream.get("tags", {})
            lang = language(tags.get("language"), tags.get("title"))
            permitted = any(matches(lang, wanted) for wanted in self.languages)
            uncertain = not lang or any(ambiguous(lang, wanted) for wanted in self.languages)
            unwanted = not permitted and (complete or not uncertain)
            duplicate = complete and permitted and not stream.get("disposition", {}).get("forced")
            if unwanted or duplicate:
                remove.append(stream["index"])
        if not remove:
            return fp, {"status": "no_change"}
        if Path(path).suffix.lower() != ".mkv":
            return fp, {"status": "review_non_mkv", "remove_streams": remove}
        physical = backing_path(path)
        free = shutil.disk_usage(physical.parent).free
        required = Path(path).stat().st_size + max(10, self.cfg.get("min_free_gb", 50)) * 1024 ** 3
        if free < required:
            return fp, {"status": "insufficient_space"}
        backupdir = self.quarantine / "media" / hashlib.sha256((path + fp).encode()).hexdigest()[:20]
        backupdir.mkdir(parents=True, exist_ok=True)
        backup = backupdir / Path(path).name
        staged = physical.parent / (".subtitler-" + uuid.uuid4().hex + ".part")
        try:
            if fingerprint(path) != fp:
                raise Review("media_changed_during_work")
            backup = self.backup_link(path, fp, backup)
            mkvmerge = shutil.which("mkvmerge")
            if mkvmerge:
                inventory = json.loads(run([mkvmerge, "-J", path]))
                tracks = [t for t in inventory["tracks"] if t["type"] == "subtitles"]
                subtitles = [s for s in streams if s["codec_type"] == "subtitle"]
                if len(tracks) != len(subtitles):
                    raise Review("remux_track_mapping_inconclusive")
                ids = [str(t["id"]) for t, s in zip(tracks, subtitles) if s["index"] in remove]
                selection = ["--no-subtitles"] if len(ids) == len(tracks) else ["--subtitle-tracks", "!" + ",".join(ids)]
                run([mkvmerge, "-o", str(staged), *selection, path], accepted_codes=(0, 1))
            else:
                command = [self.ffmpeg, "-v", "error", "-y", "-i", path, "-map", "0"]
                for index in remove:
                    command += ["-map", f"-0:{index}"]
                command += ["-map_metadata", "0", "-map_chapters", "0", "-c", "copy", "-f", "matroska", str(staged)]
                run(command)
            after, new_duration = self.probe(str(staged))
            expected = Counter(stream_signature(s) for s in streams if s["index"] not in remove)
            actual = Counter(stream_signature(s) for s in after)
            if expected != actual:
                raise Review("remux_validation_failed", {"cause": "stream_metadata_changed"})
            if abs(new_duration - duration) > 1:
                raise Review("remux_validation_failed", {"cause": "media_duration_changed", "difference_seconds": round(new_duration - duration, 3)})
            if fingerprint(path) != fp:
                raise Review("media_changed_during_work")
            os.chmod(staged, Path(path).stat().st_mode & 0o777)
            with staged.open("rb") as handle:
                os.fsync(handle.fileno())
            if item.get("Source") in {"radarr", "sonarr"}:
                self.catalogue.db.execute("INSERT OR REPLACE INTO rescans VALUES(?,?)", (path, json.dumps(item)))
                self.catalogue.db.commit()
            os.replace(staged, physical)
            new_fp = fingerprint(path)
            self.catalogue.db.execute("UPDATE captions SET fingerprint=? WHERE path=? AND fingerprint=?", (new_fp, path, fp))
            self.catalogue.db.execute("UPDATE rejected SET fingerprint=? WHERE path=? AND fingerprint=?", (new_fp, path, fp))
            self.catalogue.db.execute("UPDATE candidate_reviews SET fingerprint=? WHERE path=? AND fingerprint=?", (new_fp, path, fp))
            self.catalogue.db.commit()
            return new_fp, {"status": "remuxed", "removed_streams": remove, "backup": str(backup)}
        finally:
            if staged.exists():
                staged.unlink()

    def process_rescans(self):
        import requests
        for path, raw in self.catalogue.db.execute("SELECT path,item FROM rescans LIMIT 10").fetchall():
            item = json.loads(raw)
            source = item["Source"]
            service = self.request.get("arr", {}).get(source, {})
            if not service.get("url") or not service.get("api_key"):
                continue
            command = {"name": "RescanMovie", "movieId": item["ID"]} if source == "radarr" else {"name": "RescanSeries", "seriesId": item.get("SeriesID")}
            if source == "sonarr" and not command["seriesId"]:
                continue
            try:
                response = requests.post(service["url"].rstrip("/") + "/api/v3/command", json=command,
                                         headers={"X-Api-Key": service["api_key"]}, timeout=30)
                if response.status_code not in {200, 201, 202}:
                    continue
                self.catalogue.db.execute("DELETE FROM rescans WHERE path=?", (path,))
                self.catalogue.db.commit()
            except Exception:
                continue

    def scan(self):
        with (self.catalogue.root / "scan.lock").open("a") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return self.emit(status="scan_already_running")
            self.scan_locked()
        if self.request.get("action", "scan") == "scan" and not self.request.get("dry_run"):
            self.transcription()

    def scan_locked(self):
        db = self.catalogue.db
        if not self.request.get("dry_run"):
            self.process_rescans()
            policy = json.dumps(self.languages)
            previous = db.execute("SELECT value FROM settings WHERE key='required_languages'").fetchone()
            if not previous or previous[0] != policy:
                # Keep provenance, but immediately reconsider previously checked
                # files when the configured target set/order changes.
                db.execute("UPDATE media SET checked=-1,next_try=0,status='pending' WHERE checked<>0")
                db.execute("DELETE FROM work_queue WHERE state<>'running'")
                db.execute("INSERT OR REPLACE INTO settings VALUES('required_languages',?)", (policy,))
                db.commit()
            revision = db.execute("SELECT value FROM settings WHERE key='recovery_revision'").fetchone()
            if not revision or revision[0] != "1":
                # Upgrade failures once without discarding their accepted tracks,
                # provider history, feedback or paid translation cache.
                db.execute("UPDATE media SET checked=-1,next_try=0 WHERE status IN ('needs_attention','managed_cleanup_pending')")
                db.execute("INSERT OR REPLACE INTO settings VALUES('recovery_revision','1')")
                db.commit()
            budget = json.dumps(self.cfg.get('paid_daily_usd', 0))
            old_budget = db.execute("SELECT value FROM settings WHERE key='paid_daily_usd'").fetchone()
            if not old_budget or old_budget[0] != budget:
                # Re-check jobs deferred under an earlier budget without
                # resetting a single reservation or cached translation chunk.
                for path, raw in db.execute("SELECT path,detail FROM media WHERE status='needs_attention'").fetchall():
                    if json.loads(raw).get('recovery', {}).get('action') == 'wait_for_daily_budget':
                        db.execute("UPDATE media SET checked=-1,next_try=0 WHERE path=?", (path,))
                        db.execute("UPDATE work_queue SET available=0 WHERE path=? AND kind='transcription' AND state='queued'", (path,))
                db.execute("INSERT OR REPLACE INTO settings VALUES('paid_daily_usd',?)", (budget,))
                db.commit()
        paths = set()
        for index, item in enumerate(self.request.get("items") or []):
            # Large inventories must not hold the SQLite writer lock while
            # another queue is publishing or reserving its paid allowance.
            if index % 100 == 0:
                db.commit()
            path = item["Path"]
            paths.add(path)
            try:
                if Path(path).is_symlink():
                    continue
                fp = fingerprint(path)
            except OSError:
                continue
            row = db.execute("SELECT fingerprint FROM media WHERE path=?", (path,)).fetchone()
            if not row or row[0] != fp:
                db.execute("DELETE FROM work_queue WHERE path=? AND state<>'running'", (path,))
                db.execute("INSERT OR REPLACE INTO media(path,fingerprint,item) VALUES(?,?,?)", (path, fp, json.dumps(item)))
            else:
                db.execute("UPDATE media SET item=? WHERE path=?", (json.dumps(item), path))
                # Lost/edited canonical output immediately re-enters the queue.
                for lang, sha, data in db.execute("SELECT language,sha,data FROM captions WHERE path=? AND fingerprint=?", (path, fp)):
                    if lang not in self.languages:
                        continue
                    output = json.loads(data)["output"]
                    if not Path(output).exists() or digest(output) != sha:
                        db.execute("UPDATE media SET next_try=0,checked=0 WHERE path=?", (path,))
        db.commit()
        # Only inventory paths remain eligible for new jobs; finish already-running work safely.
        for queued_path, in db.execute("SELECT DISTINCT path FROM work_queue WHERE state<>'running'").fetchall():
            if queued_path not in paths:
                db.execute("DELETE FROM work_queue WHERE path=? AND state<>'running'", (queued_path,))
        db.commit()
        jobs = 0
        rows = db.execute("SELECT path,fingerprint,item,detail,status,checked FROM media WHERE next_try<=? "
                          "ORDER BY EXISTS(SELECT 1 FROM feedback WHERE feedback.path=media.path AND applied=0) DESC,checked,path",
                          (time.time(),)).fetchall()
        rows = [r for r in rows if r[0] in paths]
        # Reserve one slot for a due retry, even while thousands of untouched
        # files remain. Explicit feedback and upgrade retries keep priority.
        retry = next((r for r in rows if r[4] in {"needs_attention", "managed_cleanup_pending"} and r[5] > 0), None)
        if retry and not any(r[5] < 0 for r in rows):
            index = next((i for i, r in enumerate(rows) if not db.execute("SELECT 1 FROM feedback WHERE path=? AND applied=0", (r[0],)).fetchone()), len(rows))
            rows.remove(retry)
            rows.insert(min(index, len(rows)), retry)
        for path, fp, raw, previous_detail, previous_status, checked in rows:
            if self.request.get("dry_run"):
                if self.request.get("max_jobs", 0) > 0 and jobs >= self.request["max_jobs"]:
                    break
                self.emit(path=path, status="planned", languages=self.languages, paid_calls=0)
                jobs += 1
                continue
            try:
                if json.loads(raw).get("Source") != "manual" and time.time() - Path(path).stat().st_mtime < 300:
                    continue
            except FileNotFoundError:
                continue
            feedback = db.execute("SELECT 1 FROM feedback WHERE path=? AND applied=0", (path,)).fetchone()
            waiting = db.execute("SELECT 1 FROM work_queue WHERE path=? AND kind='transcription' AND state IN ('queued','running')", (path,)).fetchone()
            if waiting and not feedback:
                continue
            if feedback:
                db.execute("DELETE FROM work_queue WHERE path=? AND kind='transcription' AND state<>'running'", (path,))
            enqueue(db, path, fp, "files", priority=2 if feedback else (1 if retry and path == retry[0] else 0))
        db.commit()
        if not self.request.get("dry_run"):
            drain(self, "files")
            self.process_rescans()

    def transcription(self):
        with (self.catalogue.root / "transcription.lock").open("a") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return self.emit(status="transcription_already_running")
            if not self.request.get("dry_run"):
                drain(self, "transcription")

    def process_job(self, path, expected_fp, kind):
        locks = self.catalogue.root / "jobs"
        locks.mkdir(exist_ok=True)
        with (locks / (hashlib.sha256(path.encode()).hexdigest() + ".lock")).open("a") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                self.catalogue.db.execute("UPDATE work_queue SET state='queued',available=? WHERE path=? AND kind=?",
                                          (time.time() + 60, path, kind))
                self.catalogue.db.commit()
                return
            return self.process_job_locked(path, expected_fp, kind)

    def process_job_locked(self, path, expected_fp, kind):
        db = self.catalogue.db
        row = db.execute("SELECT path,fingerprint,item,detail,status,checked FROM media WHERE path=?", (path,)).fetchone()
        if not row or not Path(path).exists() or row[1] != expected_fp or fingerprint(path) != expected_fp:
            self.emit(path=path, status="stale_job_skipped", queue=kind)
            return
        path, fp, raw, previous_detail, previous_status, checked = row
        item = json.loads(raw)
        searched_at = json.loads(previous_detail).get('sources_checked_at', checked)
        if kind == "transcription" and (checked <= 0 or time.time() - searched_at > 86400 or
                db.execute("SELECT 1 FROM feedback WHERE path=? AND applied=0", (path,)).fetchone()):
            enqueue(db, path, fp, "files", priority=2)
            db.commit()
            return
        detail = {} if kind != "transcription" else {k:v for k,v in json.loads(previous_detail).items() if k in self.languages}
        if kind == 'transcription':
            detail['sources_checked_at'] = searched_at
        status, new_fp = "needs_attention", fp
        generation = False
        stage = "probe"
        try:
            self.feedback(path, fp)
            streams, duration = self.probe(path)
            with tempfile.TemporaryDirectory(prefix="ownership-", dir=self.request["temp_dir"]) as directory:
                work = Path(directory)
                for lang in self.languages:
                    if kind == "transcription" and "output" not in detail.get(lang, {}):
                        continue
                    stage = "acquire:" + lang
                    try:
                        detail[lang] = self.ensure(item, fp, streams, duration, lang, work)
                    except Exception as error:
                        detail[lang] = {"status": "missing_or_unverified", "error": str(error) if isinstance(error, Review) else "local_engine_failed",
                                        "diagnostic": diagnostic(error, stage)}
                # All configured searches finish before generation. Prefer
                # languages supplied by existing dialogue for free local
                # transcription; keep configured order within each group.
                if kind == 'files':
                    detail['sources_checked_at'] = time.time()
                def order(lang):
                    audio = self.audio(streams, lang, language(item.get("OriginalLanguage")))
                    tags = audio.get("tags", {})
                    return 0 if matches(language(tags.get("language"), tags.get("title")), lang) else 1
                for lang in sorted(self.languages, key=order) if kind != "files" else []:
                    stage = "generate:" + lang
                    try:
                        if "output" not in detail.get(lang, {}) and not self.generation_blocker(lang, detail.get(lang, {})):
                            audio = self.audio(streams, lang, language(item.get("OriginalLanguage")))
                            reference = work / f"audio-{audio['index']}.wav"
                            if not reference.exists():
                                run([self.ffmpeg, "-v", "error", "-y", "-threads",
                                     str(self.cfg.get("queues", {}).get("worker_threads", 2)),
                                     "-i", path, "-map", f"0:{audio['index']}", "-vn", "-ac", "1", "-ar", "16000", str(reference)])
                        detail[lang] = self.fallback(item, fp, streams, duration, lang, detail, work)
                    except Exception as error:
                        detail[lang].update(error=str(error) if isinstance(error, Review) else "local_engine_failed", diagnostic=diagnostic(error, stage))
                complete = all("output" in detail.get(lang, {}) for lang in self.languages)
                generation = self.needs_generation(detail)
                stage = "sidecar_cleanup"
                self.cleanup_sidecars(path, fp, complete)
                stage = "embedded_cleanup"
                try:
                    new_fp, pruning = self.prune(item, fp, streams, duration, complete)
                except Exception as error:
                    pruning = {"status": "retry_pending", "reason": str(error) if isinstance(error, Review) else "local_engine_failed",
                               "diagnostic": diagnostic(error, stage)}
                detail["track_policy"] = pruning
                cleaned = pruning["status"] in {"disabled", "no_change", "remuxed"}
                status = ("managed" if cleaned else "managed_cleanup_pending") if complete else (
                    "queued_transcription" if kind == "files" and generation else "needs_attention")
        except Review as error:
            detail["error"] = str(error)
            detail["diagnostic"] = diagnostic(error, stage)
        except Exception as error:
            detail["error"] = "local_engine_failed"
            detail["diagnostic"] = diagnostic(error, stage)
        # Never persist a stale success across a concurrent Radarr import.
        if Path(path).exists() and fingerprint(path) == new_fp:
            pending = db.execute("SELECT 1 FROM feedback WHERE path=? AND applied=0", (path,)).fetchone()
            attempt = 0 if status in {"managed", "queued_transcription"} else json.loads(previous_detail).get("recovery", {}).get("attempt", 0) + 1
            maximum = max(3600, self.request.get("retry_seconds", 86400))
            delay = 0 if pending or status == "queued_transcription" else (7 * 86400 if status == "managed" else min(maximum, 3600 * 2 ** min(attempt - 1, 8)))
            action = recovery_action(detail, status)
            if not pending and action == "wait_for_daily_budget":
                delay = max(delay, 86400 - time.time() % 86400 + 60)
            if not pending and action in {"provider_configuration_required", "translation_requires_review"}:
                delay = max(delay, 86400)
            detail["recovery"] = {"attempt": attempt, "retry_in_seconds": delay, "action": action}
            db.execute("UPDATE media SET fingerprint=?,checked=?,next_try=?,status=?,detail=? WHERE path=?",
                       (new_fp, time.time(), time.time() + delay, status, json.dumps(detail), path))
            if status == "queued_transcription" or (kind == "transcription" and generation and status == "needs_attention"):
                if kind == "transcription":
                    # Reschedule this claim directly; coordinator completion must not overwrite it.
                    db.execute("UPDATE work_queue SET state='queued',fingerprint=?,available=? WHERE path=? AND kind='transcription'",
                               (new_fp, time.time() + delay, path))
                else:
                    enqueue(db, path, new_fp, "transcription")
            db.commit()
        self.emit(path=path, status=status, detail=detail)
        self.process_rescans()

    def status(self):
        db = self.catalogue.db
        counts = dict(db.execute("SELECT status,count(*) FROM media GROUP BY status"))
        details = [{"path": p, "status": s, "detail": json.loads(d), "next_try": n}
                   for p, s, d, n in db.execute("SELECT path,status,detail,next_try FROM media ORDER BY checked DESC")]
        spent = 0
        if db.execute("SELECT 1 FROM sqlite_master WHERE name='paid_ledger'").fetchone():
            spent = db.execute("SELECT coalesce(sum(reserved),0) FROM paid_ledger WHERE day=?", (time.strftime("%Y-%m-%d", time.gmtime()),)).fetchone()[0]
        queues = {kind: dict(db.execute("SELECT state,count(*) FROM work_queue WHERE kind=? GROUP BY state", (kind,)))
                  for kind in ["files", "transcription"]}
        schedulers = {k:json.loads(v) for k,v in db.execute("SELECT key,value FROM settings WHERE key LIKE 'scheduler_%'")}
        self.emit(required_languages=self.languages, counts=counts, queues=queues, schedulers=schedulers, pending_feedback=db.execute("SELECT count(*) FROM feedback WHERE applied=0").fetchone()[0],
                  pending_rescans=db.execute("SELECT count(*) FROM rescans").fetchone()[0],
                  media=details, paid_reserved_usd_today=round(spent, 4), paid_daily_usd=self.cfg.get('paid_daily_usd', 0))


def main():
    os.umask(0o077)
    # Unwind temporary-file and remux cleanup when the Go daemon cancels this
    # process group. A stopped job remains queued instead of becoming a failure.
    def terminate(signum, frame):
        raise SystemExit(0)
    signal.signal(signal.SIGTERM, terminate)
    request = json.load(sys.stdin)
    manager = Manager(request)
    action = request["action"]
    if action in {"scan", "files", "transcription"}:
        os.nice(max(0, 10 - os.nice(0)))
        Path(request["temp_dir"]).mkdir(parents=True, exist_ok=True)
        if action == "transcription":
            manager.transcription()
        else:
            manager.scan()
    elif action == "status":
        manager.status()
    elif action == "report":
        feedback = request["feedback"]
        try:
            feedback["language"] = normalize(feedback["language"])
        except ValueError:
            raise Review("invalid_feedback") from None
        if feedback["language"] not in manager.languages:
            raise Review("feedback_language_not_configured")
        manager.emit(**manager.catalogue.report(feedback["path"], feedback["language"], feedback["reason"]))
    else:
        raise Review("unknown_action")


if __name__ == "__main__":
    try:
        main()
    except Review as error:
        print(json.dumps({"error": str(error)}), flush=True)
        sys.exit(1)
    except Exception:
        print(json.dumps({"error": "ownership_engine_failed"}), flush=True)
        sys.exit(1)
