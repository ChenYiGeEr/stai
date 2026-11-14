package mergetool

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type conflictKind int

const (
	kindText conflictKind = iota
	kindClean
	kindBinary
	kindDelete
	kindRename
	kindSubmodule
	kindMalformed
)

// classify decides whether $MERGED is a text conflict this slice can edit.
// Binary, delete, rename, and submodule conflicts are refused. A text file
// that already has no conflict markers is clean.
func classify(opt Options) (conflictKind, error) {
	if kind, ok := gitConflictKind(opt.Merged); ok {
		return kind, nil
	}
	for _, path := range []string{opt.Base, opt.Local, opt.Remote, opt.Merged} {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		if bytes.Contains(data, []byte{0}) {
			return kindBinary, nil
		}
	}
	if missing, err := sideMissing(opt.Local); err != nil {
		return 0, err
	} else if missing {
		return kindDelete, nil
	}
	if missing, err := sideMissing(opt.Remote); err != nil {
		return 0, err
	} else if missing {
		return kindDelete, nil
	}
	data, err := os.ReadFile(opt.Merged)
	if err != nil {
		return 0, err
	}
	if submoduleText(data) {
		return kindSubmodule, nil
	}
	hunks := Parse(data)
	starts := countStartMarkers(data)
	if starts != len(hunks) {
		return kindMalformed, nil
	}
	if len(hunks) > 0 {
		return kindText, nil
	}
	return kindClean, nil
}

func sideMissing(path string) (bool, error) {
	if path == "" {
		return true, nil
	}
	_, err := os.Stat(path)
	if err == nil {
		return false, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	return false, err
}

// submoduleText matches git's "Subproject commit <sha>" conflict body.
// Every non-marker line must be that sentence, so ordinary prose that
// merely mentions a submodule is left as text.
func submoduleText(data []byte) bool {
	lines := splitLines(data)
	payload := 0
	for _, line := range lines {
		if len(line.text) == 0 {
			continue
		}
		if markerLabel(line.text, "<<<<<<<") || markerLabel(line.text, "=======") ||
			markerLabel(line.text, ">>>>>>>") || markerLabel(line.text, "|||||||") {
			continue
		}
		if !subprojectLine(string(line.text)) {
			return false
		}
		payload++
	}
	return payload > 0
}

func subprojectLine(s string) bool {
	const prefix = "Subproject commit "
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	hex := s[len(prefix):]
	if len(hex) != 40 && len(hex) != 64 {
		return false
	}
	for _, c := range hex {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

type indexStage struct {
	mode, sha, stage, path string
}

// gitConflictKind inspects the index when $MERGED is an unmerged path.
// Content conflicts (both stages present, not a gitlink) return ok=false
// so the caller can still distinguish binary bytes from text markers.
// Rename/rename is the index shape git actually writes: the old path is
// stage 1 only, and each new path is stage 2 only or stage 3 only.
func gitConflictKind(mergedPath string) (conflictKind, bool) {
	abs, err := filepath.Abs(mergedPath)
	if err != nil {
		return 0, false
	}
	root, err := gitOutput(filepath.Dir(abs), "rev-parse", "--show-toplevel")
	if err != nil {
		return 0, false
	}
	root = strings.TrimSpace(root)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, false
	}
	rel = filepath.ToSlash(rel)
	entries, err := unmergedEntries(root)
	if err != nil {
		return 0, false
	}
	var mine []indexStage
	for _, e := range entries {
		if e.path == rel {
			mine = append(mine, e)
		}
	}
	if len(mine) == 0 {
		return 0, false
	}
	has1, has2, has3 := false, false, false
	for _, e := range mine {
		if e.mode == "160000" {
			return kindSubmodule, true
		}
		switch e.stage {
		case "1":
			has1 = true
		case "2":
			has2 = true
		case "3":
			has3 = true
		}
	}
	if has2 && has3 {
		return 0, false
	}
	if renamePath(entries, rel, has1, has2, has3) {
		return kindRename, true
	}
	return kindDelete, true
}

func unmergedEntries(root string) ([]indexStage, error) {
	ls, err := gitOutput(root, "ls-files", "-u")
	if err != nil {
		return nil, err
	}
	var entries []indexStage
	for _, line := range strings.Split(ls, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 3 {
			continue
		}
		entries = append(entries, indexStage{mode: fields[0], sha: fields[1], stage: fields[2], path: path})
	}
	return entries, nil
}

func renamePath(all []indexStage, rel string, has1, has2, has3 bool) bool {
	// Destination of a rename/rename: the path exists on only one side.
	if !has1 && has2 != has3 {
		return true
	}
	// Source of a rename/rename: base only, while some other path is a one-sided add.
	if has1 && !has2 && !has3 {
		for _, e := range all {
			if e.path != rel && oneSidedAdd(all, e.path) {
				return true
			}
		}
	}
	return false
}

func oneSidedAdd(all []indexStage, path string) bool {
	has1, has2, has3 := false, false, false
	for _, e := range all {
		if e.path != path {
			continue
		}
		switch e.stage {
		case "1":
			has1 = true
		case "2":
			has2 = true
		case "3":
			has3 = true
		}
	}
	return !has1 && has2 != has3
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "--no-pager"}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func gitEnv() []string {
	env := os.Environ()
	kept := make([]string, 0, len(env)+2)
	for _, e := range env {
		if strings.HasPrefix(e, "LC_ALL=") || strings.HasPrefix(e, "LANG=") || strings.HasPrefix(e, "LC_MESSAGES=") {
			continue
		}
		kept = append(kept, e)
	}
	return append(kept, "LC_ALL=C", "LANG=C")
}
