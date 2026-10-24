"""Untagged dialogue must use real language evidence and retain word checks."""
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from manager import Review, canonical
from mediawork import MediaAssets
import test_manager as fixtures


class AudioLanguageTests(unittest.TestCase):
    setUp = fixtures.OwnershipTests.setUp
    caption = fixtures.OwnershipTests.caption

    def detect(self, values, reference=None):
        model = Mock()
        model.transcribe.side_effect = [([], SimpleNamespace(language=lang, language_probability=p,
                                                            duration_after_vad=seconds))
                                       for lang, p, seconds in values]
        reference = reference or SimpleNamespace(identity="media:stream1", sample=Mock())
        with patch.object(self.manager, "speech_model", return_value=model), patch.object(self.manager, "emit"):
            result = self.manager.detect_audio_language(reference, 120, self.root)
        return result, model

    def test_independent_confident_samples_identify_language_without_forcing_it(self):
        result, model = self.detect([("fr", .99, 12), ("fr", .95, 10), ("en", .6, 8)])
        self.assertEqual((result["language"], result["status"]), ("fr", "confident"))
        self.assertEqual(model.transcribe.call_count, 3)
        for call in model.transcribe.call_args_list:
            self.assertIsNone(call.kwargs["language"])
            self.assertTrue(call.kwargs["vad_filter"])

    def test_mixed_low_confidence_or_silent_samples_do_not_establish_language(self):
        for values in [[("en", .99, 10), ("fr", .99, 10), ("en", .99, 10)],
                       [("en", .89, 10)] * 3, [("en", .99, 0)] * 3]:
            with self.subTest(values=values):
                self.manager.audio_language_samples.clear()
                result, _ = self.detect(values)
                self.assertEqual((result["language"], result["status"]), ("", "inconclusive"))

    def test_detection_does_not_establish_portuguese_locale(self):
        result, _ = self.detect([("pt", .99, 10)] * 3)
        self.assertEqual(result["language"], "pt")
        from locales import matches
        self.assertFalse(matches(result["language"], "pt-PT"))

    def test_detection_failure_remains_unconfirmed_and_does_not_expose_error_text(self):
        with patch.object(self.manager, "speech_model", side_effect=RuntimeError("private-provider-value")), \
             patch.object(self.manager, "emit"):
            result = self.manager.detect_audio_language(SimpleNamespace(identity="media:1", sample=Mock()), 120, self.root)
        self.assertEqual((result["language"], result["status"]), ("", "unavailable"))
        self.assertNotIn("private-provider-value", str(result))

    def test_english_only_verification_model_does_not_force_english_detection(self):
        self.manager.cfg["verification_model"] = "base.en"
        model = Mock()
        model.transcribe.return_value = ([], SimpleNamespace(language="fr", language_probability=.99, duration_after_vad=10))
        with patch.object(self.manager, "speech_model", return_value=model) as load, patch.object(self.manager, "emit"):
            result = self.manager.detect_audio_language(SimpleNamespace(identity="media:1", sample=Mock()), 120, self.root)
        self.assertEqual(result["language"], "fr")
        self.assertEqual(load.call_args.args, ("base",))

    def test_observations_survive_restart_and_are_isolated_by_stream_and_media(self):
        streams = [{"codec_type": "audio", "index": 1, "tags": {}}]
        self.manager.current_item = self.item
        def assets(fp):
            value = MediaAssets(self.manager, str(self.video), fp, streams, 120, self.root)
            self.addCleanup(value.close)
            return value
        first = assets(self.fp)
        with patch("manager.run", return_value=b""):
            result, _ = self.detect([("en", .99, 10)] * 3, first.audio(1))
        self.assertEqual(result["language"], "en")
        self.manager.audio_language_samples.clear()
        with patch("manager.run", side_effect=AssertionError("audio reread")), \
             patch.object(self.manager, "speech_model", side_effect=AssertionError("model repeated")):
            cached = self.manager.detect_audio_language(first.audio(1), 120, self.root)
        self.assertEqual(cached, result)
        with patch("manager.run", return_value=b""):
            other, model = self.detect([("fr", .99, 10)] * 3, first.audio(2))
            changed, changed_model = self.detect([("de", .99, 10)] * 3, assets("new-media-fingerprint").audio(1))
        self.assertEqual(other["language"], "fr")
        self.assertEqual(changed["language"], "de")
        self.assertEqual((model.transcribe.call_count, changed_model.transcribe.call_count), (3, 3))

    def test_known_audio_tags_skip_detection_and_keep_literal_verification(self):
        candidate = self.root / "candidate.srt"
        fixtures.dialogue().save(str(candidate), format_="srt")
        with patch.object(self.manager, "detect_audio_language", side_effect=AssertionError("tagged audio")), \
             patch.object(self.manager, "verify_words", return_value={"status": "verified_samples"}) as verify:
            result = self.manager.publish(self.item, self.fp, "en", candidate, {"source": "provider"},
                                          "reference.wav", {"index": 1, "tags": {"language": "eng"}}, 120, self.root)
        verify.assert_called_once()
        self.assertEqual(result["audio_language"], "en")
        self.assertNotIn("audio_language_detection", result)

    def test_untagged_audio_uses_word_check_instead_of_vad_and_records_evidence(self):
        candidate = self.root / "candidate.srt"
        fixtures.dialogue().save(str(candidate), format_="srt")
        evidence = {"language": "en", "status": "confident", "samples": [{"probability": .99}]}
        with patch.object(self.manager, "detect_audio_language", return_value=evidence), \
             patch.object(self.manager, "sync", side_effect=AssertionError("VAD cannot prove words")), \
             patch.object(self.manager, "verify_words", return_value={"status": "verified_samples"}) as verify:
            result = self.manager.publish(self.item, self.fp, "en", candidate, {"source": "provider"},
                                          "reference.wav", {"index": 1, "tags": {}}, 120, self.root)
        verify.assert_called_once()
        self.assertEqual(result["audio_language_detection"], evidence)
        self.assertEqual(result["timing"]["method"], "sampled_word_timestamps")
        self.assertTrue(Path(result["output"]).is_file())

    def test_detected_language_does_not_bypass_inconclusive_word_checks(self):
        candidate = self.root / "candidate.srt"
        fixtures.dialogue().save(str(candidate), format_="srt")
        def sync(reference, source, destination):
            destination.write_bytes(source.read_bytes())
            return {"method": "mock_vad"}
        with patch.object(self.manager, "detect_audio_language", return_value={"language": "en"}), \
             patch.object(self.manager, "sync", side_effect=sync), \
             patch.object(self.manager, "verify_words", return_value={"status": "inconclusive"}):
            with self.assertRaisesRegex(Review, "word_verification_inconclusive"):
                self.manager.publish(self.item, self.fp, "en", candidate, {"source": "provider"},
                                     "reference.wav", {"index": 1, "tags": {}}, 120, self.root)
        self.assertFalse(Path(canonical(self.video, "en")).exists())

    def test_translation_budget_wait_does_not_run_detection_or_decode_audio(self):
        self.manager.cfg.update(paid_fallback=True, paid_daily_usd=2)
        source = self.caption()
        detail = {"en": {"output": str(source)}, "pt-PT": {"providers": {"test": "searched"}}}
        from paid import PaidUnavailable
        with patch.object(self.manager, "detect_audio_language", side_effect=AssertionError("unnecessary detection")), \
             patch("paid.Paid") as paid:
            paid.return_value.translate.side_effect = PaidUnavailable("daily_paid_budget_exhausted")
            result = self.manager.fallback(self.item, self.fp, [{"codec_type": "audio", "index": 1, "tags": {}}],
                                           120, "pt-PT", detail, self.root)
        self.assertEqual(result["translation_failure"], "daily_paid_budget_exhausted")

    def test_unknown_audio_can_supply_free_transcription_after_completed_search(self):
        self.manager.cfg.update(local_transcription=True)
        with patch.object(self.manager, "detect_audio_language", return_value={"language": "en"}), \
             patch.object(self.manager, "transcribe", return_value=("generated.srt", {"source": "local_transcription"})) as transcribe, \
             patch.object(self.manager, "publish", return_value={"output": "accepted.srt"}):
            result = self.manager.fallback(self.item, self.fp, [{"codec_type": "audio", "index": 1, "tags": {}}],
                                           120, "en", {"en": {"providers": {"test": "searched"}}}, self.root)
        transcribe.assert_called_once()
        self.assertEqual(result["output"], "accepted.srt")
