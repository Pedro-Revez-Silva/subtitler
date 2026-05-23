package subtitle

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

var Extensions = []string{".srt", ".vtt", ".ass", ".ssa", ".sub", ".idx"}

type Sidecar struct {
	Path     string
	Language string
	Ext      string
	Suffixes []string
}

func FindSidecars(videoPath string) ([]Sidecar, error) {
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var sidecars []Sidecar
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if !slices.Contains(Extensions, ext) {
			continue
		}
		nameWithoutExt := strings.TrimSuffix(name, filepath.Ext(name))
		if nameWithoutExt != base && !strings.HasPrefix(nameWithoutExt, base+".") {
			continue
		}
		suffix := strings.TrimPrefix(nameWithoutExt, base)
		suffix = strings.TrimPrefix(suffix, ".")
		parts := []string{}
		if suffix != "" {
			parts = strings.Split(suffix, ".")
		}
		sidecars = append(sidecars, Sidecar{
			Path:     filepath.Join(dir, name),
			Language: detectLanguage(parts),
			Ext:      ext,
			Suffixes: parts,
		})
	}
	return sidecars, nil
}

func OutputPath(videoPath, language, title string) string {
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	parts := []string{base}
	title = safeToken(title)
	if title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, jellyfinLanguage(language), "srt")
	return filepath.Join(dir, strings.Join(parts, "."))
}

func IsProtected(sidecar Sidecar, protectedSuffixes []string) bool {
	for _, suffix := range sidecar.Suffixes {
		for _, protected := range protectedSuffixes {
			if strings.EqualFold(suffix, protected) {
				return true
			}
		}
	}
	return false
}

func detectLanguage(parts []string) string {
	for _, part := range parts {
		normalized := normalizeLanguage(part)
		if normalized != "" {
			return normalized
		}
	}
	return ""
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "en", "eng", "english":
		return "en"
	case "pt", "por", "portuguese":
		return "pt"
	case "pt-pt", "pt_pt":
		return "pt-PT"
	case "pt-br", "pt_br":
		return "pt-BR"
	case "es", "spa", "spanish":
		return "es"
	case "fr", "fre", "fra", "french":
		return "fr"
	case "de", "ger", "deu", "german":
		return "de"
	default:
		if len(value) == 2 {
			return value
		}
		return ""
	}
}

func jellyfinLanguage(language string) string {
	switch normalizeLanguage(language) {
	case "pt-PT", "pt-BR", "pt":
		return "pt"
	case "en":
		return "en"
	default:
		language = strings.ToLower(strings.TrimSpace(language))
		if idx := strings.Index(language, "-"); idx > 0 {
			return language[:idx]
		}
		return language
	}
}

func safeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(" ", "-", ".", "-", "/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	value = replacer.Replace(value)
	value = strings.Trim(value, "-")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	return value
}
