"""Bounded Blu-ray PGS decoding and local Tesseract OCR.

PGS packet layouts/RLE follow the public format as implemented by FFmpeg's
libavcodec/pgssubdec.c. This parser is independently implemented; it never writes
archive paths or executes subtitle data. OCR retains the original cue timing.
"""
from concurrent.futures import ThreadPoolExecutor
import csv
import hashlib
import io
import os
import statistics
import subprocess
from locales import ocr_language


def integer(data):
    return int.from_bytes(data, "big")


def rle(data, width, height):
    if not 0 < width * height <= 16_000_000:
        raise ValueError("invalid_pgs_dimensions")
    pixels = bytearray()
    cursor = 0
    while cursor < len(data) and len(pixels) < width * height:
        color = data[cursor]
        cursor += 1
        count = 1
        if color == 0:
            flag = data[cursor]
            cursor += 1
            count = flag & 0x3f
            if flag & 0x40:
                count = count * 256 + data[cursor]
                cursor += 1
            color = 0
            if flag & 0x80:
                color = data[cursor]
                cursor += 1
            if count == 0:
                if len(pixels) % width:
                    raise ValueError("invalid_pgs_line")
                continue
        if len(pixels) + count > width * height:
            raise ValueError("pgs_pixel_overflow")
        pixels.extend(bytes([color]) * count)
    if len(pixels) != width * height:
        raise ValueError("truncated_pgs_bitmap")
    return bytes(pixels)


def frames(path):
    from PIL import Image, ImageChops
    objects, palettes, presentation = {}, {}, None
    with open(path, "rb") as handle:
        while header := handle.read(13):
            if len(header) != 13 or header[:2] != b"PG":
                raise ValueError("invalid_pgs_packet")
            timestamp = integer(header[2:6]) / 90000
            kind, length = header[10], integer(header[11:13])
            data = handle.read(length)
            if len(data) != length:
                raise ValueError("truncated_pgs_packet")
            if kind == 0x14:
                palette = palettes.setdefault(data[0], [255] * 256)
                for offset in range(2, len(data), 5):
                    index, lum, cr, cb, alpha = data[offset:offset + 5]
                    palette[index] = 0 if lum >= 96 and alpha >= 96 else 255
            elif kind == 0x15:
                key, flag = integer(data[:2]), data[3]
                if flag & 0x80:
                    expected = integer(data[4:7]) - 4
                    if not 0 <= expected <= 16_000_000:
                        raise ValueError("pgs_object_too_large")
                    objects[key] = {"width": integer(data[7:9]), "height": integer(data[9:11]),
                                    "data": bytearray(data[11:]), "expected": expected}
                elif key in objects:
                    objects[key]["data"].extend(data[4:])
                else:
                    raise ValueError("pgs_missing_fragment")
                if len(objects[key]["data"]) > objects[key]["expected"]:
                    raise ValueError("pgs_fragment_overflow")
            elif kind == 0x16:
                if data[7] & 0x80:  # a new epoch invalidates object/palette caches
                    objects, palettes = {}, {}
                refs, offset = [], 11
                count = data[10]
                if count > 64:
                    raise ValueError("too_many_pgs_objects")
                for _ in range(count):
                    refs.append((integer(data[offset:offset + 2]), integer(data[offset + 4:offset + 6]), integer(data[offset + 6:offset + 8])))
                    if data[offset + 3] & 0x80:
                        raise ValueError("cropped_pgs_needs_review")
                    offset += 8
                presentation = (timestamp, data[9], refs)
            elif kind == 0x80 and presentation:
                timestamp, palette_id, refs = presentation
                if not refs:
                    yield timestamp, None
                    continue
                palette = palettes[palette_id]
                left, top = min(x for _, x, _ in refs), min(y for _, _, y in refs)
                right = max(x + objects[k]["width"] for k, x, _ in refs)
                bottom = max(y + objects[k]["height"] for k, _, y in refs)
                if not 0 < (right - left) * (bottom - top) <= 16_000_000:
                    raise ValueError("pgs_canvas_too_large")
                canvas = Image.new("L", (right - left, bottom - top), 255)
                for key, x, y in refs:
                    obj = objects[key]
                    if len(obj["data"]) != obj["expected"]:
                        raise ValueError("pgs_object_incomplete")
                    image = Image.frombytes("P", (obj["width"], obj["height"]), rle(obj["data"], obj["width"], obj["height"]))
                    image.putpalette([component for value in palette for component in [value, value, value]])
                    canvas.paste(image.convert("L"), (x - left, y - top))
                bounds = ImageChops.invert(canvas).getbbox()
                if bounds:
                    cropped = canvas.crop(bounds)
                    padded = Image.new("L", (cropped.width + 40, cropped.height + 40), 255)
                    padded.paste(cropped, (20, 20))
                    yield timestamp, padded
                else:
                    yield timestamp, None
                if len(objects) > 64 or len(palettes) > 8:
                    raise ValueError("pgs_cache_bounds_exceeded")


def recognize(image, lang):
    payload = io.BytesIO()
    image.save(payload, format="PNG")
    env = dict(os.environ, OMP_THREAD_LIMIT="1")
    result = subprocess.run(["tesseract", "stdin", "stdout", "-l", ocr_language(lang), "--psm", "6", "tsv"],
                            input=payload.getvalue(), capture_output=True, timeout=30, env=env)
    if result.returncode:
        raise ValueError("local_ocr_unavailable")
    lines, confidence = {}, []
    for row in csv.DictReader(io.StringIO(result.stdout.decode()), delimiter="\t"):
        text = row.get("text", "").strip()
        if not text:
            continue
        key = (row["block_num"], row["par_num"], row["line_num"])
        lines.setdefault(key, []).append(text)
        confidence.append(float(row["conf"]))
    return "\\N".join(" ".join(line) for line in lines.values()), statistics.median(confidence) if confidence else 0


def convert(path, output, lang, duration):
    import pysubs2
    cues, confidences = pysubs2.SSAFile(), []
    previous, seen = None, {}
    # A bounded pending queue avoids retaining an entire film's bitmaps.
    pending = []
    with ThreadPoolExecutor(max_workers=3) as pool:
        def flush(entry):
            start, end, future = entry
            text, confidence = future.result() if hasattr(future, "result") else future
            if not text or confidence < 50:
                raise ValueError("low_confidence_ocr")
            confidences.append(confidence)
            if cues and cues[-1].text == text and start * 1000 - cues[-1].end < 200:
                cues[-1].end = round(end * 1000)
            else:
                cues.append(pysubs2.SSAEvent(start=round(start * 1000), end=round(end * 1000), text=text))
        for timestamp, image in frames(path):
            if previous and timestamp > previous[0]:
                start, old = previous
                key = hashlib.sha256(old.tobytes()).hexdigest()
                future = seen.get(key)
                if future is None:
                    future = pool.submit(recognize, old, lang)
                    if len(seen) < 10000:
                        seen[key] = future
                pending.append((start, timestamp, future))
                if len(pending) >= 6:
                    flush(pending.pop(0))
            previous = (timestamp, image) if image else None
        if previous:
            start, old = previous
            pending.append((start, min(duration, start + 10), pool.submit(recognize, old, lang)))
        for entry in pending:
            flush(entry)
    if not confidences or statistics.median(confidences) < 80:
        raise ValueError("low_confidence_ocr")
    cues.save(str(output), format_="srt")
    return {"method": "local_tesseract", "median_confidence": statistics.median(confidences), "cues": len(cues)}
