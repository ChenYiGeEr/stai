package main

import (
	"bytes"
	"fmt"
	"strings"
)

// writeClipboard puts msg on the pasteboard and verifies it by reading it
// back. pbcopy is tried first; if it reports success but the pasteboard does
// not hold msg (seen when stai runs under SourceTree), the message is written
// through NSPasteboard via osascript. Every step is logged.
func writeClipboard(msg string) error {
	before, berr := pasteboardChangeCount()
	logf("gen clipboard before-write changeCount=%s err=%v", before, berr)

	var stderr bytes.Buffer
	cmd := utf8Cmd("pbcopy")
	cmd.Stdin = strings.NewReader(msg)
	cmd.Stderr = &stderr
	perr := cmd.Run()
	logf("gen clipboard pbcopy err=%v stderr=%q", perr, stderr.String())

	// Only trust pbcopy if it succeeded AND the clipboard matches
	if perr == nil {
		if got, _ := clipboardText(); got == msg {
			logf("gen clipboard written via pbcopy")
			return nil
		}
	}

	cmd = utf8Cmd("osascript", "-l", "JavaScript", "-e", pasteboardWriteJXA, "--", msg)
	stderr.Reset()
	cmd.Stderr = &stderr
	oerr := cmd.Run()
	logf("gen clipboard NSPasteboard fallback err=%v stderr=%q", oerr, stderr.String())

	got, _ := clipboardText()
	if got != msg {
		return fmt.Errorf("pasteboard does not hold the message after both pbcopy and NSPasteboard (pbcopy err=%v, fallback err=%v)", perr, oerr)
	}
	logf("gen clipboard written via NSPasteboard fallback")
	return nil
}

// pasteboardWriteJXA clears the general pasteboard and sets argv[0] as its
// plain-text content.
const pasteboardWriteJXA = `function run(argv) {
	ObjC.import('AppKit');
	var pb = $.NSPasteboard.generalPasteboard;
	pb.clearContents();
	pb.setStringForType($(argv[0]), $.NSPasteboardTypeString);
	return 'ok';
}`

// clipboardText returns the current pasteboard content as text.
func clipboardText() (string, error) {
	out, err := utf8Cmd("pbpaste").Output()
	return string(out), err
}

// pasteboardChangeCount returns the general pasteboard's changeCount, which
// increments on every write by any process. A change between two checks
// means something else wrote to the clipboard in between.
func pasteboardChangeCount() (string, error) {
	out, err := utf8Cmd("osascript", "-l", "JavaScript", "-e",
		"ObjC.import('AppKit'); $.NSPasteboard.generalPasteboard.changeCount").Output()
	return strings.TrimSpace(string(out)), err
}

// logClipboard records whether the clipboard still holds want, and the
// pasteboard changeCount, at a named point of the run. Used to tell whether
// the clipboard was emptied after stai wrote to it.
func logClipboard(tag, want string) {
	got, err := clipboardText()
	count, cerr := pasteboardChangeCount()
	logf("gen clipboard %s match=%v changeCount=%s got=%q err=%v countErr=%v",
		tag, got == want, count, got, err, cerr)
}