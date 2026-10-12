import contextlib
import errno
import io
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import pysubs2
from babelfish import Language
from manager import Catalogue, Manager, Review, canonical, digest, fingerprint, language, validate, word_metrics, text_alignment
from paid import Paid, PaidUnavailable
from providers import accepted_language, identity, safe_yify_url


def dialogue():
    cues = pysubs2.SSAFile()
    for i in range(30):
        cues.append(pysubs2.SSAEvent(start=i * 4000 + 1000, end=i * 4000 + 3500,
                                    text=f"Hello friend number {i}, we should visit the beautiful garden tomorrow."))
    return cues


class OwnershipTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.video = self.root / "Film (2024).mkv"
        self.video.write_bytes(b"media-version-one")
        os.utime(self.video, (1, 1))
        self.fp = fingerprint(self.video)
        self.item = {"Path": str(self.video), "Source": "radarr", "Title": "Film", "OriginalLanguage": "English"}
        self.request = {"management": {"state_dir": str(self.root / "state"), "quarantine_dir": str(self.root / "archive"),
                                        "verify_words": True, "paid_fallback": False},
                        "languages": ["en", "pt-PT"], "temp_dir": str(self.root), "items": [self.item], "max_jobs": 1}
        self.manager = Manager(self.request)
        self.addCleanup(self.manager.catalogue.db.close)

    def register(self):
        self.manager.catalogue.db.execute("INSERT INTO media(path,fingerprint,item) VALUES(?,?,?)",
                                          (str(self.video), self.fp, json.dumps(self.item)))
        self.manager.catalogue.db.commit()

    def caption(self, lang="en"):
        output = Path(canonical(self.video, lang))
        dialogue().save(str(output), format_="srt")
        data = {"output": str(output), "source_id": "provider:one", "input_sha": "original-input"}
        self.manager.catalogue.record(str(self.video), self.fp, lang, digest(output), data)
        return output

    def test_locale_is_exact(self):
        self.assertEqual(language("por"), "pt")
        self.assertEqual(language("por", "Português Portugal"), "pt-PT")
        self.assertEqual(language("por", "European"), "pt-PT")
        self.assertEqual(language("por", "Portuguese (Iberian)"), "pt-PT")
        self.assertEqual(language("dut"), "nl")
        self.assertEqual(language("pt-PT", "Brazilian"), "pt-BR")
        self.assertNotEqual(canonical(self.video, "pt-PT"), canonical(self.video, "en"))
        self.assertFalse(accepted_language(Language("por"), "pt-PT"))
        self.assertFalse(accepted_language(Language("por", "BR"), "pt-PT"))
        self.assertTrue(accepted_language(Language("por", "PT"), "pt-PT"))

    def test_structure_rejects_partial_invalid_and_wrong_cut(self):
        validate(dialogue(), 120)
        for modification in [lambda c: setattr(c[0], "end", -1), lambda c: setattr(c[-1], "end", 130000),
                             lambda c: setattr(c[2], "start", 0)]:
            cues = dialogue()
            modification(cues)
            with self.assertRaises(Review):
                validate(cues, 120)
        with self.assertRaises(Review):
            validate(dialogue(), 400)

    def test_words_check_offsets_separately_from_text(self):
        cues = dialogue()
        recognized = []
        for cue in cues[:10]:
            for word in cue.plaintext.split():
                recognized.append((word, cue.start / 1000 + 0.1, cue.end / 1000 - 0.1))
        self.assertEqual(word_metrics(cues, recognized, 0, 40)["status"], "verified")
        shifted = [(w, s + 8, e + 8) for w, s, e in recognized]
        self.assertEqual(word_metrics(cues, shifted, 0, 40)["status"], "mismatch")
        wrong = [("entirely unrelated sentence", 1, 2)] * 10
        self.assertEqual(word_metrics(cues, wrong, 0, 40)["status"], "inconclusive")

    def test_text_audio_anchors_repair_shift_and_drift(self):
        cues = dialogue()
        checks = {"samples": [{"text_match": .8, "matched_words": 30, "sample_midpoint": t,
                                "offset_seconds": 5 + t * .001} for t in [10, 60, 110]]}
        result = text_alignment(cues, checks)
        self.assertIsNotNone(result)
        self.assertEqual(cues[0].start, 6001)
        self.assertAlmostEqual(result["framerate_scale"], 1.001)
        checks["samples"][1]["offset_seconds"] += 15
        self.assertIsNone(text_alignment(dialogue(), checks))

    def test_feedback_rejects_both_original_and_aligned_hash(self):
        self.register()
        output = self.caption()
        sha = digest(output)
        self.manager.catalogue.report(str(self.video), "en", "wrong_text")
        self.manager.feedback(str(self.video), self.fp)
        self.assertFalse(output.exists())
        self.assertTrue(self.manager.catalogue.rejected(str(self.video), self.fp, "en", sha))
        self.assertTrue(self.manager.catalogue.rejected(str(self.video), self.fp, "en", "original-input"))
        self.assertTrue(self.manager.catalogue.rejected(str(self.video), self.fp, "en", source="provider:one"))
        self.assertEqual(len(list((self.root / "archive").rglob("*.srt"))), 1)

    def test_feedback_cannot_reject_a_new_media_version(self):
        self.register()
        output = self.caption()
        self.manager.catalogue.report(str(self.video), "en", "wrong_text")
        self.video.write_bytes(b"new-movie-import")
        self.manager.feedback(str(self.video), fingerprint(self.video))
        self.assertTrue(output.exists())
        with self.assertRaisesRegex(Review, "media_changed"):
            self.manager.catalogue.report(str(self.video), "en", "out_of_sync")

    def test_feedback_is_prioritized_over_the_initial_library_queue(self):
        self.register()
        earlier = self.root / "A.mkv"
        earlier.write_bytes(b"other movie")
        self.manager.request["items"].append({**self.item, "Path": str(earlier)})
        self.manager.request["dry_run"] = True
        self.manager.catalogue.report(str(self.video), "en", "missing")
        with patch.object(self.manager, "emit") as emit:
            self.manager.scan()
        self.assertEqual(emit.call_args_list[0].kwargs["path"], str(self.video))

    def test_same_content_is_not_generated_again_after_rejection(self):
        candidate = self.root / "bad.srt"
        dialogue().save(str(candidate), format_="srt")
        self.manager.catalogue.reject(str(self.video), self.fp, "en", digest(candidate), "", "wrong_text")
        with self.assertRaisesRegex(Review, "previously_rejected"):
            self.manager.publish(self.item, self.fp, "en", candidate, {"source": "sidecar"}, None, {}, 120, self.root)

    def test_existing_verified_output_skips_provider_and_transcription(self):
        self.caption()
        with patch.object(self.manager, "provider_candidates", side_effect=AssertionError("provider called")), \
             patch.object(self.manager, "speech_model", side_effect=AssertionError("transcription called")):
            result = self.manager.ensure(self.item, self.fp, [], 120, "en", self.root)
        self.assertIn("output", result)

    def test_provider_outage_never_triggers_generation(self):
        detail = {"en": {"providers": {"opensubtitlescom": "unavailable"}}}
        with patch.object(self.manager, "transcribe", side_effect=AssertionError("transcription called")):
            result = self.manager.fallback(self.item, self.fp, [], 120, "en", detail, self.root)
        self.assertEqual(result["generation_deferred"], "provider_search_incomplete")

    def test_untried_candidates_and_uncertain_timing_never_spend(self):
        for current, reason in [({"providers": {"opensubtitlescom": "candidates_pending"}}, "provider_search_incomplete"),
                                ({"providers": {"opensubtitlescom": "searched"}, "failures": ["word_verification_inconclusive"]},
                                 "existing_candidate_needs_review")]:
            with patch.object(self.manager, "transcribe", side_effect=AssertionError("transcription called")):
                result = self.manager.fallback(self.item, self.fp, [], 120, "en", {"en": current}, self.root)
            self.assertEqual(result["generation_deferred"], reason)

    def test_local_translation_normalizes_regional_audio_codes(self):
        segments = [SimpleNamespace(start=c.start / 1000, end=c.end / 1000, text=c.plaintext,
                                    no_speech_prob=.1, avg_logprob=-.1) for c in dialogue()]
        with patch.object(self.manager, "speech_model") as model:
            model.return_value.transcribe.return_value = (segments, None)
            self.manager.transcribe(self.root / "audio.wav", "en", self.root, 120, source_language="pt-BR", translate=True)
        self.assertEqual(model.return_value.transcribe.call_args.kwargs["language"], "pt")
        self.assertEqual(model.return_value.transcribe.call_args.kwargs["task"], "translate")

    def test_wrong_language_label_is_rejected_before_alignment(self):
        candidate = self.root / "mislabeled.srt"
        dialogue().save(str(candidate), format_="srt")
        with patch.object(self.manager, "sync", side_effect=AssertionError("alignment called")):
            with self.assertRaisesRegex(Review, "subtitle_language_mismatch"):
                self.manager.publish(self.item, self.fp, "pt-PT", candidate, {"source": "provider"}, None, {}, 120, self.root)

    def test_translation_inherits_verified_clock_and_rejects_a_changed_clock(self):
        source = self.caption()
        candidate = self.root / "translated.srt"
        cues = dialogue()
        for i, cue in enumerate(cues):
            cue.text = f"Olá meu amigo número {i}, amanhã vamos visitar o jardim com a nossa família."
        cues.save(str(candidate), format_="srt")
        provenance = {"source": "paid_translation", "derived_from_sha": digest(source), "derived_from_language": "en"}
        stream = {"index": 1, "tags": {"language": "eng"}}
        with patch.object(self.manager, "sync", side_effect=AssertionError("alignment called")), \
             patch.object(self.manager, "verify_words", side_effect=AssertionError("literal translation matching called")):
            result = self.manager.publish(self.item, self.fp, "pt-PT", candidate, provenance, None, stream, 120, self.root)
        self.assertEqual(result["timing"]["method"], "inherited_accepted_source_timestamps")
        cues[1].end += 1000
        cues.save(str(candidate), format_="srt")
        with self.assertRaisesRegex(Review, "translation_changed_verified_timestamps"):
            self.manager.publish(self.item, self.fp, "pt-PT", candidate, provenance, None, stream, 120, self.root)
        source.write_text("changed source")
        with self.assertRaisesRegex(Review, "translation_source_changed"):
            self.manager.publish(self.item, self.fp, "pt-PT", candidate, provenance, None, stream, 120, self.root)

    def test_same_language_translation_must_match_the_actual_dub(self):
        source = self.caption()
        candidate = self.root / "translated.srt"
        cues = dialogue()
        for i, cue in enumerate(cues):
            cue.text = f"Olá meu amigo número {i}, amanhã vamos visitar o jardim com a nossa família."
        cues.save(str(candidate), format_="srt")
        provenance = {"source": "paid_translation", "derived_from_sha": digest(source), "derived_from_language": "en"}
        stream = {"index": 2, "tags": {"language": "por", "title": "European"}}
        with patch.object(self.manager, "verify_words", return_value={"status": "mismatch"}) as verify:
            with self.assertRaisesRegex(Review, "words_do_not_match_audio_timing"):
                self.manager.publish(self.item, self.fp, "pt-PT", candidate, provenance, None, stream, 120, self.root)
        verify.assert_called_once()

    def test_free_target_audio_generation_precedes_paid_translation(self):
        self.manager.cfg.update(local_transcription=True, paid_fallback=True, paid_daily_usd=1)
        audio = {"codec_type": "audio", "index": 1, "tags": {"language": "eng"}}
        detail = {"en": {"providers": {"opensubtitlescom": "searched"}}, "pt-PT": {"output": "source.srt"}}
        with patch("paid.Paid", side_effect=AssertionError("paid called")), \
             patch.object(self.manager, "transcribe", return_value=(self.root / "generated.srt", {"source": "local_transcription"})) as transcribe, \
             patch.object(self.manager, "publish", return_value={"output": "local.srt"}):
            result = self.manager.fallback(self.item, self.fp, [audio], 120, "en", detail, self.root)
        transcribe.assert_called_once()
        self.assertEqual(result["output"], "local.srt")

    def test_portugal_audio_can_supply_free_english_translation(self):
        self.manager.cfg.update(local_transcription=True, paid_fallback=True, paid_daily_usd=1)
        audio = {"codec_type": "audio", "index": 1, "tags": {"language": "por", "title": "European"}}
        detail = {"en": {"providers": {"opensubtitlescom": "searched"}}, "pt-PT": {"output": "source.srt"}}
        with patch("paid.Paid", side_effect=AssertionError("paid called")), \
             patch.object(self.manager, "transcribe", return_value=(self.root / "generated.srt", {"source": "local_transcription"})) as transcribe, \
             patch.object(self.manager, "publish", return_value={"output": "local.srt"}):
            self.manager.fallback(self.item, self.fp, [audio], 120, "en", detail, self.root)
        self.assertTrue(transcribe.call_args.kwargs["translate"])
        self.assertEqual(transcribe.call_args.kwargs["source_language"], "pt-PT")

    def test_reporting_source_invalidates_its_translation(self):
        self.register()
        source = self.caption()
        translated = self.caption("pt-PT")
        data = self.manager.catalogue.caption(str(self.video), self.fp, "pt-PT")[1]
        data["derived_from_sha"] = digest(source)
        self.manager.catalogue.record(str(self.video), self.fp, "pt-PT", digest(translated), data)
        self.manager.catalogue.report(str(self.video), "en", "wrong_text")
        self.manager.feedback(str(self.video), self.fp)
        self.assertFalse(translated.exists())
        self.assertIsNone(self.manager.catalogue.caption(str(self.video), self.fp, "pt-PT"))

    def test_existing_then_provider_order(self):
        events = []
        candidate = self.root / "candidate.srt"
        dialogue().save(str(candidate), format_="srt")
        def local(*args):
            events.append("existing")
            yield candidate, {"source": "sidecar"}
        def provider(*args):
            events.append("provider")
            yield candidate, {"source": "provider"}
        def publish(*args):
            source = args[4]["source"]
            events.append("verify_" + source)
            if source == "sidecar":
                raise Review("timing_alignment_inconclusive")
            return {"output": "good.srt"}
        stream = {"codec_type": "audio", "index": 1, "tags": {"language": "eng"}}
        (self.root / "audio-1.wav").write_bytes(b"stub")
        with patch.object(self.manager, "local_candidates", local), patch.object(self.manager, "provider_candidates", provider), \
             patch.object(self.manager, "publish", publish):
            result = self.manager.ensure(self.item, self.fp, [stream], 120, "en", self.root)
        self.assertIn("output", result)
        self.assertEqual(events, ["existing", "verify_sidecar", "provider", "verify_provider"])

    def test_publication_is_fenced_against_new_import(self):
        candidate = self.root / "candidate.srt"
        dialogue().save(str(candidate), format_="srt")
        stream = {"index": 1, "tags": {"language": "eng"}}
        def sync(ref, candidate, output):
            shutil.copy2(candidate, output)
            return {"content_proof": False}
        def verify(*args):
            self.video.write_bytes(b"replaced by Radarr")
            return {"status": "verified_samples"}
        with patch.object(self.manager, "sync", sync), patch.object(self.manager, "verify_words", verify):
            with self.assertRaisesRegex(Review, "media_changed"):
                self.manager.publish(self.item, self.fp, "en", candidate, {"source": "sidecar"}, None, stream, 120, self.root)
        self.assertFalse(Path(canonical(self.video, "en")).exists())

    def test_unwanted_forced_subtitle_is_removed_but_original_seed_survives(self):
        bad = self.video.with_suffix(".tr.forced.srt")
        bad.write_bytes(b"Turkish captions")
        seed = self.root / "seed.srt"
        os.link(bad, seed)
        self.manager.cleanup_sidecars(str(self.video), self.fp, False)
        self.assertFalse(bad.exists())
        self.assertEqual(seed.read_bytes(), b"Turkish captions")

    def test_mergerfs_backup_uses_source_branch_and_rejects_collisions(self):
        base = self.root / "branch"
        base.mkdir()
        source = base / self.video.name
        os.link(self.video, source)
        destination = self.root / "archive" / "media" / "old.mkv"
        destination.parent.mkdir(parents=True)
        original_link = os.link
        def link(src, dst):
            if str(src) == str(self.video):
                raise OSError(errno.EXDEV, "simulated branch mismatch")
            return original_link(src, dst)
        attributes = {"user.mergerfs.basepath": str(base).encode(), "user.mergerfs.fullpath": str(source).encode(),
                      "user.mergerfs.relpath": b"/archive/media"}
        with patch("manager.os.link", link), patch("manager.os.getxattr", lambda path, attr: attributes[attr]):
            backup = self.manager.backup_link(str(self.video), self.fp, destination)
            self.assertTrue(os.path.samefile(source, backup))
            backup.unlink()
            backup.write_bytes(b"different file")
            with self.assertRaisesRegex(Review, "backup_collision"):
                self.manager.backup_link(str(self.video), self.fp, destination)

    @unittest.skipUnless(shutil.which("ffmpeg"), "ffmpeg required")
    def test_legacy_audio_cleanup_setting_cannot_remove_any_audio(self):
        subprocess.run(["ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=2",
                        "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-t", "2", "-map", "0:v", "-map", "1:a", "-map", "1:a",
                        "-c:v", "ffv1", "-c:a", "pcm_s16le", "-metadata:s:a:0", "language=eng", "-metadata:s:a:0", "title=Commentary",
                        "-metadata:s:a:1", "language=tur", str(self.video)], check=True)
        fp, original_sha = fingerprint(self.video), digest(self.video)
        self.manager.cfg.update(prune_audio=True, prune_embedded=True)
        streams, duration = self.manager.probe(str(self.video))
        new_fp, status = self.manager.prune(self.item, fp, streams, duration, False)
        self.assertEqual(new_fp, fp)
        self.assertEqual(digest(self.video), original_sha)
        self.assertEqual(status["status"], "no_change")
        self.manager.cfg.update(prune_embedded=False)
        new_fp, status = self.manager.prune(self.item, fp, streams, duration, False)
        self.assertEqual(status["status"], "disabled")
        self.assertEqual(new_fp, fp)

    @unittest.skipUnless(shutil.which("ffmpeg"), "ffmpeg required")
    def test_subtitle_remux_preserves_every_audio_track_and_seed_inode(self):
        captions = self.root / "turkish.srt"
        captions.write_text("1\n00:00:00,000 --> 00:00:01,500\nTurkish caption\n")
        subprocess.run(["ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=2",
                        "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-i", str(captions), "-t", "2",
                        "-map", "0:v", "-map", "1:a", "-map", "1:a", "-map", "1:a", "-map", "1:a", "-map", "2:s",
                        "-c:v", "ffv1", "-c:a", "pcm_s16le", "-c:s", "srt",
                        "-metadata:s:a:0", "language=eng", "-metadata:s:a:0", "title=Commentary",
                        "-metadata:s:a:1", "language=tur", "-metadata:s:a:2", "language=por", "-metadata:s:a:2", "title=Brazilian Portuguese",
                        "-metadata:s:a:3", "language=und", "-metadata:s:s:0", "language=tur", str(self.video)], check=True)
        fp = fingerprint(self.video)
        seed = self.root / "seed.mkv"
        os.link(self.video, seed)
        original_sha = digest(seed)
        self.manager.cfg.update(prune_audio=True, prune_embedded=True)
        streams, duration = self.manager.probe(str(self.video))
        def audio_packets(path):
            return [subprocess.check_output(["ffmpeg", "-v", "error", "-i", str(path), "-map", f"0:a:{index}",
                                             "-c", "copy", "-f", "hash", "-hash", "sha256", "-"])
                    for index in range(4)]
        before_packets = audio_packets(self.video)
        new_fp, status = self.manager.prune(self.item, fp, streams, duration, False)
        self.assertEqual(status["status"], "remuxed")
        self.assertNotEqual(new_fp, fp)
        self.assertEqual(digest(seed), original_sha)
        self.assertEqual(digest(status["backup"]), original_sha)
        after, _ = self.manager.probe(str(self.video))
        audios = lambda ss: [(s["codec_name"], s.get("tags", {}).get("language"), s.get("tags", {}).get("title"), s.get("disposition"))
                             for s in ss if s["codec_type"] == "audio"]
        self.assertEqual(audios(after), audios(streams))
        self.assertEqual(audio_packets(self.video), before_packets)
        self.assertEqual(status["removed_streams"], [s["index"] for s in streams if s["codec_type"] == "subtitle"])
        self.assertFalse(any(s["codec_type"] == "subtitle" for s in after))


class BudgetAndProviderTests(unittest.TestCase):
    def test_shutdown_unwinds_temporary_work(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker = root / "working-directory"
            request = {"action": "scan", "temp_dir": directory,
                       "management": {"state_dir": str(root / "state"), "quarantine_dir": str(root / "archive")}}
            script = """import tempfile,time,sys
from pathlib import Path
from manager import Manager,main
def scan(self):
    with tempfile.TemporaryDirectory(dir=self.request['temp_dir']) as work:
        Path(sys.argv[1]).write_text(work)
        print('ready',flush=True)
        time.sleep(30)
Manager.scan=scan
main()
"""
            process = subprocess.Popen([sys.executable, "-c", script, str(marker)], stdin=subprocess.PIPE,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                       env={**os.environ, "PYTHONPATH": str(Path(__file__).parent)})
            try:
                process.stdin.write(json.dumps(request).encode())
                process.stdin.flush()
                process.stdin.close()
                process.stdin = None
                import select
                self.assertTrue(select.select([process.stdout], [], [], 5)[0])
                self.assertEqual(process.stdout.readline().strip(), b"ready")
                work = Path(marker.read_text())
                process.terminate()
                process.communicate(timeout=5)
                self.assertEqual(process.returncode, 0)
                self.assertFalse(work.exists())
            finally:
                if process.poll() is None:
                    process.kill()
                process.communicate()

    def test_translation_validates_ids_and_resumes_without_another_charge(self):
        with tempfile.TemporaryDirectory() as directory:
            db = sqlite3.connect(Path(directory) / "db")
            paid = Paid(db, {"api_key": "unit-test-placeholder", "translation_model": "gpt-4o-mini"}, 1)
            cues = dialogue()[:2]
            response = {"output": [{"content": [{"type": "output_text", "text": json.dumps({"translations": [
                {"id": i, "text": "Português europeu"} for i in range(2)]})}]}]}
            with patch.object(paid, "post") as post:
                post.return_value.json.return_value = response
                self.assertEqual(paid.translate(cues, "pt-PT", ""), ["Português europeu"] * 2)
                request = post.call_args.kwargs["json"]
                format = request["text"]["format"]
                self.assertEqual(format["type"], "json_schema")
                self.assertTrue(format["strict"])
                self.assertFalse(format["schema"]["additionalProperties"])
                cue_schema = format["schema"]["properties"]["translations"]["items"]
                self.assertEqual(cue_schema["required"], ["id", "text"])
                self.assertFalse(cue_schema["additionalProperties"])
                reserved = db.execute("SELECT reserved FROM paid_ledger").fetchone()[0]
                expected = (len(request["input"].encode()) + len(json.dumps(format).encode()) + 2000) * 0.15 / 1_000_000 + 6000 * 0.60 / 1_000_000
                self.assertAlmostEqual(reserved, expected)
                paid.translate(cues, "pt-PT", "")
                self.assertEqual(post.call_count, 1)
                self.assertEqual(db.execute("SELECT count(*) FROM paid_ledger").fetchone()[0], 1)
                response["output"][0]["content"][0]["text"] = '{"translations":[{"id":0,"text":"a"},{"id":0,"text":"b"}]}'
                with self.assertRaisesRegex(PaidUnavailable, "invalid_paid_translation"):
                    paid.translate(cues, "en", "")
            db.close()

    def test_translation_does_not_cache_incomplete_or_refused_results(self):
        with tempfile.TemporaryDirectory() as directory:
            db = sqlite3.connect(Path(directory) / "db")
            paid = Paid(db, {"api_key": "unit-test-placeholder", "translation_model": "gpt-4o-mini"}, 1)
            valid_text = json.dumps({"translations": [{"id": 0, "text": "Português europeu"}]})
            responses = [
                {"status": "incomplete", "output": [{"content": [{"type": "output_text", "text": valid_text}]}]},
                {"status": "completed", "output": [{"content": [{"type": "refusal", "refusal": "Declined"}]}]},
            ]
            with patch.object(paid, "post") as post:
                for response in responses:
                    post.return_value.json.return_value = response
                    with self.assertRaisesRegex(PaidUnavailable, "invalid_paid_translation|translation_refused"):
                        paid.translate(dialogue()[:1], "pt-PT", "")
            self.assertEqual(db.execute("SELECT count(*) FROM paid_cache").fetchone()[0], 0)
            self.assertEqual(db.execute("SELECT count(*) FROM paid_ledger").fetchone()[0], 3)
            db.close()

    def test_budget_survives_restart_and_charges_ambiguous_failures(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "db"
            db = sqlite3.connect(path)
            config = {"api_key": "unit-test-placeholder", "base_url": "https://api.openai.com/v1"}
            Paid(db, config, 1).reserve(0.7, "transcribe")
            db.close()
            db = sqlite3.connect(path)
            with self.assertRaisesRegex(PaidUnavailable, "budget"):
                Paid(db, config, 1).reserve(0.4, "translate")
            db.close()

    def test_provider_movie_and_episode_identity(self):
        self.assertFalse(identity({"title"}, False))
        self.assertTrue(identity({"imdb_id"}, False))
        self.assertFalse(identity({"series", "season"}, True))
        self.assertTrue(identity({"series", "season", "episode"}, True))

    def test_provider_download_link_cannot_leave_its_host(self):
        with self.assertRaises(ValueError):
            safe_yify_url("https://untrusted.example/file")
        self.assertEqual(safe_yify_url("/movie-imdb/tt1"), "https://yifysubtitles.ch/movie-imdb/tt1")


if __name__ == "__main__":
    unittest.main()
