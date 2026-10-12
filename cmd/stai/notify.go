package main

import (
	"fmt"
	"strings"

	"stai/internal/config"
	"stai/internal/i18n"
)

// notify shows a macOS notification. Best-effort: a failure must never
// break the workflow.
func notify(title, subtitle, body string) {
	script := `on run argv
	display notification (item 1 of argv) with title (item 2 of argv) subtitle (item 3 of argv)
end run`
	cmd := utf8Cmd("osascript", "-e", script, "--", body, title, subtitle)
	_ = cmd.Run()
}

// notifyCopied surfaces the generated message in a macOS notification —
// SourceTree custom actions swallow stdout, so without it the user would
// see nothing before pasting. An empty configured subtitle falls back to the
// language-dependent default.
func notifyCopied(n config.Notify, msg string) {
	subtitle := n.Subtitle
	if subtitle == "" {
		subtitle = i18n.T("notify_subtitle")
	}
	notify(n.Title, subtitle, msg)
}

// dialogEdit shows the generated message in an editable macOS dialog. It
// returns the edited text, or ok=false when the user cancels or the dialog
// fails (never block the workflow because of a UI hiccup).
func dialogEdit(msg string) (edited string, ok bool) {
	cancel, okBtn := i18n.T("dialog_cancel"), i18n.T("dialog_ok")
	script := fmt.Sprintf(`on run argv
	return text returned of (display dialog %q default answer (item 1 of argv) buttons {%q, %q} default button %q cancel button %q with title %q)
end run`, i18n.T("dialog_edit_body"), cancel, okBtn, okBtn, cancel, i18n.T("dialog_edit_title"))
	cmd := utf8Cmd("osascript", "-e", script, "--", msg)
	out, err := cmd.Output()
	if err != nil {
		return "", false // -128 = user cancelled
	}
	return strings.TrimSuffix(string(out), "\n"), true
}