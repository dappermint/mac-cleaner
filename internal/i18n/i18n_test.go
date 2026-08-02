package i18n

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var placeholder = regexp.MustCompile(`%(?:\[[0-9]+\])?[+#0 -]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?[a-zA-Z%]`)

func TestShippedLocalesAreComplete(t *testing.T) {
	source, err := load(DefaultLocale)
	if err != nil {
		t.Fatalf("load source locale: %v", err)
	}
	for _, locale := range Available() {
		translated, err := load(locale)
		if err != nil {
			t.Errorf("load %s: %v", locale, err)
			continue
		}
		if !reflect.DeepEqual(source.Keys(), translated.Keys()) {
			t.Errorf("%s does not have exactly the en_GB message keys", locale)
		}
		for key, sourceMessage := range source.messages {
			translatedMessage := translated.messages[key]
			if strings.TrimSpace(translatedMessage) == "" {
				t.Errorf("%s has a blank %s", locale, key)
			}
			if !reflect.DeepEqual(placeholder.FindAllString(sourceMessage, -1), placeholder.FindAllString(translatedMessage, -1)) {
				t.Errorf("%s changes placeholders in %s", locale, key)
			}
		}
	}
}

func TestEnglishAliasesAndFallback(t *testing.T) {
	localizer, available := Load("en-GB")
	if !available || localizer.Locale() != DefaultLocale {
		t.Fatalf("en-GB did not resolve to en_GB: locale=%q available=%v", localizer.Locale(), available)
	}
	localizer, available = Load("nl_NL")
	if available || localizer.Locale() != DefaultLocale {
		t.Fatalf("missing locale did not fall back: locale=%q available=%v", localizer.Locale(), available)
	}
}

func TestMissingMessageIsVisible(t *testing.T) {
	if got := EnglishGB().T("not.real"); got != "[missing message: not.real]" {
		t.Errorf("missing message = %q", got)
	}
}
