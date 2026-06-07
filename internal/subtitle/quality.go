package subtitle

import (
	"fmt"
	"regexp"
	"strings"
)

var poisonedTextRE = regexp.MustCompile(`(?i)(\b(?:aac|hevc|web[.-]?dl|web\.hevc|10bit|2160p|1080p|x264|x265)\b|\.com\b|release group|codec names|watermark text|avoid release)`)

func ValidateGenerated(cues []Cue) error {
	if len(cues) == 0 {
		return fmt.Errorf("generated subtitles contain no cues")
	}
	return ValidateCueQuality(cues)
}

func ValidateCueQuality(cues []Cue) error {
	longTextCounts := map[string]int{}
	poisoned := 0
	fictionBoilerplate := 0
	for _, cue := range cues {
		text := strings.TrimSpace(cue.Text)
		normalized := normalizeQualityText(text)
		if normalized == "" {
			continue
		}
		if poisonedTextRE.MatchString(normalized) {
			poisoned++
		}
		if strings.Contains(normalized, "work of fiction") {
			fictionBoilerplate++
		}
		if len(normalized) >= 40 {
			longTextCounts[normalized]++
		}
	}

	if poisoned > 0 {
		return fmt.Errorf("generated subtitles failed quality check: %d cue(s) contain release, codec, URL, or prompt-leak text", poisoned)
	}
	if fictionBoilerplate > 1 {
		return fmt.Errorf("generated subtitles failed quality check: repeated boilerplate disclaimer appears in %d cues", fictionBoilerplate)
	}
	for text, count := range longTextCounts {
		if count >= 3 {
			return fmt.Errorf("generated subtitles failed quality check: long cue text repeated %d times: %q", count, text)
		}
	}
	return nil
}

func normalizeQualityText(value string) string {
	value = strings.ToLower(value)
	value = strings.Join(strings.Fields(value), " ")
	return value
}
