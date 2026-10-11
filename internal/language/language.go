package language

import (
	"errors"
	"regexp"
	"strings"

	xlanguage "golang.org/x/text/language"
)

var tagPattern = regexp.MustCompile(`(?i)^[a-z]{2,3}(?:-[a-z]{4})?(?:-[a-z]{2})?$`)

// Normalize accepts an ISO language with an optional script and country.
func Normalize(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "_", "-")
	if !tagPattern.MatchString(value) {
		return "", errors.New("invalid subtitle language tag")
	}
	tag, err := xlanguage.Parse(value)
	if err != nil || tag == xlanguage.Und || tag.String() == "mul" || tag.String() == "zxx" {
		return "", errors.New("invalid subtitle language tag")
	}
	_, _, region := tag.Raw()
	if region != (xlanguage.Region{}) && !region.IsCountry() {
		return "", errors.New("invalid subtitle country")
	}
	return tag.String(), nil
}
