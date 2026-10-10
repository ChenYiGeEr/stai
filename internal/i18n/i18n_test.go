package i18n

import "testing"

func TestNormalizeAcceptsOnlySupported(t *testing.T) {
	for _, l := range []string{"zh-CN", "en"} {
		if got, ok := Normalize(l); !ok || got != l {
			t.Errorf("Normalize(%q) = %q, %v; want %q, true", l, got, ok, l)
		}
	}
	for _, l := range []string{"", "zh", "zh-TW", "EN", "fr", "en-US"} {
		if got, ok := Normalize(l); ok || got != "zh-CN" {
			t.Errorf("Normalize(%q) = %q, %v; want fallback zh-CN, false", l, got, ok)
		}
	}
}

func TestTLooksUpActiveLanguage(t *testing.T) {
	defer SetLang("zh-CN")
	SetLang("zh-CN")
	if got := T("staged_empty"); got != "暂存区没有改动" {
		t.Errorf("zh staged_empty = %q", got)
	}
	SetLang("en")
	if got := T("staged_empty"); got != "staging area is empty" {
		t.Errorf("en staged_empty = %q", got)
	}
}

func TestTAppliesFormatArgs(t *testing.T) {
	defer SetLang("zh-CN")
	SetLang("zh-CN")
	if got := T("ahead_none", "origin/main"); got != "当前分支没有领先 origin/main 的提交" {
		t.Errorf("zh ahead_none = %q", got)
	}
	SetLang("en")
	if got := T("ahead_none", "origin/main"); got != "current branch has no commits ahead of origin/main" {
		t.Errorf("en ahead_none = %q", got)
	}
}

func TestTFallsBackToZhForUnknownKey(t *testing.T) {
	defer SetLang("zh-CN")
	SetLang("en")
	if got := T("zh_only_key_never_defined"); got != "!zh_only_key_never_defined!" {
		t.Errorf("unknown key should render as !key!, got %q", got)
	}
}

// TestKeyParity ensures both tables define exactly the same keys, so a
// translation can never be silently missing in one language.
func TestKeyParity(t *testing.T) {
	for k := range zhCN {
		if _, ok := en[k]; !ok {
			t.Errorf("en is missing key %q", k)
		}
	}
	for k := range en {
		if _, ok := zhCN[k]; !ok {
			t.Errorf("zh-CN is missing key %q", k)
		}
	}
}
