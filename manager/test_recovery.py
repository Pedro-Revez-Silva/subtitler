"""Regressions for production failures and bounded recovery."""
import io
import json
import os
from pathlib import Path
import sqlite3
import subprocess
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from manager import Review, diagnostic, media_duration, run, recovery_action, fingerprint, digest
from locales import language
from paid import Paid, PaidUnavailable
import test_manager as fixtures
from test_manager import dialogue


class RecoveryTests(unittest.TestCase):
    setUp = fixtures.OwnershipTests.setUp
    caption = fixtures.OwnershipTests.caption
    register = fixtures.OwnershipTests.register

    def test_missing_portuguese_titles_are_valid_metadata(self):
        self.assertEqual(language('por', None), 'pt')
        self.assertEqual(language(None, None), '')
        self.assertEqual(language('pob', None), 'pt-BR')
        stream = {'codec_type': 'audio', 'index': 1, 'tags': {'language': 'por'}}
        self.assertEqual(self.manager.audio([stream], 'pt-PT', '')['index'], 1)

    def test_same_language_reference_does_not_claim_a_missing_locale(self):
        english={'codec_type':'audio','index':1,'tags':{'language':'eng'}}
        generic={'codec_type':'audio','index':2,'tags':{'language':'por'}}
        brazil={'codec_type':'audio','index':3,'tags':{'language':'pob'}}
        self.assertEqual(self.manager.audio([english,generic,brazil],'pt-PT','en')['index'],2)
        self.assertEqual(self.manager.audio([english,brazil],'pt-PT','en')['index'],1)
        self.assertEqual(language(generic['tags']['language']),'pt')

    def test_recovery_actions_distinguish_budget_credentials_and_uncertainty(self):
        self.assertEqual(recovery_action({'pt-PT':{'paid_failure':'daily_paid_budget_exhausted'}},'needs_attention'),'wait_for_daily_budget')
        self.assertEqual(recovery_action({'en':{'translation_diagnostic':{'cause':'quota'}}},'needs_attention'),'provider_configuration_required')
        self.assertEqual(recovery_action({'en':{'failures':['candidate_verification_pending']}},'needs_attention'),'try_other_candidates')
        self.assertEqual(recovery_action({'pt-PT':{'translation_failure':'translation_call_limit'}},'needs_attention'),'resume_translation')
        self.assertEqual(recovery_action({},'managed_cleanup_pending'),'retry_subtitle_cleanup')

    @unittest.skipUnless(fixtures.shutil.which('ffmpeg') and fixtures.shutil.which('mkvmerge'), 'ffmpeg and mkvmerge required')
    def test_matroska_cleanup_preserves_cover_art_and_audio_packets(self):
        from PIL import Image
        art=self.root/'cover.jpg'; Image.new('RGB',(32,32),'blue').save(art)
        subtitles=self.root/'captions.srt'; subtitles.write_text('1\n00:00:00,000 --> 00:00:01,500\nCaption\n')
        subprocess.run(['ffmpeg','-v','error','-y','-f','lavfi','-i','color=c=black:s=32x32:d=2',
                        '-f','lavfi','-i','anullsrc=r=16000:cl=mono','-i',str(subtitles),'-t','2',
                        '-map','0:v','-map','1:a','-map','2:s','-c:v','ffv1','-c:a','pcm_s16le','-c:s','srt',
                        '-metadata:s:s:0','language=tur','-attach',str(art),'-metadata:s:t:0','mimetype=image/jpeg',str(self.video)],check=True)
        self.manager.cfg.update(prune_embedded=True)
        seed=self.root/'seed.mkv'; os.link(self.video,seed)
        original=digest(seed); fp=fingerprint(self.video)
        def audio_hash(path):
            return subprocess.check_output(['ffmpeg','-v','error','-i',str(path),'-map','0:a','-c','copy','-f','hash','-hash','sha256','-'])
        before_audio=audio_hash(self.video); streams,duration=self.manager.probe(str(self.video))
        _,result=self.manager.prune(self.item,fp,streams,duration,False)
        self.assertEqual(result['status'],'remuxed'); self.assertEqual(digest(seed),original)
        self.assertEqual(audio_hash(self.video),before_audio)
        after,_=self.manager.probe(str(self.video))
        self.assertFalse(any(s['codec_type']=='subtitle' for s in after))
        covers=[s for s in after if s.get('disposition',{}).get('attached_pic')]
        self.assertEqual(len(covers),1); self.assertEqual(covers[0]['tags']['filename'],'cover.jpg')

    def test_tool_errors_preserve_category_without_secret_messages(self):
        result = SimpleNamespace(returncode=1, stdout=b'', stderr=b'Permission denied secret-api-key')
        with patch('manager.subprocess.run', return_value=result):
            with self.assertRaises(Review) as raised: run(['ffmpeg', 'secret-url'])
        d = diagnostic(raised.exception, 'embedded_cleanup')
        self.assertEqual(d['category'], 'permission')
        self.assertNotIn('secret', json.dumps(d))
        with patch('manager.subprocess.run', side_effect=subprocess.TimeoutExpired('secret-command', 3)):
            with self.assertRaisesRegex(Review, 'media_tool_timeout') as raised: run(['ffmpeg'], 3)
        self.assertEqual(raised.exception.diagnostic['timeout_seconds'], 3)

    def test_container_duration_does_not_include_trailing_subtitle_packets(self):
        streams = [{'codec_type':'video','tags':{'DURATION':'01:55:28.714'}},
                   {'codec_type':'audio','tags':{'DURATION':'01:55:28.704'}},
                   {'codec_type':'subtitle','duration':'6930.423'},
                   {'codec_type':'video','duration':'6930.423','disposition':{'attached_pic':1}}]
        self.assertEqual(media_duration(streams, '6930.423'), 6928.714)
        self.assertEqual(media_duration([{'codec_type':'audio','duration':'2.5'}], '3'), 2.5)
        self.assertEqual(media_duration([{'codec_type':'video'}], '3'), 3)

    def test_language_failure_keeps_successes_and_has_safe_diagnostics(self):
        self.register()
        def ensure(item, fp, streams, duration, lang, work):
            if lang == 'pt-PT': raise TypeError('private key in an upstream exception')
            return {'output':'accepted.srt'}
        stream = {'codec_type':'audio','index':1,'tags':{'language':'eng'}}
        with patch.object(self.manager,'probe',return_value=([stream],120)), \
             patch.object(self.manager,'ensure',ensure), \
             patch.object(self.manager,'cleanup_sidecars'), \
             patch.object(self.manager,'prune',return_value=(self.fp,{'status':'disabled'})), \
             patch.object(self.manager,'emit'):
            self.manager.scan()
        status, raw = self.manager.catalogue.db.execute('SELECT status,detail FROM media').fetchone()
        detail = json.loads(raw)
        self.assertEqual(status,'needs_attention')
        self.assertEqual(detail['en']['output'],'accepted.srt')
        self.assertEqual(detail['pt-PT']['diagnostic']['exception'],'TypeError')
        self.assertNotIn('private key',raw)
        self.assertEqual(detail['recovery']['retry_in_seconds'],3600)

    def test_cleanup_failure_is_separate_from_subtitle_coverage(self):
        self.register()
        stream = {'codec_type':'audio','index':1,'tags':{'language':'eng'}}
        with patch.object(self.manager,'probe',return_value=([stream],120)), \
             patch.object(self.manager,'ensure',return_value={'output':'accepted.srt'}), \
             patch.object(self.manager,'cleanup_sidecars'), \
             patch.object(self.manager,'prune',side_effect=Review('media_tool_failed',{'category':'timestamps'})), \
             patch.object(self.manager,'emit'):
            self.manager.scan()
        status, raw = self.manager.catalogue.db.execute('SELECT status,detail FROM media').fetchone()
        self.assertEqual(status,'managed_cleanup_pending')
        d = json.loads(raw)
        self.assertIn('output',d['en']); self.assertIn('output',d['pt-PT'])
        self.assertEqual(d['track_policy']['diagnostic']['category'],'timestamps')

    def test_due_retries_are_not_starved_by_the_initial_queue(self):
        self.register()
        db = self.manager.catalogue.db
        db.execute("INSERT INTO settings VALUES('recovery_revision','1')")
        db.execute("INSERT INTO settings VALUES('required_languages',?)",(json.dumps(self.manager.languages),))
        db.execute("UPDATE media SET status='needs_attention',checked=100,next_try=0")
        for i in range(10):
            path = self.root / f'A{i}.mkv'; path.write_bytes(b'media')
            self.manager.request['items'].append({**self.item,'Path':str(path)})
        db.commit()
        self.manager.request['dry_run'] = True
        with patch.object(self.manager,'emit') as emit: self.manager.scan()
        self.assertEqual(emit.call_args_list[0].kwargs['path'],str(self.video))

    def test_uncertain_candidates_are_deferred_without_permanent_rejection(self):
        cat = self.manager.catalogue
        cat.defer(str(self.video),self.fp,'en','sha','provider:one','word_verification_inconclusive')
        self.assertTrue(cat.deferred(str(self.video),self.fp,'en',source='provider:one'))
        self.assertFalse(cat.rejected(str(self.video),self.fp,'en',source='provider:one'))
        with patch('providers.candidates') as candidates:
            candidates.return_value = iter([])
            list(self.manager.provider_candidates(self.item,'en',self.root))
            skip = candidates.call_args.args[4]
            self.assertTrue(skip('','provider:one'))
            self.assertFalse(skip('','provider:two'))
        with patch('manager.time.time',return_value=999999999999):
            self.assertFalse(cat.deferred(str(self.video),self.fp,'en',source='provider:one'))

    def test_inconclusive_words_escalate_locally_without_relaxing_acceptance(self):
        def metric(*args):
            metric.calls += 1
            return {'status':'inconclusive' if metric.calls <= 3 else 'verified'}
        metric.calls = 0
        model = SimpleNamespace(transcribe=lambda *a,**k:([],None))
        with patch.object(self.manager,'speech_model',return_value=model) as models, \
             patch('manager.run',return_value=b''), patch('manager.word_metrics',side_effect=metric), \
             patch.object(self.manager,'emit'):
            result = self.manager.verify_words(self.root/'audio.wav',dialogue(),'en',self.root)
        self.assertEqual(result['status'],'verified_samples')
        self.assertEqual(result['method'],'local_whisper_small_int8')
        self.assertEqual([m.args[0] for m in models.call_args_list],['base']*3+['small']*3)

    def test_translated_provider_can_use_an_accepted_subtitle_clock(self):
        from manager import digest
        source = self.caption()
        candidate = self.root/'pt.srt'; cues=dialogue()
        for i,c in enumerate(cues): c.text=f'Olá meu amigo número {i}, amanhã vamos visitar um jardim muito bonito com a nossa família.'
        cues.save(str(candidate),format_='srt')
        def sync(reference,source_file,output):
            if Path(reference).suffix=='.wav': raise Review('timing_alignment_inconclusive')
            output.write_bytes(source_file.read_bytes())
            return {'method':'ffsubsync_subtitle_reference','content_proof':False}
        with patch.object(self.manager,'sync',side_effect=sync):
            result=self.manager.publish(self.item,self.fp,'pt-PT',candidate,{'source':'provider'},self.root/'audio.wav',
                                        {'index':1,'tags':{'language':'eng'}},120,self.root)
        self.assertEqual(result['timing']['reference_sha'],digest(source))
        self.assertEqual(result['content']['status'],'translated_text_unverified')


class TranslationRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.db=sqlite3.connect(':memory:'); self.addCleanup(self.db.close)
        self.paid=Paid(self.db,{'api_key':'unit-test-placeholder','translation_model':'gpt-4o-mini'},1)

    def response(self,batch):
        return {'status':'completed','output':[{'content':[{'type':'output_text','text':json.dumps({'translations':[
            {'id':v['id'],'text':'Tradução europeia '+str(v['id'])} for v in batch]})}]}]}

    def test_failed_batch_splits_and_caches_valid_parts(self):
        sizes=[]
        def post(endpoint,**kw):
            batch=json.loads(kw['json']['input'].split('\nInput: ',1)[1]); sizes.append(len(batch))
            response={'status':'incomplete','output':[]} if len(batch)>2 else self.response(batch)
            return SimpleNamespace(json=lambda:response)
        with patch.object(self.paid,'post',side_effect=post):
            result=self.paid.translate(dialogue()[:4],'pt-PT','')
            cached=self.paid.translate(dialogue()[:4],'pt-PT','')
        self.assertEqual(sizes,[4,2,2]); self.assertEqual(result,cached); self.assertEqual(len(result),4)
        self.assertEqual(self.db.execute('SELECT count(*) FROM paid_ledger').fetchone()[0],3)

    def test_resuming_uses_successful_batches_and_respects_request_limit(self):
        def post(endpoint,**kw):
            batch=json.loads(kw['json']['input'].split('\nInput: ',1)[1])
            response=self.response(batch) if len(batch)==1 else {'status':'incomplete','output':[]}
            return SimpleNamespace(json=lambda:response)
        with patch.object(self.paid,'post',side_effect=post) as call:
            with self.assertRaisesRegex(PaidUnavailable,'translation_call_limit'):
                self.paid.translate(dialogue(),'pt-PT','')
        self.assertEqual(call.call_count,20)
        self.assertGreater(self.db.execute('SELECT count(*) FROM paid_cache').fetchone()[0],0)

    def test_refusal_does_not_retry_or_split(self):
        response={'status':'completed','output':[{'content':[{'type':'refusal'}]}]}
        with patch.object(self.paid,'post',return_value=SimpleNamespace(json=lambda:response)) as call:
            with self.assertRaisesRegex(PaidUnavailable,'translation_refused') as raised:
                self.paid.translate(dialogue(),'pt-PT','')
        self.assertEqual(call.call_count,1); self.assertEqual(raised.exception.diagnostic['cause'],'refusal')

    def test_provider_failure_resumes_from_validated_cache(self):
        calls=[]
        def post(endpoint,**kw):
            batch=json.loads(kw['json']['input'].split('\nInput: ',1)[1]); calls.append(batch[0]['id'])
            if batch[0]['id']==40 and calls.count(40)==1: raise PaidUnavailable('paid_provider_failed')
            return SimpleNamespace(json=lambda:self.response(batch))
        cues=dialogue(); cues.extend(dialogue())
        with patch.object(self.paid,'post',side_effect=post):
            with self.assertRaisesRegex(PaidUnavailable,'paid_provider_failed'): self.paid.translate(cues,'pt-PT','')
            translated=self.paid.translate(cues,'pt-PT','')
        self.assertEqual(calls,[0,40,40]); self.assertEqual(len(translated),60)

    def test_missing_cue_reason_is_recorded_after_bounded_leaf_retry(self):
        with patch.object(self.paid,'post',return_value=SimpleNamespace(json=lambda:self.response([]))) as call:
            with self.assertRaises(PaidUnavailable) as raised: self.paid.translate(dialogue()[:1],'pt-PT','')
        self.assertEqual(call.call_count,2)
        self.assertEqual(raised.exception.diagnostic['cause'],'missing_or_extra_cues')

    def test_invalid_cached_cues_are_retranslated_and_not_published(self):
        def post(endpoint,**kw):
            batch=json.loads(kw['json']['input'].split('\nInput: ',1)[1])
            return SimpleNamespace(json=lambda:self.response(batch))
        with patch.object(self.paid,'post',side_effect=post) as call:
            original=self.paid.translate(dialogue()[:1],'pt-PT','')
            self.db.execute("UPDATE paid_cache SET data=?",(json.dumps({'99':'wrong cue'}),));self.db.commit()
            repaired=self.paid.translate(dialogue()[:1],'pt-PT','')
        self.assertEqual(original,repaired);self.assertEqual(call.call_count,2)

    def test_quota_is_distinct_from_rate_limit_without_leaking_server_text(self):
        for code,cause in [('insufficient_quota','quota'),('rate_limit_exceeded','rate_limit')]:
            response=SimpleNamespace(status_code=429,json=lambda:{'error':{'code':code,'message':'private-key'}})
            with patch('requests.post',return_value=response):
                with self.assertRaises(PaidUnavailable) as raised: self.paid.post('/responses',json={})
            self.assertEqual(raised.exception.diagnostic['cause'],cause)
            self.assertNotIn('private',json.dumps(raised.exception.diagnostic))
