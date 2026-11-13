package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func TestAC1Copy(t *testing.T) {
	root := moduleRoot(t)
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	zh := read("README.md")
	en := read("README.en.md")

	for _, s := range []string{
		"⌥G → 通知 → 提交框 Cmd+V",
		"打开提交框不会运行 `prepare-commit-msg`",
		"Empty Commit Message / 你没有填写提交说明",
		"4.2.19（macOS）",
		"Windows 与更早版本不在本说明的核对范围内",
		"命令行空信息提交（钩子写入 message 文件）",
		"仍可能留下空提交",
		"生成的说明不会出现在提交框里",
	} {
		if !strings.Contains(zh, s) {
			t.Errorf("README.md missing %q", s)
		}
	}
	for _, s := range []string{
		"⌥G → notification → Cmd+V in the commit box",
		"Opening the commit box does not run `prepare-commit-msg`",
		"Empty Commit Message / 你没有填写提交说明",
		"4.2.19 (macOS)",
		"Windows and older",
		"CLI commit with an empty message file",
		"an empty commit can still be left behind",
		"the generated text does not show up in the commit box",
	} {
		if !strings.Contains(en, s) {
			t.Errorf("README.en.md missing %q", s)
		}
	}

	var scanned []string
	for _, rel := range []string{"README.md", "README.en.md"} {
		scanned = append(scanned, rel)
	}
	for _, dir := range []string{"cmd/stai", "internal/i18n"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			scanned = append(scanned, filepath.Join(dir, e.Name()))
		}
	}
	banned := []string{"填入提交框", "fills in", "fill in the commit box"}
	for _, rel := range scanned {
		body := strings.ToLower(read(rel))
		for _, phrase := range banned {
			if strings.Contains(body, strings.ToLower(phrase)) {
				t.Errorf("%s still contains %q", rel, phrase)
			}
		}
	}
}
