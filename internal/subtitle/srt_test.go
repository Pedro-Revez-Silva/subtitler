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
