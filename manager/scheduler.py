"""Two durable queues with independent worker pools and host-load admission.

Each queue has an exclusive coordinator lock. Jobs for one media path cannot
run in both queues simultaneously. Processes own their SQLite/model instances;
no credentials are persisted in queue rows or passed in command arguments.
"""
from concurrent.futures import Future, ProcessPoolExecutor, wait, FIRST_COMPLETED
import json
import multiprocessing
import os
from pathlib import Path
import signal
import sys
import threading
import time


def prepare(db):
    db.executescript("""
        CREATE TABLE IF NOT EXISTS work_queue(
            path TEXT, kind TEXT, fingerprint TEXT, state TEXT DEFAULT 'queued',
            available REAL DEFAULT 0, priority INTEGER DEFAULT 0,
            PRIMARY KEY(path,kind));
        CREATE INDEX IF NOT EXISTS work_queue_due ON work_queue(kind,state,available);
    """)
    columns = {row[1] for row in db.execute('PRAGMA table_info(work_queue)')}
    for name, declaration in [('resource', "TEXT DEFAULT 'disk'"), ('last_run', 'REAL DEFAULT 0')]:
        if name not in columns:
            try:
                db.execute(f'ALTER TABLE work_queue ADD COLUMN {name} {declaration}')
            except Exception:
                if name not in {row[1] for row in db.execute('PRAGMA table_info(work_queue)')}:
                    raise
    db.execute('CREATE INDEX IF NOT EXISTS work_queue_resources ON work_queue(state,resource)')
    db.commit()


def disk_resource(fp):
    return 'disk:' + fp.split(':', 1)[0]


def enqueue(db, path, fp, kind, available=0, priority=0, resource=None):
    db.execute("INSERT INTO work_queue(path,kind,fingerprint,state,available,priority,resource) VALUES(?,?,?,'queued',?,?,?) "
               "ON CONFLICT(path,kind) DO UPDATE SET fingerprint=excluded.fingerprint,state='queued',"
               "available=excluded.available,priority=excluded.priority,resource=excluded.resource WHERE work_queue.state<>'running'",
               (path, kind, fp, available, priority, resource or disk_resource(fp)))


def claim(db, kind, sample=None, cfg=None):
    try:
        db.execute("BEGIN IMMEDIATE")
        rows = db.execute("SELECT q.path,q.fingerprint,q.resource FROM work_queue q JOIN media m ON m.path=q.path "
                         "WHERE q.kind=? AND q.state='queued' AND q.available<=? AND q.fingerprint=m.fingerprint "
                         "AND NOT EXISTS(SELECT 1 FROM work_queue other WHERE other.path=q.path AND other.state='running') "
                         "AND (q.resource='text' OR NOT EXISTS(SELECT 1 FROM work_queue other WHERE other.resource=q.resource AND other.state='running')) "
                         "ORDER BY q.priority DESC,q.last_run,m.checked,q.path", (kind, time.time())).fetchall()
        row = next((row for row in rows if resource_available(row[2], sample, cfg)), None)
        if row:
            db.execute("UPDATE work_queue SET state='running',last_run=? WHERE path=? AND kind=?", (time.time(),row[0], kind))
        db.commit()
        return row[:2] if row else None
    except BaseException:
        db.rollback()
        raise


def resource_available(resource, sample, cfg):
    if not sample or not cfg or not cfg.get('adaptive', False) or resource == 'text':
        return True
    disk = sample.get('disks', {}).get(resource)
    if disk is None:
        return sample['io_pressure'] < cfg.get('max_io_pressure', 20)
    return (disk['utilization'] < cfg.get('max_disk_utilization', 85) and
            disk['await_ms'] < cfg.get('max_disk_await_ms', 50))


class LoadSensor:
    def __init__(self):
        self.previous_cpu = None
        self.previous_disks = {}
        self.previous_time = None

    def sample(self):
        cpus = os.cpu_count() or 1
        cpu = [int(v) for v in Path('/proc/stat').read_text().splitlines()[0].split()[1:]]
        total, idle = sum(cpu[:8]), cpu[3] + cpu[4]
        busy = 0
        if self.previous_cpu and total > self.previous_cpu[0]:
            busy = 100 * (1 - (idle - self.previous_cpu[1]) / (total - self.previous_cpu[0]))
        self.previous_cpu = total, idle
        mem = {line.split(':')[0]: int(line.split()[1]) * 1024 for line in Path('/proc/meminfo').read_text().splitlines()}
        available = mem['MemAvailable']
        # Respect a container memory ceiling as well as host free memory.
        try:
            maximum = Path('/sys/fs/cgroup/memory.max').read_text().strip()
            if maximum != 'max':
                current = int(Path('/sys/fs/cgroup/memory.current').read_text())
                # Like Docker's working-set metric, exclude reclaimable inactive
                # file cache. Reading a large MKV must not pause the queue forever.
                try:
                    stats = dict(line.split() for line in Path('/sys/fs/cgroup/memory.stat').read_text().splitlines())
                    inactive = int(stats.get('inactive_file', 0))
                except (OSError, ValueError):
                    inactive = 0
                available = min(available, int(maximum) - current + min(current, inactive))
        except (OSError, ValueError):
            pass
        pressure = 0
        try:
            fields = Path('/proc/pressure/io').read_text().splitlines()[0].split()
            pressure = float(next(v.split('=')[1] for v in fields if v.startswith('avg10=')))
        except (OSError, StopIteration):
            pass
        disks, current = {}, {}
        now = time.monotonic()
        try:
            for line in Path('/proc/diskstats').read_text().splitlines():
                fields = line.split()
                key = 'disk:' + str(os.makedev(int(fields[0]), int(fields[1])))
                values = [int(v) for v in fields[3:]]
                current[key] = values
                before = self.previous_disks.get(key)
                if before is not None and self.previous_time is not None and now > self.previous_time:
                    requests = values[0] + values[4] - before[0] - before[4]
                    disks[key] = {'utilization': round(min(100,max(0,(values[9]-before[9])/(now-self.previous_time)/10)),1),
                                  'await_ms': round(max(0,values[3]+values[7]-before[3]-before[7])/max(1,requests),1)}
        except (OSError, ValueError, IndexError):
            pass
        self.previous_disks, self.previous_time = current, now
        return {'cpu_percent': round(busy, 1), 'load_per_cpu': round(os.getloadavg()[0] / cpus, 3),
                'available_memory_gb': round(max(0, available) / 1024**3, 2), 'io_pressure': pressure, 'disks':disks}


def effective_cpus():
    count = len(os.sched_getaffinity(0)) if hasattr(os, 'sched_getaffinity') else os.cpu_count() or 1
    try:
        quota, period = Path('/sys/fs/cgroup/cpu.max').read_text().split()
        if quota != 'max':
            count = min(count, max(1, int(quota) // int(period)))
    except (OSError, ValueError):
        pass
    return count


class Admission:
    def __init__(self, cfg, maximum):
        self.cfg, self.maximum = cfg, maximum
        self.limit, self.idle_samples = 1, 0

    def update(self, sample):
        if not self.cfg.get('adaptive', False):
            self.limit = self.maximum
            return self.limit, 'fixed'
        reason = None
        for key, threshold, reverse in [('cpu_percent', 85, False),
                ('load_per_cpu', self.cfg.get('max_load_per_cpu', .85), False),
                ('available_memory_gb', self.cfg.get('min_memory_gb', 2), True)]:
            # Linux load includes tasks waiting on disks. That must not stop
            # text-only work or jobs on a different physical drive.
            if key == 'load_per_cpu' and sample['cpu_percent'] < 55 and sample['io_pressure'] >= self.cfg.get('max_io_pressure',20):
                continue
            if (sample[key] < threshold if reverse else sample[key] >= threshold):
                reason = key
                break
        if reason:
            self.limit, self.idle_samples = 0, 0
            return 0, reason
        if sample['cpu_percent'] < 55 and (sample['load_per_cpu'] < self.cfg.get('max_load_per_cpu', .85) * .65 or sample['io_pressure'] >= self.cfg.get('max_io_pressure',20)):
            self.idle_samples += 1
            if self.idle_samples >= 3:
                self.limit = min(self.maximum, self.limit + 1)
                self.idle_samples = 0
        else:
            self.idle_samples = 0
            self.limit = max(1, self.limit - 1)
        return self.limit, 'recovering' if not self.limit else 'available'


_worker = None
_print_lock = None


def initialize_worker(request, print_lock):
    global _worker, _print_lock
    from manager import Manager
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda signum, frame: sys.exit(0))
    os.nice(max(0, 10 - os.nice(0)))
    threads = str(request['management'].get('queues', {}).get('worker_threads', 2))
    os.environ['OMP_THREAD_LIMIT'] = threads
    os.environ['OPENBLAS_NUM_THREADS'] = threads
    _print_lock = print_lock
    _worker = Manager(request)


def execute_job(path, fp, kind):
    return _worker.process_job(path, fp, kind)


def emit(record):
    if _print_lock:
        with _print_lock:
            print(json.dumps(record, ensure_ascii=False), flush=True)
    else:
        print(json.dumps(record, ensure_ascii=False), flush=True)


class InlineExecutor:
    def submit(self, function, *args):
        future = Future()
        try:
            future.set_result(function(*args))
        except Exception as error:
            future.set_exception(error)
        return future

    def shutdown(self, **kwargs):
        pass


def drain(manager, kind):
    global _print_lock
    db = manager.catalogue.db
    cfg = manager.cfg.get('queues', {})
    maximum = cfg.get('file_workers' if kind == 'files' else 'transcription_workers', 1)
    maximum = min(maximum, max(1, (effective_cpus() - 1) // cfg.get('worker_threads', 2)))
    poll = cfg.get('poll_seconds', 5)
    admission, sensor = Admission(cfg, maximum), LoadSensor()
    # Exclusive queue lock proves old coordinator/workers are gone. Recover
    # interrupted jobs while retaining their paid cache and original priority.
    db.execute("UPDATE work_queue SET state='queued' WHERE kind=? AND state='running'", (kind,))
    db.execute("UPDATE work_queue SET resource='disk:' || substr(fingerprint,1,instr(fingerprint,':')-1) WHERE resource='disk'")
    db.commit()
    request = {k: v for k, v in manager.request.items() if k != 'items'}
    if maximum == 1:
        # No cross-process semaphore is needed by the inline executor. A
        # multiprocessing lock would start a resource-tracker subprocess for
        # every serial batch, even when there is no eligible work.
        print_lock = threading.Lock()
        executor = InlineExecutor()
    else:
        context = multiprocessing.get_context('spawn')
        print_lock = context.Lock()
        executor = ProcessPoolExecutor(max_workers=maximum, mp_context=context,
                                      initializer=initialize_worker, initargs=(request, print_lock))
    _print_lock = print_lock
    active, started = {}, 0
    cap = manager.request.get('max_jobs', 0)
    if cap <= 0:
        cap = cfg.get('batch_size', 16)
    last_sample, sample, limit, reason = 0, {}, 1, 'initial'
    try:
        if cfg.get('adaptive', False):
            sensor.sample()
            time.sleep(.2)  # First disk sample needs a real counter interval.
        while True:
            now = time.monotonic()
            if now - last_sample >= poll:
                try:
                    sample = sensor.sample()
                    limit, reason = admission.update(sample)
                except (OSError, ValueError, KeyError):
                    limit, reason = 0, 'load_sensor_unavailable'
                last_sample = now
            for future in list(active):
                if future.done():
                    path, fp = active.pop(future)
                    future.result()  # unexpected worker exits fail the cycle; its durable claim survives
                    db.execute("UPDATE work_queue SET state='done' WHERE path=? AND kind=? AND fingerprint=? AND state='running'", (path, kind, fp))
                    db.commit()
            while len(active) < limit and (cap <= 0 or started < cap):
                row = claim(db, kind, sample, cfg)
                if not row:
                    break
                path, fp = row
                started += 1
                manager.emit(status='job_started', queue=kind, path=path)
                # A single generation worker can spend hours in one job. Save
                # its running state before entering the synchronous executor.
                db.execute("INSERT OR REPLACE INTO settings VALUES(?,?)", ('scheduler_' + kind,
                           json.dumps({'queue':kind,'active':len(active)+1,'limit':limit,'maximum':maximum,
                                       'reason':reason,'load':sample,'updated_at':time.time()})))
                db.commit()
                if isinstance(executor, InlineExecutor):
                    future = executor.submit(manager.process_job, path, fp, kind)
                else:
                    future = executor.submit(execute_job, path, fp, kind)
                active[future] = path, fp
            if not active and limit and started < cap and db.execute("SELECT 1 FROM work_queue WHERE kind=? AND state='queued' AND available<=?",(kind,time.time())).fetchone():
                reason = 'disk_busy_or_locked'
            state = {'queue': kind, 'active': len(active), 'limit': limit, 'maximum': maximum,
                     'reason': reason, 'load': sample, 'updated_at': time.time()}
            db.execute("INSERT OR REPLACE INTO settings VALUES(?,?)", ('scheduler_' + kind, json.dumps(state)))
            db.commit()
            if not active:
                # Return under sustained pressure and let the daemon retry;
                # never turn a capacity pause into a failed subtitle.
                if limit == 0 or reason == 'disk_busy_or_locked':
                    manager.emit(status='queue_paused', **state)
                break
            wait(active, timeout=poll, return_when=FIRST_COMPLETED)
        manager.emit(status='queue_finished', queue=kind, jobs=started)
    finally:
        executor.shutdown(wait=True, cancel_futures=True)
        db.execute("INSERT OR REPLACE INTO settings VALUES(?,?)", ('scheduler_' + kind,
                   json.dumps({'queue':kind,'active':0,'limit':limit,'maximum':maximum,'reason':reason,
                               'load':sample,'updated_at':time.time()})))
        db.commit()
