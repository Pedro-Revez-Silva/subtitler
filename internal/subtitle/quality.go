package subtitle

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var poisonedTextRE = regexp.MustCompile(`(?i)(\b(?:aac|hevc|web[.-]?dl|web\.hevc|10bit|2160p|1080p|x264|x265)\b|\.com\b|release group|codec names|watermark text|avoid release)`)

type QualityError struct {
	Reason string
	Count  int
	Sample string
}

func (e *QualityError) Error() string {
	switch e.Reason {
	case "poisoned_text":
		return fmt.Sprintf("generated subtitles failed quality check: %d cue(s) contain release, codec, URL, or prompt-leak text", e.Count)
	case "boilerplate":
		return fmt.Sprintf("generated subtitles failed quality check: repeated boilerplate disclaimer appears in %d cues", e.Count)
	case "repeated_long_text":
		return fmt.Sprintf("generated subtitles failed quality check: long cue text repeated %d times: %q", e.Count, e.Sample)
	default:
		return "generated subtitles failed quality check"
	}
}

func AsQualityError(err error) (*QualityError, bool) {
	var qualityErr *QualityError
	if errors.As(err, &qualityErr) {
		return qualityErr, true
	}
	return nil, false
}

func ValidateGenerated(cues []Cue) error {
	if len(cues) == 0 {
		return fmt.Errorf("generated subtitles contain no cues")
	}
	return ValidateCueQuality(cues)
}

func ValidateCueQuality(cues []Cue) error {
	longTextCounts := map[string]int{}
	poisoned := 0
	poisonedSample := ""
	fictionBoilerplate := 0
	for _, cue := range cues {
		text := strings.TrimSpace(cue.Text)
		normalized := normalizeQualityText(text)
		if normalized == "" {
			continue
		}
		if poisonedTextRE.MatchString(normalized) {
			poisoned++
			if poisonedSample == "" {
				poisonedSample = qualitySample(text)
			}
		}
		if strings.Contains(normalized, "work of fiction") {
			fictionBoilerplate++
		}
		if len(normalized) >= 40 {
			longTextCounts[normalized]++
		}
	}

	if poisoned > 0 {
		return &QualityError{Reason: "poisoned_text", Count: poisoned, Sample: poisonedSample}
	}
	if fictionBoilerplate > 1 {
		return &QualityError{Reason: "boilerplate", Count: fictionBoilerplate, Sample: "work of fiction"}
	}
	for text, count := range longTextCounts {
		if count >= 3 {
			return &QualityError{Reason: "repeated_long_text", Count: count, Sample: qualitySample(text)}
		}
	}
	return nil
}

func normalizeQualityText(value string) string {
	value = strings.ToLower(value)
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func qualitySample(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 240 {
		return value
	}
	return strings.TrimSpace(value[:240]) + "..."
}
