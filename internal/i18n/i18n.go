// Package i18n owns text shown to people. Machine-readable identifiers and
// values from macOS stay outside this package.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const DefaultLocale = "en_GB"

//go:embed locales/*.json
var localeFiles embed.FS

type localeFile struct {
	Locale   string            `json:"locale"`
	Name     string            `json:"name"`
	Messages map[string]string `json:"messages"`
}

type Localizer struct {
	locale   string
	messages map[string]string
}

var registry = map[string]string{
	DefaultLocale: "locales/en_GB.json",
}

func Normalize(locale string) string {
	locale = strings.TrimSpace(strings.ReplaceAll(locale, "-", "_"))
	if strings.EqualFold(locale, "en_GB") {
		return DefaultLocale
	}
	return locale
}

// Load returns the selected locale and whether it was available. Unavailable
// and incomplete locales fail closed to the complete en_GB catalogue.
func Load(locale string) (*Localizer, bool) {
	normalized := Normalize(locale)
	if normalized == "" {
		normalized = DefaultLocale
	}
	localizer, err := load(normalized)
	if err == nil {
		return localizer, true
	}
	fallback, fallbackErr := load(DefaultLocale)
	if fallbackErr != nil {
		panic(fallbackErr)
	}
	return fallback, false
}

func load(locale string) (*Localizer, error) {
	path, ok := registry[locale]
	if !ok {
		return nil, fmt.Errorf("locale %q is not registered", locale)
	}
	contents, err := localeFiles.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var decoded localeFile
	if err := json.Unmarshal(contents, &decoded); err != nil {
		return nil, err
	}
	if decoded.Locale != locale || strings.TrimSpace(decoded.Name) == "" || len(decoded.Messages) == 0 {
		return nil, fmt.Errorf("locale file %s is incomplete", filepath.Base(path))
	}
	for key, message := range decoded.Messages {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(message) == "" {
			return nil, fmt.Errorf("locale %s has a blank key or message", locale)
		}
	}
	return &Localizer{locale: locale, messages: decoded.Messages}, nil
}

func EnglishGB() *Localizer {
	localizer, _ := Load(DefaultLocale)
	return localizer
}

func (l *Localizer) Locale() string {
	if l == nil {
		return DefaultLocale
	}
	return l.locale
}

func (l *Localizer) T(key string, arguments ...any) string {
	if l == nil {
		l = EnglishGB()
	}
	message, ok := l.messages[key]
	if !ok {
		return "[missing message: " + key + "]"
	}
	if len(arguments) == 0 {
		return message
	}
	return fmt.Sprintf(message, arguments...)
}

func (l *Localizer) Keys() []string {
	keys := make([]string, 0, len(l.messages))
	for key := range l.messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type contextKey struct{}

func WithContext(ctx context.Context, localizer *Localizer) context.Context {
	return context.WithValue(ctx, contextKey{}, localizer)
}

func FromContext(ctx context.Context) *Localizer {
	if localizer, ok := ctx.Value(contextKey{}).(*Localizer); ok && localizer != nil {
		return localizer
	}
	return EnglishGB()
}

func Available() []string {
	locales := make([]string, 0, len(registry))
	for locale := range registry {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	return locales
}
