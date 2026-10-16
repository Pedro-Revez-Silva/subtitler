"""Exercise the real audio decoder without loading a speech model."""
import math
from pathlib import Path
import struct
import tempfile
import unittest
import wave

from faster_whisper.audio import decode_audio


class AudioDependencyTests(unittest.TestCase):
    def test_faster_whisper_decodes_a_wave_file(self):
        sample_rate = 16000
        sample_count = 1600
        pcm = b''.join(struct.pack('<h', int(12000 * math.sin(2 * math.pi * 440 * i / sample_rate)))
                       for i in range(sample_count))
        with tempfile.TemporaryDirectory() as directory:
            audio = Path(directory) / 'tone.wav'
            with wave.open(str(audio), 'wb') as output:
                output.setnchannels(1)
                output.setsampwidth(2)
                output.setframerate(sample_rate)
                output.writeframes(pcm)
            decoded = decode_audio(str(audio), sampling_rate=sample_rate)

        self.assertEqual(decoded.shape, (sample_count,))
        self.assertEqual(str(decoded.dtype), 'float32')
        self.assertGreater(float(abs(decoded).max()), .3)


if __name__ == '__main__':
    unittest.main()
