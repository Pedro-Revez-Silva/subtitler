"""Queue isolation, backpressure, restart, and simultaneous-worker regressions."""
from concurrent.futures import ThreadPoolExecutor
import fcntl
import hashlib
import json
from pathlib import Path
import sqlite3
import threading
import time
import unittest
from unittest.mock import patch

from manager import Manager, canonical, digest, fingerprint
from paid import Paid, PaidUnavailable
from scheduler import Admission, LoadSensor, claim, drain, enqueue
import test_manager as fixtures


class QueueTests(unittest.TestCase):
    setUp = fixtures.OwnershipTests.setUp
    register = fixtures.OwnershipTests.register

    def local_patches(self):
        from contextlib import ExitStack
        stack = ExitStack()
        audio = {'codec_type':'audio','index':1,'tags':{'language':'eng'}}
        stack.enter_context(patch.object(self.manager, 'probe', return_value=([audio],120)))
        stack.enter_context(patch.object(self.manager, 'cleanup_sidecars'))
        stack.enter_context(patch.object(self.manager, 'prune', side_effect=lambda item,fp,*args:(fp,{'status':'disabled'})))
        stack.enter_context(patch.object(self.manager, 'emit'))
        stack.enter_context(patch('manager.run', return_value=b''))
        return stack

    def test_file_queue_searches_all_languages_then_persists_generation(self):
        self.request['action'] = 'files'
        self.manager.cfg['local_transcription'] = True
        events = []
        def ensure(*args):
            events.append(('search', args[4]))
            return {'providers':{'test':'searched'}}
        with self.local_patches(), patch.object(self.manager,'ensure',side_effect=ensure), \
             patch.object(self.manager,'fallback',side_effect=AssertionError('file queue attempted generation')):
            self.manager.scan()
        db = self.manager.catalogue.db
        self.assertEqual(events,[('search','en'),('search','pt-PT')])
        self.assertEqual(db.execute('SELECT status FROM media').fetchone()[0],'queued_transcription')
        self.assertEqual(db.execute("SELECT state FROM work_queue WHERE kind='transcription'").fetchone()[0],'queued')
        # Reopening the catalogue retains the pending job; no temporary audio or key is stored.
        other = Manager(self.request)
        self.addCleanup(other.catalogue.db.close)
        self.assertEqual(other.catalogue.db.execute("SELECT count(*) FROM work_queue WHERE kind='transcription' AND state='queued'").fetchone()[0],1)
        with self.local_patches(), patch.object(self.manager,'fallback',return_value={'output':'accepted.srt'}):
            self.manager.transcription()
        self.assertEqual(db.execute('SELECT status FROM media').fetchone()[0],'managed')

    def test_budget_wait_stays_in_generation_queue_without_blocking_files(self):
        self.request.update(action='files',max_jobs=2)
        self.manager.cfg['paid_fallback'] = True
        second = self.root/'Other.mkv'; second.write_bytes(b'other'); second.touch()
        self.request['items'].append({**self.item,'Path':str(second),'Source':'manual'})
        with self.local_patches(), patch.object(self.manager,'ensure',return_value={'providers':{'test':'searched'}}):
            self.manager.scan()
        db=self.manager.catalogue.db
        self.assertEqual(db.execute("SELECT count(*) FROM work_queue WHERE kind='files' AND state='done'").fetchone()[0],2)
        with self.local_patches(), patch.object(self.manager,'fallback',return_value={
                'providers':{'test':'searched'},'paid_failure':'daily_paid_budget_exhausted'}):
            self.manager.transcription()
        rows=db.execute("SELECT state,available FROM work_queue WHERE kind='transcription'").fetchall()
        self.assertEqual(len(rows),2)
        self.assertTrue(all(state=='queued' and available>time.time() for state,available in rows))

    def test_uncertain_provider_candidates_never_enter_generation_queue(self):
        self.request['action']='files'; self.manager.cfg['paid_fallback']=True
        with self.local_patches(), patch.object(self.manager,'ensure',return_value={
                'providers':{'test':'searched'},'failures':['word_verification_inconclusive']}):
            self.manager.scan()
        self.assertEqual(self.manager.catalogue.db.execute("SELECT count(*) FROM work_queue WHERE kind='transcription'").fetchone()[0],0)

    def test_claims_exclude_the_same_path_in_the_other_queue(self):
        self.register(); db=self.manager.catalogue.db
        for kind in ['files','transcription']: enqueue(db,str(self.video),self.fp,kind)
        db.commit()
        self.assertIsNotNone(claim(db,'files'))
        self.assertIsNone(claim(db,'transcription'))
        db.execute("UPDATE work_queue SET state='done' WHERE kind='files'"); db.commit()
        self.assertIsNotNone(claim(db,'transcription'))

    def test_orphan_worker_media_lock_prevents_duplicate_publication(self):
        self.register(); db=self.manager.catalogue.db
        enqueue(db,str(self.video),self.fp,'files'); db.commit(); claim(db,'files')
        locks=self.manager.catalogue.root/'jobs'; locks.mkdir()
        with (locks/(hashlib.sha256(str(self.video).encode()).hexdigest()+'.lock')).open('a') as lock:
            fcntl.flock(lock,fcntl.LOCK_EX)
            with patch.object(self.manager,'process_job_locked',side_effect=AssertionError('duplicate worker')):
                self.manager.process_job(str(self.video),self.fp,'files')
        state,available=db.execute('SELECT state,available FROM work_queue').fetchone()
        self.assertEqual(state,'queued'); self.assertGreater(available,time.time())

    def test_interrupted_queue_claim_is_recovered(self):
        self.register(); db=self.manager.catalogue.db
        enqueue(db,str(self.video),self.fp,'files'); db.commit(); claim(db,'files')
        with patch.object(self.manager,'process_job') as job, patch.object(self.manager,'emit'):
            drain(self.manager,'files')
        job.assert_called_once_with(str(self.video),self.fp,'files')
        self.assertEqual(db.execute('SELECT state FROM work_queue').fetchone()[0],'done')

    def test_sustained_load_leaves_the_job_queued(self):
        self.register(); db=self.manager.catalogue.db
        enqueue(db,str(self.video),self.fp,'files'); db.commit()
        self.manager.cfg['queues']={'adaptive':True}
        sample={'cpu_percent':95,'load_per_cpu':.2,'available_memory_gb':8,'io_pressure':0}
        with patch('scheduler.LoadSensor.sample',return_value=sample), patch.object(self.manager,'process_job') as job, patch.object(self.manager,'emit'):
            drain(self.manager,'files')
        job.assert_not_called()
        self.assertEqual(db.execute('SELECT state FROM work_queue').fetchone()[0],'queued')

    def test_generation_reports_active_work_before_it_finishes(self):
        self.register(); db=self.manager.catalogue.db
        enqueue(db,str(self.video),self.fp,'transcription'); db.commit()
        def job(*args):
            raw=db.execute("SELECT value FROM settings WHERE key='scheduler_transcription'").fetchone()[0]
            self.assertEqual(json.loads(raw)['active'],1)
        with patch.object(self.manager,'process_job',side_effect=job), patch.object(self.manager,'emit'):
            drain(self.manager,'transcription')
        self.assertEqual(json.loads(db.execute("SELECT value FROM settings WHERE key='scheduler_transcription'").fetchone()[0])['active'],0)

    def test_stale_provider_proof_requeues_files_before_spending(self):
        self.register(); db=self.manager.catalogue.db
        # Recent generation retries must not make an old provider search fresh.
        db.execute('UPDATE media SET checked=?,detail=?',
                   (time.time(),json.dumps({'sources_checked_at':time.time()-2*86400})))
        enqueue(db,str(self.video),self.fp,'transcription'); db.commit()
        with patch.object(self.manager,'fallback',side_effect=AssertionError('stale search authorized spending')):
            self.manager.process_job(str(self.video),self.fp,'transcription')
        self.assertEqual(db.execute("SELECT state FROM work_queue WHERE kind='files'").fetchone()[0],'queued')

    def test_two_real_processes_work_concurrently_and_preserve_the_catalogue(self):
        self.request.update(action='files',languages=['en'],max_jobs=2)
        self.manager.languages=['en']
        self.manager.cfg['queues']={'file_workers':2,'transcription_workers':1,'adaptive':False,'poll_seconds':1,'worker_threads':1}
        markers=self.root/'markers'; markers.mkdir()
        probe=self.root/'probe.py'
        probe.write_text('#!/usr/bin/env python3\nimport json,sys,time\nfrom pathlib import Path\n'
                         + 'root=Path('+repr(str(markers))+')\n(root/Path(sys.argv[-1]).name).touch()\n'
                         + 'deadline=time.monotonic()+5\nwhile len(list(root.iterdir()))<2 and time.monotonic()<deadline: time.sleep(.02)\n'
                         + 'print(json.dumps({"streams":[{"codec_type":"audio","index":1,"codec_name":"ac3","tags":{"language":"eng"}}],"format":{"duration":"120"}}))\n')
        probe.chmod(0o755); self.request['ffprobe']=str(probe); self.manager.ffprobe=str(probe)
        second=self.root/'Other.mkv'; second.write_bytes(b'other')
        self.request['items']=[{**self.item,'Source':'manual'}, {**self.item,'Path':str(second),'Source':'manual'}]
        for item in self.request['items']:
            output=Path(canonical(item['Path'],'en')); output.write_text('accepted caption')
            self.manager.catalogue.record(item['Path'],fingerprint(item['Path']),'en',digest(output),{'output':str(output),'audio_stream':1})
        with patch('scheduler.effective_cpus',return_value=4):
            self.manager.scan()
        self.assertEqual(len(list(markers.iterdir())),2)
        self.assertEqual(self.manager.catalogue.db.execute("SELECT count(*) FROM media WHERE status='managed'").fetchone()[0],2)


class AdmissionTests(unittest.TestCase):
    def test_reclaimable_video_cache_is_not_treated_as_exhausted_memory(self):
        gib=1024**3
        files={'/proc/stat':'cpu 1 0 0 10 0 0 0 0\n',
               '/proc/meminfo':f'MemAvailable: {8*gib//1024} kB\n',
               '/sys/fs/cgroup/memory.max':str(6*gib),
               '/sys/fs/cgroup/memory.current':str(5*gib),
               '/sys/fs/cgroup/memory.stat':f'inactive_file {3*gib}\n',
               '/proc/pressure/io':'some avg10=0.00 avg60=0.00\n'}
        with patch.object(Path,'read_text',lambda path:files[str(path)]), \
             patch('scheduler.os.getloadavg',return_value=(.1,.1,.1)), patch('scheduler.os.cpu_count',return_value=4):
            sample=LoadSensor().sample()
        self.assertEqual(sample['available_memory_gb'],4)
        self.assertEqual(Admission({'adaptive':True},3).update(sample)[0],1)

    def test_idle_capacity_ramps_and_each_pressure_signal_stops_admission(self):
        gate=Admission({'adaptive':True},4)
        idle={'cpu_percent':10,'load_per_cpu':.1,'available_memory_gb':8,'io_pressure':0}
        for _ in range(3): gate.update(idle)
        self.assertEqual(gate.limit,2)
        for key,value in [('cpu_percent',90),('load_per_cpu',1),('available_memory_gb',1),('io_pressure',30)]:
            self.assertEqual(gate.update({**idle,key:value})[0],0)
        for _ in range(3): gate.update(idle)
        self.assertEqual(gate.limit,1)
        self.assertEqual(gate.update({**idle,'cpu_percent':65})[0],1)


class ConcurrentBudgetTests(unittest.TestCase):
    def test_simultaneous_workers_cannot_overspend(self):
        import tempfile
        with tempfile.TemporaryDirectory() as root:
            path=str(Path(root)/'budget.sqlite'); barrier=threading.Barrier(2)
            def reserve(_):
                db=sqlite3.connect(path,timeout=10)
                try:
                    paid=Paid(db,{'api_key':'unit-test-placeholder'},1)
                    barrier.wait(timeout=5)
                    try: paid.reserve(.6,'translate'); return 'reserved'
                    except PaidUnavailable: return 'blocked'
                finally: db.close()
            with ThreadPoolExecutor(max_workers=2) as pool: outcomes=list(pool.map(reserve,range(2)))
            self.assertEqual(sorted(outcomes),['blocked','reserved'])
            with sqlite3.connect(path) as db:
                self.assertAlmostEqual(db.execute('SELECT sum(reserved) FROM paid_ledger').fetchone()[0],.6)

