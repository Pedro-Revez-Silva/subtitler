package subtitle

import "testing"

func TestValidateGeneratedRejectsPoisonedReleaseText(t *testing.T) {
	cues := []Cue{
		{StartMS: 1, EndMS: 2, Text: "This is normal dialogue."},
		{StartMS: 3, EndMS: 4, Text: "The.Mummy.2026.2160p.WEB.HEVC.10bit.AAC5.1-LuCY"},
	}

	if err := ValidateGenerated(cues); err == nil {
		t.Fatal("expected poisoned release text to be rejected")
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

	if err := ValidateGenerated(cues); err == nil {
		t.Fatal("expected repeated boilerplate to be rejected")
	}
}

func TestValidateGeneratedRejectsRepeatedLongText(t *testing.T) {
	cues := []Cue{
		{StartMS: 1, EndMS: 2, Text: "This same long hallucinated sentence repeats again and again."},
		{StartMS: 3, EndMS: 4, Text: "This same long hallucinated sentence repeats again and again."},
		{StartMS: 5, EndMS: 6, Text: "This same long hallucinated sentence repeats again and again."},
	}

	if err := ValidateGenerated(cues); err == nil {
		t.Fatal("expected repeated long text to be rejected")
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
