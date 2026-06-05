package subtitle

import (
	"strings"
	"testing"
)

func TestParseOffsetAndFormatSRT(t *testing.T) {
	input := `1
00:00:01,000 --> 00:00:02,500
Hello.

2
00:00:03,000 --> 00:00:04,000
World.
`

	cues, err := ParseSRT(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(cues))
	}

	output := FormatSRT(Offset(cues, 10_000))
	if !strings.Contains(output, "00:00:11,000 --> 00:00:12,500") {
		t.Fatalf("expected offset timestamp in output:\n%s", output)
	}
	if !strings.Contains(output, "Hello.") || !strings.Contains(output, "World.") {
		t.Fatalf("expected cue text in output:\n%s", output)
	}
}

func TestTrimToDurationDropsAndCapsCues(t *testing.T) {
	cues := []Cue{
		{Index: 1, StartMS: 1_000, EndMS: 2_000, Text: "inside"},
		{Index: 2, StartMS: 9_000, EndMS: 12_000, Text: "capped"},
		{Index: 3, StartMS: 10_000, EndMS: 11_000, Text: "dropped"},
	}

	trimmed := TrimToDuration(cues, 10_000)
	if len(trimmed) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(trimmed))
	}
	if trimmed[1].EndMS != 10_000 {
		t.Fatalf("expected second cue to be capped at duration, got %d", trimmed[1].EndMS)
	}
	if strings.Contains(FormatSRT(trimmed), "dropped") {
		t.Fatal("expected cue starting at duration to be dropped")
	}
}

func TestParseSRTWithoutCueNumbersAndMalformedBlocks(t *testing.T) {
	input := `00:00:01,000 --> 00:00:02,000
No number.
Second line.

not a cue
`
	cues, err := ParseSRT(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 1 || cues[0].Text != "No number.\nSecond line." {
		t.Fatalf("unexpected cues %#v", cues)
	}

	malformed, err := ParseSRT("1\n00:aa:01,000 --> 00:00:02,000\nBad\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(malformed) != 0 {
		t.Fatalf("expected malformed block to be skipped, got %#v", malformed)
	}
}

func TestFormatSRTSortsSkipsInvalidAndClampsNegative(t *testing.T) {
	output := FormatSRT([]Cue{
		{Index: 1, StartMS: 5_000, EndMS: 4_000, Text: "skip"},
		{Index: 2, StartMS: 2_000, EndMS: 3_000, Text: "second"},
		{Index: 3, StartMS: -100, EndMS: 1_000, Text: "first"},
	})
	if strings.Contains(output, "skip") {
		t.Fatalf("expected invalid cue to be skipped:\n%s", output)
	}
	if !strings.Contains(output, "00:00:00,000 --> 00:00:01,000") {
		t.Fatalf("expected negative timestamp clamp:\n%s", output)
	}
	if strings.Index(output, "first") > strings.Index(output, "second") {
		t.Fatalf("expected cues sorted by time:\n%s", output)
	}
}

func TestTrimToDurationNoopsForNonPositiveDuration(t *testing.T) {
	cues := []Cue{{StartMS: 1, EndMS: 2, Text: "kept"}}
	if got := TrimToDuration(cues, 0); len(got) != 1 {
		t.Fatalf("expected no-op trim, got %#v", got)
	}
}
