package subtitle

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Cue struct {
	Index   int
	StartMS int
	EndMS   int
	Text    string
}

var timingRE = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2}),(\d{3})\s+-->\s+(\d{2}):(\d{2}):(\d{2}),(\d{3})`)

func ParseSRT(data string) ([]Cue, error) {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	blocks := strings.Split(data, "\n\n")
	cues := []Cue{}
	for _, block := range blocks {
		lines := nonEmptyLines(block)
		if len(lines) < 3 {
			continue
		}
		timingLine := 1
		if timingRE.MatchString(lines[0]) {
			timingLine = 0
		}
		if timingLine >= len(lines) {
			continue
		}
		match := timingRE.FindStringSubmatch(lines[timingLine])
		if match == nil {
			continue
		}
		start, err := parseTimestamp(match[1:5])
		if err != nil {
			return nil, err
		}
		end, err := parseTimestamp(match[5:9])
		if err != nil {
			return nil, err
		}
		textStart := timingLine + 1
		if textStart >= len(lines) {
			continue
		}
		cues = append(cues, Cue{
			Index:   len(cues) + 1,
			StartMS: start,
			EndMS:   end,
			Text:    strings.Join(lines[textStart:], "\n"),
		})
	}
	return cues, nil
}

func Offset(cues []Cue, offsetMS int) []Cue {
	out := make([]Cue, len(cues))
	for idx, cue := range cues {
		cue.StartMS += offsetMS
		cue.EndMS += offsetMS
		out[idx] = cue
	}
	return out
}

func TrimToDuration(cues []Cue, durationMS int) []Cue {
	if durationMS <= 0 {
		return cues
	}
	out := make([]Cue, 0, len(cues))
	for _, cue := range cues {
		if cue.StartMS >= durationMS {
			continue
		}
		if cue.EndMS > durationMS {
			cue.EndMS = durationMS
		}
		out = append(out, cue)
	}
	return out
}

func FormatSRT(cues []Cue) string {
	sort.SliceStable(cues, func(i, j int) bool {
		return cues[i].StartMS < cues[j].StartMS
	})
	var b strings.Builder
	for idx, cue := range cues {
		if cue.EndMS <= cue.StartMS {
			continue
		}
		b.WriteString(fmt.Sprintf("%d\n", idx+1))
		b.WriteString(formatTimestamp(cue.StartMS))
		b.WriteString(" --> ")
		b.WriteString(formatTimestamp(cue.EndMS))
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(cue.Text))
		b.WriteString("\n\n")
	}
	return b.String()
}

func parseTimestamp(parts []string) (int, error) {
	if len(parts) != 4 {
		return 0, fmt.Errorf("invalid timestamp")
	}
	values := [4]int{}
	for idx, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return 0, err
		}
		values[idx] = value
	}
	return (((values[0]*60)+values[1])*60+values[2])*1000 + values[3], nil
}

func formatTimestamp(ms int) string {
	if ms < 0 {
		ms = 0
	}
	hours := ms / 3600000
	ms %= 3600000
	minutes := ms / 60000
	ms %= 60000
	seconds := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, ms)
}

func nonEmptyLines(block string) []string {
	raw := strings.Split(strings.TrimSpace(block), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
