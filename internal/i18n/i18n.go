// Package i18n holds stai's user-facing strings in both supported
// languages. The active language is a package-level variable set once after
// config load (stai is a single-threaded, short-lived CLI). AI prompts keep
// their own per-language templates in internal/ai; only CLI-facing text
// lives here.
package i18n

import "fmt"

// lang is the active language code: "zh-CN" or "en".
var lang = "zh-CN"

// SetLang sets the active language. Call once after config load.
func SetLang(l string) { lang = l }

// Current returns the active language code.
func Current() string { return lang }

// Normalize validates l strictly: only "zh-CN" and "en" are accepted. It
// returns the fallback "zh-CN" and false for anything else.
func Normalize(l string) (string, bool) {
	if l == "zh-CN" || l == "en" {
		return l, true
	}
	return "zh-CN", false
}

// T returns the message for key in the active language, applying fmt args
// when given. Unknown keys fall back to zh-CN, then render as "!key!" so a
// missing translation is visible instead of silently blank.
func T(key string, args ...any) string {
	s, ok := messages[lang][key]
	if !ok {
		s, ok = messages["zh-CN"][key]
	}
	if !ok {
		return "!" + key + "!"
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}
