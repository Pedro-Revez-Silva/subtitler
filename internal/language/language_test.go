package language

import "testing"

func TestNormalizeLanguagesAndLocales(t *testing.T) {
	for input, want := range map[string]string{"fr": "fr", "ENG": "en", "pt_br": "pt-BR", "es-mx": "es-MX", "zh-hant-tw": "zh-Hant-TW"} {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("Normalize(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"", "auto", "und", "mul", "zxx", "xx", "en-XX", "../en", "en.srt"} {
		if _, err := Normalize(input); err == nil {
			t.Fatalf("accepted invalid language %q", input)
		}
	}
}
