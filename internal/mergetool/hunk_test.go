package mergetool

import (
	"bytes"
	"testing"
)

func TestParseTwoHunks(t *testing.T) {
	in := []byte("alpha\n<<<<<<< HEAD\nOURS-1\n=======\nTHEIRS-1\n>>>>>>> topic\nmid\n<<<<<<< HEAD\nOURS-2\n=======\nTHEIRS-2\n>>>>>>> topic\nomega\n")
	hunks := Parse(in)
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d", len(hunks))
	}
	if string(hunks[0].Ours) != "OURS-1\n" || string(hunks[0].Theirs) != "THEIRS-1\n" {
		t.Fatalf("hunk0 ours=%q theirs=%q", hunks[0].Ours, hunks[0].Theirs)
	}
	if string(hunks[1].Ours) != "OURS-2\n" || string(hunks[1].Theirs) != "THEIRS-2\n" {
		t.Fatalf("hunk1 ours=%q theirs=%q", hunks[1].Ours, hunks[1].Theirs)
	}
	if countStartMarkers(in) != 2 {
		t.Fatalf("starts = %d", countStartMarkers(in))
	}
}

func TestParseDiff3Sides(t *testing.T) {
	in := []byte("<<<<<<< HEAD\nOURS\n||||||| base\nBASE-ONLY\n=======\nTHEIRS\n>>>>>>> topic\n")
	hunks := Parse(in)
	if len(hunks) != 1 {
		t.Fatalf("hunks = %d", len(hunks))
	}
	if string(hunks[0].Ours) != "OURS\n" || string(hunks[0].Base) != "BASE-ONLY\n" || string(hunks[0].Theirs) != "THEIRS\n" {
		t.Fatalf("ours=%q base=%q theirs=%q", hunks[0].Ours, hunks[0].Base, hunks[0].Theirs)
	}
	if bytes.Contains(hunks[0].Ours, []byte("BASE-ONLY")) || bytes.Contains(hunks[0].Theirs, []byte("BASE-ONLY")) {
		t.Fatal("base leaked into a side")
	}
}

func TestParseIncompleteIsNotAHunk(t *testing.T) {
	in := []byte("<<<<<<< HEAD\nno closer\n")
	if len(Parse(in)) != 0 {
		t.Fatal("unclosed marker must not become a hunk")
	}
	if countStartMarkers(in) != 1 {
		t.Fatalf("starts = %d", countStartMarkers(in))
	}
}
