package subtitle

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateGeneratedRejectsPoisonedReleaseText(t *testing.T) {
	cues := []Cue{
		{StartMS: 1, EndMS: 2, Text: "This is normal dialogue."},
		{StartMS: 3, EndMS: 4, Text: "The.Mummy.2026.2160p.WEB.HEVC.10bit.AAC5.1-LuCY"},
	}

	err := ValidateGenerated(cues)
	if err == nil {
		t.Fatal("expected poisoned release text to be rejected")
	}
	qualityErr, ok := AsQualityError(err)
	if !ok {
		t.Fatalf("expected quality error, got %T", err)
	}
	if qualityErr.Reason != "poisoned_text" || qualityErr.Count != 1 || !strings.Contains(qualityErr.Sample, "2160p") {
		t.Fatalf("unexpected quality error: %#v", qualityErr)
	}
}

func TestValidateGeneratedAllowsLucyAsDialogue(t *testing.T) {
	cues := []Cue{
		{StartMS: 1, EndMS: 2, Text: "Lucy, stay with me."},
	}

	if err := ValidateGenerated(cues); err != nil {
		t.Fatalf("expected Lucy as dialogue to pass, got %v", err)
	}
}

func TestValidateGeneratedRejectsRepeatedBoilerplate(t *testing.T) {
	cues := []Cue{
		{StartMS: 1, EndMS: 2, Text: "This movie is a work of fiction."},
		{StartMS: 3, EndMS: 4, Text: "This movie is a work of fiction."},
	}

	err := ValidateGenerated(cues)
	if err == nil {
		t.Fatal("expected repeated boilerplate to be rejected")
	}
	qualityErr, ok := AsQualityError(err)
	if !ok {
		t.Fatalf("expected quality error, got %T", err)
	}
	if qualityErr.Reason != "boilerplate" || qualityErr.Count != 2 {
		t.Fatalf("unexpected quality error: %#v", qualityErr)
	}
}

func TestValidateGeneratedRejectsRepeatedLongText(t *testing.T) {
	cues := []Cue{
		{StartMS: 1, EndMS: 2, Text: "This same long hallucinated sentence repeats again and again."},
		{StartMS: 3, EndMS: 4, Text: "This same long hallucinated sentence repeats again and again."},
		{StartMS: 5, EndMS: 6, Text: "This same long hallucinated sentence repeats again and again."},
	}

	err := ValidateGenerated(cues)
	if err == nil {
		t.Fatal("expected repeated long text to be rejected")
	}
	qualityErr, ok := AsQualityError(err)
	if !ok {
		t.Fatalf("expected quality error, got %T", err)
	}
	if qualityErr.Reason != "repeated_long_text" || qualityErr.Count != 3 {
		t.Fatalf("unexpected quality error: %#v", qualityErr)
	}
}

func TestValidateGeneratedTruncatesQualitySamples(t *testing.T) {
	longPoisonedText := "2160p " + strings.Repeat("release metadata ", 40)
	err := ValidateGenerated([]Cue{{StartMS: 1, EndMS: 2, Text: longPoisonedText}})
	qualityErr, ok := AsQualityError(err)
	if !ok {
		t.Fatalf("expected quality error, got %T", err)
	}
	if len(qualityErr.Sample) > 243 || !strings.HasSuffix(qualityErr.Sample, "...") {
		t.Fatalf("expected truncated sample, got len=%d sample=%q", len(qualityErr.Sample), qualityErr.Sample)
	}
}

func TestQualityErrorHelpers(t *testing.T) {
	if _, ok := AsQualityError(errors.New("plain")); ok {
		t.Fatal("expected plain error not to be treated as quality error")
	}
	for _, tt := range []struct {
		name   string
		err    *QualityError
		needle string
	}{
		{name: "poisoned", err: &QualityError{Reason: "poisoned_text", Count: 2}, needle: "2 cue(s) contain release"},
		{name: "boilerplate", err: &QualityError{Reason: "boilerplate", Count: 3}, needle: "3 cues"},
		{name: "repeated", err: &QualityError{Reason: "repeated_long_text", Count: 4, Sample: "same line"}, needle: "same line"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); !strings.Contains(got, tt.needle) {
				t.Fatalf("expected %q to contain %q", got, tt.needle)
			}
		})
	}
	if got := (&QualityError{Reason: "unknown"}).Error(); got != "generated subtitles failed quality check" {
		t.Fatalf("unexpected default quality error message %q", got)
	}
}

func TestValidateGeneratedAllowsCommonShortDialogue(t *testing.T) {
	cues := []Cue{
		{StartMS: 1, EndMS: 2, Text: "No."},
		{StartMS: 3, EndMS: 4, Text: "No."},
		{StartMS: 5, EndMS: 6, Text: "No."},
		{StartMS: 7, EndMS: 8, Text: "Katie?"},
	}

	if err := ValidateGenerated(cues); err != nil {
		t.Fatalf("expected common short dialogue to pass, got %v", err)
	}
}

func TestValidateGeneratedRejectsEmptyCueSet(t *testing.T) {
	if err := ValidateGenerated(nil); err == nil {
		t.Fatal("expected empty cue set to be rejected")
	}
}
