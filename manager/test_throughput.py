"""Bounded media reads, resource isolation, fair resumption, and deferred cleanup."""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from manager import Manager, Review, fingerprint, digest, canonical
from mediawork import MediaAssets
from paid import Paid, PaidUnavailable
from scheduler import Admission, LoadSensor, claim, enqueue, prepare
import test_manager as fixtures


class ThroughputTests(unittest.TestCase):
    setUp = fixtures.OwnershipTests.setUp
    register = fixtures.OwnershipTests.register
    caption = fixtures.OwnershipTests.caption

    def manager_assets(self, streams, duration=120):
        self.manager.current_item = self.item
        assets = MediaAssets(self.manager,str(self.video),fingerprint(self.video),streams,duration,self.root)
        self.addCleanup(assets.close)
        return assets

    @unittest.skipUnless(fixtures.shutil.which('ffmpeg'), 'ffmpeg required')
    def test_one_pass_extracts_both_subtitles_and_audio_and_survives_restart(self):
        subs=[]
        for lang in ['en','pt']:
            p=self.root/(lang+'.srt'); p.write_text('1\n00:00:00,100 --> 00:00:01,500\nCaption\n'); subs.append(p)
        subprocess.run(['ffmpeg','-v','error','-y','-f','lavfi','-i','color=c=black:s=32x32:d=2',
                        '-f','lavfi','-i','anullsrc=r=16000:cl=mono','-i',str(subs[0]),'-i',str(subs[1]),
                        '-t','2','-map','0:v','-map','1:a','-map','2:s','-map','3:s',
                        '-c:v','ffv1','-c:a','pcm_s16le','-c:s','srt','-metadata:s:a:0','language=eng',
                        '-metadata:s:s:0','language=eng','-metadata:s:s:1','language=por',
                        '-metadata:s:s:1','title=Portuguese Portugal',str(self.video)],check=True)
        streams,duration=self.manager.probe(str(self.video))
        assets=self.manager_assets(streams,duration)
        from manager import run
        with patch('manager.run',wraps=run) as call:
            first=assets.subtitle(streams[2]); second=assets.subtitle(streams[3]); audio=Path(assets.audio(1))
        self.assertEqual(call.call_count,1)
        self.assertTrue(all(p.is_file() for p in [first,second,audio]))
        self.assertEqual(call.call_args.args[0].count('-i'),1)
        # A fresh manager reuses the committed assets without rereading video.
        reopened=self.manager_assets(streams,duration)
        with patch('manager.run',side_effect=AssertionError('repeated movie read')):
            self.assertEqual(reopened.subtitle(streams[2]),first)
            self.assertEqual(Path(reopened.audio(1)),audio)
        self.video.write_bytes(b'new import')
        changed=self.manager_assets(streams,duration)
        self.assertNotEqual(changed.directory,assets.directory)

    def test_word_checks_seek_short_samples_and_persist_observations(self):
        streams=[{'codec_type':'audio','index':1,'tags':{'language':'eng'}}]
        assets=self.manager_assets(streams)
        model=SimpleNamespace(transcribe=lambda *a,**k:([],None))
        with patch('manager.run',return_value=b'') as run, patch.object(self.manager,'speech_model',return_value=model), \
             patch('manager.word_metrics',return_value={'status':'verified'}):
            self.manager.verify_words(assets.audio(1),fixtures.dialogue(),'en',self.root)
        self.assertEqual(run.call_count,3)
        for call in run.call_args_list:
            command=call.args[0]
            self.assertLess(command.index('-ss'),command.index('-i'))
            self.assertIn(str(self.video),command)
            self.assertIn('0:1',command)
            self.assertEqual(command[command.index('-t')+1],'60')
        self.assertFalse((assets.directory/'audio-1.wav').exists())
        self.manager.word_samples.clear()
        with patch('manager.run',side_effect=AssertionError('samples reread')), \
             patch.object(self.manager,'speech_model',side_effect=AssertionError('model repeated')), \
             patch('manager.word_metrics',return_value={'status':'verified'}):
            self.assertEqual(self.manager.verify_words(assets.audio(1),fixtures.dialogue(),'en',self.root)['status'],'verified_samples')

    def test_cache_eviction_preserves_a_live_workers_inputs(self):
        self.manager.cfg['media_cache_gb']=.000001
        first=self.manager_assets([{'codec_type':'audio','index':1,'tags':{'language':'eng'}}])
        payload=first.directory/'audio-1.wav'; payload.write_bytes(b'x'*4096)
        first.trim()
        self.assertTrue(payload.exists())
        first.close()
        self.assertFalse(payload.exists())

    def generation(self, current, fallback):
        self.register(); self.caption()
        db=self.manager.catalogue.db
        self.manager.cfg.update(paid_fallback=True,paid_daily_usd=2,local_transcription=False)
        detail={'en':{'output':str(canonical(self.video,'en'))},'pt-PT':current,'sources_checked_at':time.time()}
        db.execute('UPDATE media SET checked=?,detail=?',(time.time(),json.dumps(detail)))
        enqueue(db,str(self.video),self.fp,'transcription'); db.commit()
        streams=[{'codec_type':'audio','index':1,'tags':{'language':'eng'}}]
        with patch.object(self.manager,'probe',return_value=(streams,120)), patch.object(self.manager,'fallback',side_effect=fallback), \
             patch.object(self.manager,'emit'), patch('manager.run',side_effect=AssertionError('unnecessary movie/audio read')):
            self.manager.process_job(str(self.video),self.fp,'transcription')
        return db

    def test_translation_yields_promptly_and_is_reclassified_as_text(self):
        def fallback(item,fp,streams,duration,lang,detail,work):
            return detail[lang] if 'output' in detail[lang] else {
                'providers':{'test':'searched'},'translation_failure':'translation_call_limit'}
        db=self.generation({'providers':{'test':'searched'}},fallback)
        state,available,resource=db.execute("SELECT state,available,resource FROM work_queue WHERE kind='transcription'").fetchone()
        self.assertEqual((state,resource),('queued','text'))
        self.assertLess(available-time.time(),6)

    def test_exhausted_translation_budget_does_not_extract_audio(self):
        self.request['openai']={'api_key':'unit-test-placeholder','translation_model':'gpt-4o-mini'}
        def fallback(*args):
            return Manager.fallback(self.manager,*args)
        paid=Paid(self.manager.catalogue.db,self.request['openai'],2)
        paid.reserve(2,'translate')
        db=self.generation({'providers':{'test':'searched'}},fallback)
        detail=json.loads(db.execute('SELECT detail FROM media').fetchone()[0])
        self.assertEqual(detail['pt-PT']['translation_failure'],'daily_paid_budget_exhausted')
        self.assertEqual(detail['recovery']['action'],'wait_for_daily_budget')
        self.assertEqual(db.execute('SELECT sum(reserved) FROM paid_ledger').fetchone()[0],2)

    def test_successful_text_translation_keeps_the_clock_without_reading_movie(self):
        self.request['openai']={'api_key':'unit-test-placeholder','translation_model':'gpt-4o-mini'}
        def post(endpoint,**kwargs):
            cues=json.loads(kwargs['json']['input'].split('\nInput: ',1)[1])
            return SimpleNamespace(json=lambda:{'status':'completed','output':[{'content':[{'type':'output_text',
                'text':json.dumps({'translations':[{'id':c['id'],'text':f"Olá meu amigo número {c['id']}, amanhã vamos visitar o jardim com a nossa família."} for c in cues]})}]}]})
        with patch('paid.Paid.post',side_effect=post):
            db=self.generation({'providers':{'test':'searched'}},lambda *args:Manager.fallback(self.manager,*args))
        detail=json.loads(db.execute('SELECT detail FROM media').fetchone()[0])
        self.assertEqual(detail['pt-PT']['timing']['method'],'inherited_accepted_source_timestamps')
        self.assertTrue(Path(canonical(self.video,'pt-PT')).is_file())
        self.assertEqual(db.execute('SELECT status FROM media').fetchone()[0],'managed')

    def test_exhausted_upload_budget_is_checked_before_preparing_audio(self):
        self.manager.languages=['en']
        self.manager.cfg.update(paid_fallback=True,paid_daily_usd=2,local_transcription=False)
        self.request['openai']={'api_key':'unit-test-placeholder','transcription_model':'whisper-1'}
        Paid(self.manager.catalogue.db,self.request['openai'],2).reserve(1.999,'translate')
        streams=[{'codec_type':'audio','index':1,'tags':{'language':'eng'}}]
        with patch('manager.run',side_effect=AssertionError('upload prepared without budget')):
            result=self.manager.fallback(self.item,self.fp,streams,120,'en',{'en':{'providers':{'test':'searched'}}},self.root)
        if self.manager.assets is not None:self.manager.assets.close()
        self.assertEqual(result['paid_failure'],'daily_paid_budget_exhausted')

    def test_changed_source_audio_returns_to_files_without_searching_in_generation(self):
        self.register(); output=self.caption()
        data=self.manager.catalogue.caption(str(self.video),self.fp,'en')[1]
        data['audio_stream']=99
        self.manager.catalogue.record(str(self.video),self.fp,'en',digest(output),data)
        db=self.manager.catalogue.db
        detail={'en':data,'pt-PT':{'providers':{'test':'searched'}},'sources_checked_at':time.time()}
        db.execute('UPDATE media SET checked=?,detail=?',(time.time(),json.dumps(detail)))
        enqueue(db,str(self.video),self.fp,'transcription',resource='text'); db.commit()
        audio={'codec_type':'audio','index':1,'tags':{'language':'eng'}}
        with patch.object(self.manager,'probe',return_value=([audio],120)), \
             patch.object(self.manager,'ensure',side_effect=AssertionError('generation acquired subtitles')), \
             patch.object(self.manager,'fallback',side_effect=AssertionError('stale source authorized generation')):
            self.manager.process_job(str(self.video),self.fp,'transcription')
        self.assertEqual(db.execute("SELECT priority FROM work_queue WHERE kind='files'").fetchone()[0],2)

    def test_first_pass_queues_cleanup_instead_of_rewriting_movie(self):
        self.register(); self.caption('en'); self.caption('pt-PT')
        self.manager.cfg['prune_embedded']=True
        audio={'codec_type':'audio','index':1,'tags':{'language':'eng'}}
        unwanted={'codec_type':'subtitle','index':2,'tags':{'language':'tur'}}
        db=self.manager.catalogue.db
        enqueue(db,str(self.video),self.fp,'files'); db.commit(); claim(db,'files')
        with patch.object(self.manager,'probe',return_value=([audio,unwanted],120)), \
             patch.object(self.manager,'remux',side_effect=AssertionError('acquisition rewrote movie')),patch.object(self.manager,'emit'):
            self.manager.process_job(str(self.video),self.fp,'files')
        self.assertEqual(db.execute('SELECT status FROM media').fetchone()[0],'managed_cleanup_pending')
        self.assertEqual(db.execute('SELECT state,priority FROM work_queue').fetchone(),('queued',-1))
        other=self.root/'Other.mkv'; other.write_bytes(b'other'); fp=fingerprint(other)
        db.execute('INSERT INTO media(path,fingerprint,item) VALUES(?,?,?)',(str(other),fp,'{}'))
        enqueue(db,str(other),fp,'files'); db.execute('UPDATE work_queue SET available=0'); db.commit()
        self.assertEqual(claim(db,'files')[0],str(other))

    def test_paid_upload_preflight_retains_reservations(self):
        paid=Paid(self.manager.catalogue.db,{'api_key':'unit-test-placeholder'},2)
        paid.reserve(1.9,'translate')
        with self.assertRaisesRegex(PaidUnavailable,'daily_paid_budget_exhausted'):
            paid.preflight(.2)
        self.assertAlmostEqual(paid.db.execute('SELECT sum(reserved) FROM paid_ledger').fetchone()[0],1.9)


class ResourceTests(unittest.TestCase):
    def db(self):
        db=sqlite3.connect(':memory:'); self.addCleanup(db.close)
        db.execute('CREATE TABLE media(path TEXT PRIMARY KEY,fingerprint TEXT,checked REAL)')
        prepare(db)
        return db

    def enqueue(self,db,path,resource,kind='files'):
        db.execute('INSERT INTO media VALUES(?,?,0)',(path,'1:2:3:4'))
        enqueue(db,path,'1:2:3:4',kind,resource=resource); db.commit()

    def test_busy_disk_does_not_block_other_drives_or_text(self):
        db=self.db()
        self.enqueue(db,'a','disk:1'); self.enqueue(db,'b','disk:2'); self.enqueue(db,'c','text','transcription')
        sample={'io_pressure':80,'disks':{'disk:1':{'utilization':99,'await_ms':100},'disk:2':{'utilization':1,'await_ms':1}}}
        cfg={'adaptive':True}
        self.assertEqual(claim(db,'files',sample,cfg)[0],'b')
        self.assertEqual(claim(db,'transcription',sample,cfg)[0],'c')
        self.assertIsNone(claim(db,'files',sample,cfg))
        gate=Admission(cfg,3)
        idle={'cpu_percent':10,'load_per_cpu':2,'available_memory_gb':5,'io_pressure':80}
        for _ in range(3):gate.update(idle)
        self.assertEqual(gate.limit,2)

    def test_same_disk_is_serialized_across_queues_but_resumption_is_fair(self):
        db=self.db()
        self.enqueue(db,'a','disk:1'); self.enqueue(db,'b','disk:1','transcription')
        self.enqueue(db,'c','text','transcription')
        self.assertEqual(claim(db,'files')[0],'a')
        self.assertEqual(claim(db,'transcription')[0],'c')
        self.assertIsNone(claim(db,'transcription'))
        db.execute("UPDATE work_queue SET state='done' WHERE path='a'")
        db.execute("UPDATE work_queue SET state='queued' WHERE path='c'"); db.commit()
        self.assertEqual(claim(db,'transcription')[0],'b')

    def test_disk_counters_measure_utilization_and_latency(self):
        sensor=LoadSensor()
        files={'/proc/stat':'cpu 1 0 0 10 0 0 0 0\n','/proc/meminfo':'MemAvailable: 8000000 kB\n',
               '/sys/fs/cgroup/memory.max':'max','/proc/pressure/io':'some avg10=80.00\n',
               '/proc/diskstats':'8 1 sda1 10 0 0 20 0 0 0 0 0 50 0\n'}
        with patch.object(Path,'read_text',lambda p:files[str(p)]),patch('scheduler.time.monotonic',side_effect=[1,2]):
            sensor.sample()
            files['/proc/diskstats']='8 1 sda1 20 0 0 40 0 0 0 0 0 950 0\n'
            sample=sensor.sample()
        self.assertEqual(sample['disks']['disk:'+str(os.makedev(8,1))],{'utilization':90,'await_ms':2})

    def test_old_queue_migration_preserves_claim_and_fingerprint(self):
        db=sqlite3.connect(':memory:'); self.addCleanup(db.close)
        db.execute("CREATE TABLE work_queue(path TEXT,kind TEXT,fingerprint TEXT,state TEXT,available REAL,priority INTEGER,PRIMARY KEY(path,kind))")
        db.execute("INSERT INTO work_queue VALUES('movie','files','1:2:3:4','running',123,2)")
        db.commit(); prepare(db)
        self.assertEqual(db.execute('SELECT fingerprint,state,available,priority FROM work_queue').fetchone(),('1:2:3:4','running',123,2))
