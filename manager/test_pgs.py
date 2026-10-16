import io
from pathlib import Path
import struct
import tempfile
import unittest
from unittest.mock import patch

from pgs import convert, frames, rle


class PGSTests(unittest.TestCase):
    def test_colored_and_transparent_runs_and_line_end(self):
        self.assertEqual(rle(bytes([1, 0, 0x82, 2, 0, 1, 0, 0]), 4, 1), bytes([1, 2, 2, 0]))

    def test_truncation_and_dimension_limits(self):
        with self.assertRaises(ValueError):
            rle(b"\1", 2, 2)
        with self.assertRaises(ValueError):
            rle(b"\1", 100000, 100000)
        with self.assertRaises(ValueError):
            rle(bytes([0, 0x85, 1]), 4, 1)

    def test_packets_have_bounded_lengths_and_palettes(self):
        def packet(kind, payload, seconds):
            return b"PG" + struct.pack(">IIBH", int(seconds * 90000), 0, kind, len(payload)) + payload
        pcs = b"\0\x20\0\x20\0\0\1\x80\0\0\1" + b"\0\1\0\0\0\0\0\0"
        palette = bytes([0, 0, 0, 0, 128, 128, 0, 1, 255, 128, 128, 255])
        obj = b"\0\1\0\xc0\0\0\x08\0\2\0\2" + bytes([1, 1, 1, 1])
        clear = b"\0\x20\0\x20\0\0\2\0\0\0\0"
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "sub.sup"
            path.write_bytes(packet(0x16, pcs, 1) + packet(0x14, palette, 1) + packet(0x15, obj, 1) + packet(0x80, b"", 1)
                             + packet(0x16, clear, 3) + packet(0x80, b"", 3))
            result = list(frames(path))
            self.assertEqual([t for t, _ in result], [1, 3])
            self.assertIsNotNone(result[0][1])
            self.assertIsNone(result[1][1])


if __name__ == "__main__":
    unittest.main()
