"""Lazy, fingerprinted media assets. No credentials or acceptance decisions live here."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import uuid


class MediaAssets:
    def __init__(self, manager, path, fp, streams, duration, work):
        self.manager, self.path, self.fp = manager, path, fp
        self.streams, self.duration, self.work = streams, duration, work
        self.root = manager.catalogue.root / 'media-cache'
        self.root.mkdir(exist_ok=True)
        key = hashlib.sha256((path + fp).encode()).hexdigest()
        locks = self.root / 'locks'
        locks.mkdir(exist_ok=True)
        self.directory = self.root / ('data-' + key)
        self.lock = (locks / (key + '.lock')).open('a')
        fcntl.flock(self.lock, fcntl.LOCK_SH)
        self.directory.mkdir(exist_ok=True)
        os.utime(self.directory, None)
        self.trim()

    def close(self):
        self.lock.close()
        self.trim()

    def trim(self):
        # Never remove another worker's live inputs. The bound applies to idle
        # cache; running extraction may temporarily exceed it by its working set.
        maximum = self.manager.cfg.get('media_cache_gb', 5) * 1024**3
        with (self.root / '.gc.lock').open('a') as gc:
            try:
                fcntl.flock(gc, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return
            directories = sorted((d for d in self.root.glob('data-*') if d.is_dir()), key=lambda d:d.stat().st_mtime)
            sizes = {}
            for directory in directories:
                size = 0
                for file in directory.iterdir():
                    try:
                        if file.is_file():
                            size += file.stat().st_size
                    except FileNotFoundError:
                        pass  # Another worker just committed a staged asset.
                sizes[directory] = size
            total = sum(sizes.values())
            for directory in directories:
                if total <= maximum:
                    break
                with (self.root / 'locks' / (directory.name[5:] + '.lock')).open('a') as lock:
                    try:
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    except BlockingIOError:
                        continue
                    shutil.rmtree(directory)
                    total -= sizes[directory]

    def audio(self, index):
        return AudioReference(self, index)

    def subtitle(self, stream):
        from manager import Review
        suffix = '.sup' if stream['codec_name'] == 'hdmv_pgs_subtitle' else '.srt'
        output = self.directory / f"subtitle-{stream['index']}{suffix}"
        if not output.exists():
            self.extract()
        if not output.exists():
            raise Review('empty_media_asset')
        return output

    def extract(self, required_audio=None):
        from manager import TEXT_CODECS, Review, fingerprint, language, matches, run
        manager = self.manager
        wanted = []
        for stream in self.streams:
            tags = stream.get('tags', {})
            if (stream['codec_type'] == 'subtitle' and
                    (stream.get('codec_name') in TEXT_CODECS or stream.get('codec_name') == 'hdmv_pgs_subtitle') and
                    any(matches(language(tags.get('language'), tags.get('title')), lang) for lang in manager.languages) and
                    not re.search(r'commentary|forced|foreign|signs(?: and songs)?', tags.get('title',''), re.I) and
                    not stream.get('disposition', {}).get('forced')):
                suffix = '.sup' if stream['codec_name'] == 'hdmv_pgs_subtitle' else '.srt'
                wanted.append((stream['index'], 'subtitle', suffix))
        # A subtitle extraction already requires a sequential read. Capture all
        # selected dialogue references in that same pass, instead of rereading
        # the video for each language or verification fallback.
        for lang in manager.languages:
            audio = manager.audio(self.streams, lang, language(manager.current_item.get('OriginalLanguage')))
            entry = (audio['index'], 'audio', '.wav')
            if entry not in wanted:
                wanted.append(entry)
        if required_audio is not None and (required_audio, 'audio', '.wav') not in wanted:
            wanted.append((required_audio, 'audio', '.wav'))
        missing = [(index,kind,suffix) for index,kind,suffix in wanted
                   if not (self.directory / f'{kind}-{index}{suffix}').exists()]
        if not missing:
            return
        command = [manager.ffmpeg, '-v', 'error', '-y', '-threads',
                   str(manager.cfg.get('queues', {}).get('worker_threads', 2)), '-i', self.path]
        staged = []
        try:
            for index,kind,suffix in missing:
                output = self.directory / f'{kind}-{index}{suffix}'
                partial = self.directory / (uuid.uuid4().hex + suffix)
                staged.append((partial, output))
                command += ['-map', f'0:{index}']
                command += ['-vn', '-ac', '1', '-ar', '16000', '-c:a', 'pcm_s16le'] if kind == 'audio' else (
                    ['-c:s', 'copy'] if suffix == '.sup' else ['-c:s', 'srt'])
                command.append(str(partial))
            manager.emit(path=self.path, stage='extracting_media_assets', streams=len(missing))
            run(command)
            if fingerprint(self.path) != self.fp:
                raise Review('media_changed_during_work')
            for partial,output in staged:
                # An empty embedded track cannot invalidate other full tracks
                # successfully extracted in this pass. It remains unavailable.
                if partial.is_file() and partial.stat().st_size:
                    os.replace(partial, output)
        finally:
            for partial,_ in staged:
                partial.unlink(missing_ok=True)


class AudioReference:
    def __init__(self, assets, index):
        self.assets, self.index = assets, index
        self.identity = assets.fp + ':' + str(index)

    def __str__(self):
        return self.__fspath__()

    def __fspath__(self):
        from manager import Review
        output = self.assets.directory / f'audio-{self.index}.wav'
        if not output.exists():
            self.assets.extract(self.index)
        if not output.exists():
            raise Review('empty_media_asset')
        return str(output)

    def sample(self, start, seconds, output):
        from manager import run
        cached = self.assets.directory / f'audio-{self.index}.wav'
        source = str(cached) if cached.exists() else self.assets.path
        command = [self.assets.manager.ffmpeg, '-v', 'error', '-y', '-ss', str(start), '-i', source]
        if not cached.exists():
            command += ['-map', f'0:{self.index}']
        run(command + ['-t', str(seconds), '-vn', '-ar', '16000', '-ac', '1', str(output)], 120)

    def word_samples(self, key, create):
        cache = self.assets.directory / ('words-' + hashlib.sha256(json.dumps(key).encode()).hexdigest() + '.json')
        try:
            value = json.loads(cache.read_text())
            if isinstance(value, list):
                return value
        except (OSError, ValueError):
            pass
        value = create()
        partial = cache.with_suffix('.part')
        partial.write_text(json.dumps(value))
        os.replace(partial, cache)
        return value
