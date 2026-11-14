package main

import (
	"bytes"
	"testing"
)

func TestMergetoolMissingMerged(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMergetool(nil, bytes.NewReader(nil), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout.String(), stderr.String())
	}
}

func TestMergetoolUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMergetool([]string{"--nope"}, bytes.NewReader(nil), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit %d\nstderr %s", code, stderr.String())
	}
}

func TestMergetoolHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMergetool([]string{"-h"}, bytes.NewReader(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstderr %s", code, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("--merged")) {
		t.Fatalf("help = %s", stderr.String())
	}
}
