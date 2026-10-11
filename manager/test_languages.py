"""Regression checks for installations whose target languages differ."""
import io
import json
from pathlib import Path
import subprocess
from types import SimpleNamespace
from unittest.mock import patch
import zipfile
import unittest

from babelfish import Language
from PIL import Image
from locales import ambiguous, language, locale, matches, normalize, ocr_language
from manager import Manager, Review, canonical, digest
from manager import words
from paid import Paid
from pgs import recognize
from providers import accepted_language, candidates, yify
import test_manager as fixtures
from test_manager import dialogue


FRENCH = "Bonjour mon ami, demain nous visiterons le magnifique jardin avec toute notre famille."


class ConfigurableLanguagesTests(unittest.TestCase):
    setUp = fixtures.OwnershipTests.setUp
    register = fixtures.OwnershipTests.register
    caption = fixtures.OwnershipTests.caption
    def test_language_tags_are_validated_and_regional_matching_is_exact(self):
        for raw, expected in [('fra', 'fr'), ('ES_mx', 'es-MX'), ('pt-BR', 'pt-BR'), ('zh-hant-tw', 'zh-Hant-TW')]:
            self.assertEqual(normalize(raw), expected)
        for bad in ['auto', 'xx', 'und', '../en', 'en-XX']:
            with self.assertRaises(ValueError): normalize(bad)
        self.assertTrue(matches('fr-CA', 'fr'))
        self.assertFalse(matches('fr', 'fr-CA'))
        self.assertFalse(matches('fr-FR', 'fr-CA'))
        self.assertTrue(ambiguous('fr', 'fr-CA'))
        self.assertFalse(ambiguous('fr-FR', 'fr-CA'))
        self.assertTrue(accepted_language(Language('por', 'BR'), 'pt-BR'))
        self.assertFalse(accepted_language(Language('por', 'PT'), 'pt-BR'))
        self.assertEqual(language('pob'), 'pt-BR')
        self.assertEqual(language('Portuguese (Brazilian)'), 'pt-BR')
        self.assertEqual(ocr_language('zh-Hans-TW'), 'chi_sim')

    def test_cleanup_uses_requested_languages_and_preserves_ambiguous_locales(self):
        self.manager.languages = ['fr', 'es']
        for code in ['fr', 'es', 'en', 'pt-PT']:
            self.video.with_suffix('.' + code + '.srt').write_text(code)
        self.manager.cleanup_sidecars(str(self.video), self.fp, False)
        self.assertTrue(self.video.with_suffix('.fr.srt').exists())
        self.assertTrue(self.video.with_suffix('.es.srt').exists())
        self.assertFalse(self.video.with_suffix('.en.srt').exists())
        self.assertFalse(self.video.with_suffix('.pt-PT.srt').exists())
        self.manager.languages = ['fr-CA']
        self.video.with_suffix('.fr-FR.srt').write_text('French from France')
        self.manager.cleanup_sidecars(str(self.video), self.fp, False)
        self.assertTrue(self.video.with_suffix('.fr.srt').exists())
        self.assertFalse(self.video.with_suffix('.fr-FR.srt').exists())
        self.manager.cleanup_sidecars(str(self.video), self.fp, True)
        self.assertFalse(self.video.with_suffix('.fr.srt').exists())

    def test_non_latin_subtitle_text_is_not_discarded(self):
        self.assertIn('привет', words('Привет, мир!'))
        self.assertIn('مرحبا', words('مرحبا بالعالم'))
        self.assertEqual(words('こんにちは世界'), list('こんにちは世界'))
        self.manager.languages = ['ja']
        candidate = self.root / 'ja.srt'
        cues = dialogue()
        for i, cue in enumerate(cues): cue.text = f'こんにちはみんな、私たちは明日友達と美しい公園に行きます。番号{i}。'
        cues.save(str(candidate), format_='srt')
        with patch.object(self.manager, 'verify_words', return_value={'status': 'verified_samples'}):
            result = self.manager.publish(self.item, self.fp, 'ja', candidate, {'source': 'embedded'}, None,
                                          {'index': 1, 'tags': {'language': 'jpn'}}, 120, self.root)
        self.assertEqual(result['output'], canonical(self.video, 'ja'))
        self.assertTrue(Path(result['output']).exists())

    def test_french_publication_and_feedback_do_not_require_english(self):
        self.manager.languages = ['fr']
        self.register()
        candidate = self.root / 'candidate-fr.srt'
        cues = dialogue()
        for i, cue in enumerate(cues): cue.text = FRENCH + f" Numéro {i}."
        cues.save(str(candidate), format_='srt')
        audio = {'index': 1, 'tags': {'language': 'fra'}}
        with patch.object(self.manager, 'verify_words', return_value={'status': 'verified_samples'}) as verify:
            result = self.manager.publish(self.item, self.fp, 'fr', candidate, {'source': 'embedded'}, None, audio, 120, self.root)
        self.assertEqual(Path(result['output']).name, 'Film (2024).subtitler.fr.srt')
        self.assertEqual(verify.call_args.args[2], 'fr')
        self.manager.catalogue.report(str(self.video), 'fra', 'wrong_text')
        self.manager.feedback(str(self.video), self.fp)
        self.assertFalse(Path(result['output']).exists())

    def test_single_foreign_target_uses_an_internal_source_without_extra_sidecars(self):
        self.manager.languages = ['fr']
        self.manager.cfg.update(paid_fallback=True, paid_daily_usd=1)
        source_file = self.root / 'source-en.srt'
        dialogue().save(str(source_file), format_='srt')
        audio = {'codec_type': 'audio', 'index': 1, 'tags': {'language': 'eng'}}
        with patch.object(self.manager, 'verify_words', return_value={'status': 'verified_samples'}):
            source = self.manager.publish(self.item, self.fp, 'en', source_file, {'source': 'embedded'}, None, audio, 120, self.root)
        self.assertTrue(Path(source['output']).is_relative_to(self.manager.catalogue.root))
        self.assertFalse(Path(canonical(self.video, 'en')).exists())
        with patch('paid.Paid') as paid, patch.object(self.manager, 'sync', side_effect=AssertionError('inherited clock was realigned')):
            paid.return_value.translate.return_value = [FRENCH + f" Numéro {i}." for i in range(30)]
            result = self.manager.fallback(self.item, self.fp, [audio], 120, 'fr',
                                           {'fr': {'providers': {'opensubtitlescom': 'searched'}}}, self.root)
        self.assertEqual(result['timing']['method'], 'inherited_accepted_source_timestamps')
        self.assertTrue(Path(canonical(self.video, 'fr')).exists())
        self.assertFalse(Path(canonical(self.video, 'en')).exists())
        self.manager.languages = ['en']
        with patch('manager.run', return_value=b''), patch.object(self.manager, 'verify_words', return_value={'status': 'verified_samples'}):
            promoted = self.manager.ensure(self.item, self.fp, [audio], 120, 'en', self.root)
        self.assertEqual(promoted['output'], canonical(self.video, 'en'))
        self.assertFalse(promoted['internal_source'])

    def test_setting_change_requeues_previously_managed_media(self):
        self.register()
        db = self.manager.catalogue.db
        db.execute("UPDATE media SET checked=100,status='managed',next_try=9999999999")
        db.execute("INSERT INTO settings VALUES('required_languages',?)", (json.dumps(self.manager.languages),))
        db.commit()
        self.manager.languages = ['fr', 'es', 'de']
        audio = {'codec_type': 'audio', 'index': 1, 'tags': {'language': 'fra'}}
        events = []
        def ensure(item, fp, streams, duration, lang, work):
            events.append(('search', lang))
            return {'providers': {'opensubtitlescom': 'searched'}}
        def fallback(item, fp, streams, duration, lang, detail, work):
            events.append(('generate', lang))
            return {'output': 'accepted-' + lang + '.srt'}
        with patch.object(self.manager, 'probe', return_value=([audio], 120)), \
             patch.object(self.manager, 'ensure', ensure), patch.object(self.manager, 'fallback', fallback), \
             patch.object(self.manager, 'cleanup_sidecars'), patch.object(self.manager, 'prune', return_value=(self.fp, {'status': 'disabled'})), \
             patch.object(self.manager, 'emit'):
            self.manager.scan()
        self.assertEqual(events, [('search', 'fr'), ('search', 'es'), ('search', 'de'),
                                  ('generate', 'fr'), ('generate', 'es'), ('generate', 'de')])
        self.assertEqual(json.loads(db.execute("SELECT value FROM settings WHERE key='required_languages'").fetchone()[0]), ['fr', 'es', 'de'])

    def test_speech_and_ocr_use_the_selected_language(self):
        segments = [SimpleNamespace(start=c.start / 1000, end=c.end / 1000, text=FRENCH + f" Numéro {i}.",
                                    no_speech_prob=.1, avg_logprob=-.1, words=[]) for i, c in enumerate(dialogue())]
        with patch.object(self.manager, 'speech_model') as model:
            model.return_value.transcribe.return_value = (segments, None)
            self.manager.transcribe(self.root / 'audio.wav', 'fr-CA', self.root, 120)
            self.assertEqual(model.return_value.transcribe.call_args.kwargs['language'], 'fr')
            with patch('manager.run', return_value=b''):
                self.manager.verify_words(self.root / 'audio.wav', dialogue(), 'de', self.root)
            self.assertEqual(model.return_value.transcribe.call_args.kwargs['language'], 'de')
        result = SimpleNamespace(returncode=0, stdout=b'block_num\tpar_num\tline_num\ttext\tconf\n1\t1\t1\tBonjour\t95\n')
        with patch('pgs.subprocess.run', return_value=result) as call:
            self.assertEqual(recognize(Image.new('L', (2, 2)), 'fr')[0], 'Bonjour')
        command = call.call_args.args[0]
        self.assertEqual(command[command.index('-l') + 1], 'fra')

    def test_provider_requests_the_configured_language(self):
        from subliminal import Movie
        config = self.root / 'providers.json'
        config.write_text(json.dumps({'providers': ['opensubtitlescom']}))
        sub = SimpleNamespace(language=Language('fra'), foreign_only=False, movie_imdb_id='tt1234567', release='',
                              id='one', provider_name='opensubtitlescom', text=FRENCH, content=FRENCH.encode(),
                              get_matches=lambda video: {'imdb_id'})
        with patch('subliminal.scan_video', return_value=Movie(str(self.video), 'Film', year=2024)), \
             patch('subliminal.ProviderPool') as pool, patch('subliminal.compute_score', return_value=100):
            p = pool.return_value.__enter__.return_value
            p.__getitem__.return_value.query.return_value = [sub]
            p.download_subtitle.return_value = True
            found = list(candidates({**self.item, 'IMDBID': 'tt1234567'}, 'fr', self.root, str(config), lambda *a: False, {}))
        self.assertEqual(p.__getitem__.return_value.query.call_args.args[0], {Language('fra')})
        self.assertEqual(len(found), 1)
        self.assertEqual(found[0][1]['language_evidence'], 'fr')

    @unittest.skipUnless(fixtures.shutil.which('ffmpeg'), 'ffmpeg required')
    def test_embedded_cleanup_keeps_configured_french_and_spanish(self):
        files = []
        for code in ['fr', 'es', 'en']:
            f = self.root / (code + '.srt')
            f.write_text('1\n00:00:00,000 --> 00:00:01,500\nCaption\n')
            files.append(f)
        command = ['ffmpeg', '-v', 'error', '-y', '-f', 'lavfi', '-i', 'color=c=black:s=32x32:d=2',
                   '-f', 'lavfi', '-i', 'anullsrc=r=16000:cl=mono']
        for f in files: command += ['-i', str(f)]
        command += ['-t', '2', '-map', '0:v', '-map', '1:a', '-map', '2:s', '-map', '3:s', '-map', '4:s',
                    '-c:v', 'ffv1', '-c:a', 'pcm_s16le', '-c:s', 'srt', '-metadata:s:a:0', 'language=tur',
                    '-metadata:s:s:0', 'language=fra', '-metadata:s:s:1', 'language=spa', '-metadata:s:s:2', 'language=eng', str(self.video)]
        subprocess.run(command, check=True)
        self.manager.languages = ['fr', 'es']
        self.manager.cfg.update(prune_embedded=True)
        fp = fixtures.fingerprint(self.video)
        streams, duration = self.manager.probe(str(self.video))
        self.manager.prune(self.item, fp, streams, duration, False)
        after, _ = self.manager.probe(str(self.video))
        self.assertEqual([language(s.get('tags', {}).get('language')) for s in after if s['codec_type'] == 'subtitle'], ['fr', 'es'])
        self.assertEqual([language(s.get('tags', {}).get('language')) for s in after if s['codec_type'] == 'audio'], ['tr'])

    def test_yify_accepts_french_and_translation_prompt_selects_its_target(self):
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, 'w') as z: z.writestr('fr.srt', FRENCH)
        page = b'<table class="other-subs"><tbody><tr><td>1</td><td>French</td><td><a href="/subtitle/one">Get</a></td></tr></tbody></table>'
        with patch('providers.limited_get', side_effect=[page, b'<a class="download-subtitle" href="/download/one">Get</a>', archive.getvalue()]):
            found = list(yify('tt1234567', self.root, lambda *a: False, {}, 'fr'))
        self.assertEqual(found[0][1]['language_evidence'], 'fr')
        paid = Paid(self.manager.catalogue.db, {'api_key': 'unit-test-placeholder', 'translation_model': 'gpt-4o-mini'}, 1)
        response = {'output': [{'content': [{'type': 'output_text', 'text': json.dumps({'translations': [{'id': 0, 'text': FRENCH}]})}]}]}
        with patch.object(paid, 'post') as post:
            post.return_value.json.return_value = response
            self.assertEqual(paid.translate(dialogue()[:1], 'fr', ''), [FRENCH])
        self.assertTrue(post.call_args.kwargs['json']['input'].startswith('Translate subtitle dialogue to French (fr).'))
