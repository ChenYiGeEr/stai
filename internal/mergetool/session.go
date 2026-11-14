// Package mergetool resolves one text file's conflict hunks for `stai mergetool`.
// One call handles one file. Keys choose each hunk; nothing is written until
// the user has accepted, skipped, quit, or saved from an editor.
package mergetool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"stai/internal/color"
	"stai/internal/i18n"
)

// ErrNoEditor means GIT_EDITOR, VISUAL, and EDITOR are all unset.
var ErrNoEditor = errors.New("mergetool: no editor configured")

// Advice is one hunk's model suggestion. Choice is "ours", "theirs", or
// "merge". Resolution is used only when Choice is "merge".
type Advice struct {
	Explanation string `json:"explanation"`
	Choice      string `json:"choice"`
	Resolution  string `json:"resolution"`
}

// Options is one mergetool invocation. In supplies keystrokes (tests may
// pass a fixture; a terminal user passes stdin). Edit, when non-nil, runs
// instead of the editor named by the environment.
type Options struct {
	Base, Local, Remote, Merged string
	In                          io.Reader
	Out                         io.Writer
	Advise                      func(context.Context, Hunk) (Advice, error)
	Edit                        func(path string) error
}

const (
	statusPending = iota
	statusAccepted
	statusSkipped
)

type decision struct {
	status int
	repl   []byte
}

type state struct {
	base  []byte
	hunks []Hunk
	dec   []decision
	i     int
}

type step int

const (
	stepStay step = iota
	stepNext
	stepQuit
	stepResolved
)

// Run drives one file. Exit 0 means $MERGED has no conflict markers (it was
// already clean, the user resolved every hunk, or the editor removed every
// marker). Exit 1 means a skip, a quit, an interrupt, or a conflict this
// slice refuses. Exit 2 means the invocation itself is incomplete.
func Run(ctx context.Context, opt Options) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if opt.Out == nil {
		opt.Out = io.Discard
	}
	if strings.TrimSpace(opt.Merged) == "" {
		fmt.Fprintln(opt.Out, i18n.T("mergetool_missing_merged"))
		return 2
	}
	kind, err := classify(opt)
	if err != nil {
		fmt.Fprintf(opt.Out, i18n.T("mergetool_read_failed", err)+"\n")
		return 1
	}
	switch kind {
	case kindBinary, kindDelete, kindRename, kindSubmodule:
		fmt.Fprintln(opt.Out, i18n.T("mergetool_unsupported", kindLabel(kind)))
		return 1
	case kindMalformed:
		fmt.Fprintln(opt.Out, i18n.T("mergetool_malformed"))
		return 1
	case kindClean:
		return 0
	}

	data, err := os.ReadFile(opt.Merged)
	if err != nil {
		fmt.Fprintf(opt.Out, i18n.T("mergetool_read_failed", err)+"\n")
		return 1
	}
	st := newState(data)
	if len(st.hunks) == 0 {
		return 0
	}
	for st.i < len(st.hunks) {
		if ctx.Err() != nil {
			return st.finish(opt)
		}
		if st.dec[st.i].status == statusSkipped {
			st.i++
			continue
		}
		h := st.hunks[st.i]
		advice, advErr := advise(ctx, opt, h)
		if ctx.Err() != nil {
			return st.finish(opt)
		}
		haveRec := advErr == nil
		writeView(opt.Out, st, h, advice, advErr)
		for {
			key, err := nextKey(ctx, opt.In)
			if err != nil {
				return st.finish(opt)
			}
			switch st.onKey(opt, key, h, advice, haveRec) {
			case stepStay:
				continue
			case stepNext:
				// The hunk was accepted, skipped, or rescanned; show the next one.
			case stepQuit:
				return st.finish(opt)
			case stepResolved:
				fmt.Fprintln(opt.Out, i18n.T("mergetool_done"))
				return 0
			}
			break
		}
	}
	return st.finish(opt)
}

func newState(data []byte) *state {
	hunks := Parse(data)
	return &state{base: data, hunks: hunks, dec: make([]decision, len(hunks))}
}

func advise(ctx context.Context, opt Options, h Hunk) (Advice, error) {
	if opt.Advise == nil {
		return Advice{}, errors.New(i18n.T("mergetool_ai_unusable"))
	}
	advice, err := opt.Advise(ctx, h)
	if err != nil {
		return Advice{}, err
	}
	switch advice.Choice {
	case "ours", "theirs":
		advice.Resolution = ""
		return advice, nil
	case "merge":
		if advice.Resolution == "" || hasConflictMarker(advice.Resolution) {
			return Advice{}, errors.New(i18n.T("mergetool_ai_unusable"))
		}
		return advice, nil
	default:
		return Advice{}, errors.New(i18n.T("mergetool_ai_unusable"))
	}
}

func (st *state) onKey(opt Options, key byte, h Hunk, advice Advice, haveRec bool) step {
	switch key {
	case 'a':
		if !haveRec {
			fmt.Fprintln(opt.Out, i18n.T("mergetool_a_unavailable"))
			writePrompt(opt.Out, false)
			return stepStay
		}
		st.dec[st.i] = decision{status: statusAccepted, repl: acceptedRepl(h, advice)}
		st.i++
		return stepNext
	case 'o':
		st.dec[st.i] = decision{status: statusAccepted, repl: append([]byte(nil), h.Ours...)}
		st.i++
		return stepNext
	case 't':
		st.dec[st.i] = decision{status: statusAccepted, repl: append([]byte(nil), h.Theirs...)}
		st.i++
		return stepNext
	case 'p':
		writePreview(opt.Out, h, advice, !haveRec)
		writePrompt(opt.Out, haveRec)
		return stepStay
	case 's':
		st.dec[st.i].status = statusSkipped
		st.i++
		return stepNext
	case 'q':
		return stepQuit
	case 'e':
		return st.edit(opt, haveRec)
	default:
		fmt.Fprintln(opt.Out, i18n.T("mergetool_unknown_key", string(rune(key))))
		writePrompt(opt.Out, haveRec)
		return stepStay
	}
}

func acceptedRepl(h Hunk, advice Advice) []byte {
	switch advice.Choice {
	case "ours":
		return append([]byte(nil), h.Ours...)
	case "theirs":
		return append([]byte(nil), h.Theirs...)
	default:
		return []byte(advice.Resolution)
	}
}

func (st *state) edit(opt Options, haveRec bool) step {
	ed, err := resolveEditor(opt)
	if err != nil {
		fmt.Fprintln(opt.Out, i18n.T("mergetool_no_editor"))
		writePrompt(opt.Out, haveRec)
		return stepStay
	}
	pre := st.render()
	if err := writeFile(opt.Merged, pre); err != nil {
		fmt.Fprintf(opt.Out, i18n.T("mergetool_write_failed", err)+"\n")
		writePrompt(opt.Out, haveRec)
		return stepStay
	}
	if err := ed(opt.Merged); err != nil {
		_ = writeFile(opt.Merged, pre)
		if errors.Is(err, ErrNoEditor) {
			fmt.Fprintln(opt.Out, i18n.T("mergetool_no_editor"))
		} else {
			fmt.Fprintf(opt.Out, i18n.T("mergetool_editor_failed", err)+"\n")
		}
		writePrompt(opt.Out, haveRec)
		return stepStay
	}
	post, err := os.ReadFile(opt.Merged)
	if err != nil {
		_ = writeFile(opt.Merged, pre)
		fmt.Fprintf(opt.Out, i18n.T("mergetool_read_failed", err)+"\n")
		writePrompt(opt.Out, haveRec)
		return stepStay
	}
	if countStartMarkers(post) == 0 {
		return stepResolved
	}
	hunks := Parse(post)
	if len(hunks) == 0 || countStartMarkers(post) != len(hunks) {
		_ = writeFile(opt.Merged, pre)
		fmt.Fprintln(opt.Out, i18n.T("mergetool_editor_malformed"))
		writePrompt(opt.Out, haveRec)
		return stepStay
	}
	st.rebase(post, hunks)
	return stepNext
}

func resolveEditor(opt Options) (func(string) error, error) {
	if opt.Edit != nil {
		return opt.Edit, nil
	}
	if editorFromEnv() == "" {
		return nil, ErrNoEditor
	}
	return runEnvEditor, nil
}

func editorFromEnv() string {
	for _, key := range []string{"GIT_EDITOR", "VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func runEnvEditor(path string) error {
	editor := editorFromEnv()
	if editor == "" {
		return ErrNoEditor
	}
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "stai-editor", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// rebase keeps previously skipped conflict blocks and continues at the
// first hunk that is still unresolved.
func (st *state) rebase(post []byte, hunks []Hunk) {
	skipped := make(map[string]int)
	for i, h := range st.hunks {
		if st.dec[i].status == statusSkipped {
			skipped[string(st.base[h.Start:h.End])]++
		}
	}
	st.base = post
	st.hunks = hunks
	st.dec = make([]decision, len(hunks))
	for i, h := range hunks {
		key := string(post[h.Start:h.End])
		if skipped[key] > 0 {
			st.dec[i].status = statusSkipped
			skipped[key]--
		}
	}
	st.i = 0
	for st.i < len(st.dec) && st.dec[st.i].status == statusSkipped {
		st.i++
	}
}

func (st *state) allAccepted() bool {
	if len(st.dec) == 0 {
		return false
	}
	for _, d := range st.dec {
		if d.status != statusAccepted {
			return false
		}
	}
	return true
}

func (st *state) render() []byte {
	var b bytes.Buffer
	prev := 0
	for i, h := range st.hunks {
		b.Write(st.base[prev:h.Start])
		if st.dec[i].status == statusAccepted {
			b.Write(st.dec[i].repl)
		} else {
			b.Write(st.base[h.Start:h.End])
		}
		prev = h.End
	}
	b.Write(st.base[prev:])
	return b.Bytes()
}

func (st *state) finish(opt Options) int {
	out := st.render()
	code := 1
	if st.allAccepted() && countStartMarkers(out) == 0 {
		code = 0
	}
	if err := persist(opt.Merged, out); err != nil {
		fmt.Fprintf(opt.Out, i18n.T("mergetool_write_failed", err)+"\n")
		return 1
	}
	if code == 0 {
		fmt.Fprintln(opt.Out, i18n.T("mergetool_done"))
	} else {
		fmt.Fprintln(opt.Out, i18n.T("mergetool_left"))
	}
	return code
}

func persist(path string, out []byte) error {
	cur, err := os.ReadFile(path)
	if err == nil && bytes.Equal(cur, out) {
		return nil
	}
	return writeFile(path, out)
}

func writeFile(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if perm := info.Mode().Perm(); perm != 0 {
			mode = perm
		}
	}
	return os.WriteFile(path, data, mode)
}

func writeView(w io.Writer, st *state, h Hunk, advice Advice, advErr error) {
	fmt.Fprintln(w, color.Cyan(color.Bold(i18n.T("mergetool_hunk_header", st.i+1, len(st.hunks)))))
	raw := st.base[h.Start:h.End]
	w.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, color.Bold(i18n.T("mergetool_explanation")))
	if advErr != nil {
		fmt.Fprintln(w, color.Yellow(i18n.T("mergetool_ai_failed", advErr.Error())))
	} else if advice.Explanation != "" {
		fmt.Fprintln(w, advice.Explanation)
	} else {
		fmt.Fprintln(w, i18n.T("mergetool_no_recommendation"))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, color.Bold(i18n.T("mergetool_recommendation")))
	switch {
	case advErr != nil:
		fmt.Fprintln(w, i18n.T("mergetool_no_recommendation"))
	case advice.Choice == "ours":
		fmt.Fprintln(w, i18n.T("mergetool_rec_ours"))
	case advice.Choice == "theirs":
		fmt.Fprintln(w, i18n.T("mergetool_rec_theirs"))
	default:
		fmt.Fprintln(w, i18n.T("mergetool_rec_merge"))
	}
	fmt.Fprintln(w)
	writePreview(w, h, advice, advErr != nil)
	fmt.Fprintln(w)
	writePrompt(w, advErr == nil)
}

func writePreview(w io.Writer, h Hunk, advice Advice, failed bool) {
	fmt.Fprintln(w, color.Bold(i18n.T("mergetool_preview")))
	if !failed {
		body := ""
		switch advice.Choice {
		case "ours":
			body = string(h.Ours)
		case "theirs":
			body = string(h.Theirs)
		default:
			body = advice.Resolution
		}
		writeBlock(w, body)
		return
	}
	fmt.Fprintln(w, i18n.T("mergetool_preview_no_rec"))
	fmt.Fprintln(w, i18n.T("mergetool_side_ours"))
	writeBlock(w, string(h.Ours))
	fmt.Fprintln(w, i18n.T("mergetool_side_theirs"))
	writeBlock(w, string(h.Theirs))
}

func writeBlock(w io.Writer, body string) {
	if body == "" {
		fmt.Fprintln(w)
		return
	}
	fmt.Fprint(w, body)
	if !strings.HasSuffix(body, "\n") {
		fmt.Fprintln(w)
	}
}

func writePrompt(w io.Writer, haveRec bool) {
	if haveRec {
		fmt.Fprintln(w, i18n.T("mergetool_prompt"))
		return
	}
	fmt.Fprintln(w, i18n.T("mergetool_prompt_no_a"))
}

func nextKey(ctx context.Context, r io.Reader) (byte, error) {
	if r == nil {
		return 0, io.EOF
	}
	for {
		b, err := readByte(ctx, r)
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return b, nil
		}
	}
}

func readByte(ctx context.Context, r io.Reader) (byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	type result struct {
		b   byte
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var buf [1]byte
		n, err := r.Read(buf[:])
		ch <- result{b: buf[0], n: n, err: err}
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case res := <-ch:
		if res.n == 1 {
			return res.b, nil
		}
		if res.err == nil {
			res.err = io.EOF
		}
		return 0, res.err
	}
}

func kindLabel(kind conflictKind) string {
	switch kind {
	case kindBinary:
		return i18n.T("mergetool_kind_binary")
	case kindDelete:
		return i18n.T("mergetool_kind_delete")
	case kindRename:
		return i18n.T("mergetool_kind_rename")
	case kindSubmodule:
		return i18n.T("mergetool_kind_submodule")
	default:
		return ""
	}
}
