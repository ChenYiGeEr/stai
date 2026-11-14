package mergetool

import "bytes"

// Hunk is one complete conflict region inside $MERGED.
// Ours is the --local side (between <<<<<<< and =======, or ||||||| when
// the file uses diff3). Theirs is the --remote side. Base is set only for
// diff3 conflicts.
type Hunk struct {
	Start, End   int
	Ours, Theirs []byte
	Base         []byte
}

type fileLine struct {
	start, end int
	text       []byte
}

func splitLines(data []byte) []fileLine {
	var out []fileLine
	i := 0
	for i < len(data) {
		start := i
		for i < len(data) && data[i] != '\n' {
			i++
		}
		textEnd := i
		if i < len(data) && data[i] == '\n' {
			i++
		}
		text := data[start:textEnd]
		if len(text) > 0 && text[len(text)-1] == '\r' {
			text = text[:len(text)-1]
		}
		out = append(out, fileLine{start: start, end: i, text: text})
	}
	return out
}

func markerLabel(text []byte, sig string) bool {
	if string(text) == sig {
		return true
	}
	return bytes.HasPrefix(text, []byte(sig+" "))
}

// Parse returns every complete conflict hunk in data, in order.
func Parse(data []byte) []Hunk {
	lines := splitLines(data)
	var hunks []Hunk
	for i := 0; i < len(lines); i++ {
		if !markerLabel(lines[i].text, "<<<<<<<") {
			continue
		}
		start := i
		oursEnd := -1
		baseStart := -1
		baseEnd := -1
		theirsStart := -1
		found := false
		for j := i + 1; j < len(lines); j++ {
			if markerLabel(lines[j].text, "<<<<<<<") {
				i = j - 1
				break
			}
			if markerLabel(lines[j].text, "|||||||") && oursEnd < 0 && baseStart < 0 {
				oursEnd = lines[j].start
				baseStart = lines[j].end
				continue
			}
			if markerLabel(lines[j].text, "=======") && theirsStart < 0 && (oursEnd >= 0 || baseStart < 0) {
				if baseStart >= 0 {
					baseEnd = lines[j].start
				} else {
					oursEnd = lines[j].start
				}
				theirsStart = lines[j].end
				continue
			}
			if markerLabel(lines[j].text, ">>>>>>>") && theirsStart >= 0 && oursEnd >= 0 {
				h := Hunk{
					Start:  lines[start].start,
					End:    lines[j].end,
					Ours:   data[lines[start].end:oursEnd],
					Theirs: data[theirsStart:lines[j].start],
				}
				if baseStart >= 0 && baseEnd >= 0 {
					h.Base = data[baseStart:baseEnd]
				}
				hunks = append(hunks, h)
				i = j
				found = true
				break
			}
		}
		if !found {
			continue
		}
	}
	return hunks
}

// countStartMarkers counts conflict-start lines, including ones Parse could
// not pair with a closer.
func countStartMarkers(data []byte) int {
	n := 0
	for _, line := range splitLines(data) {
		if markerLabel(line.text, "<<<<<<<") {
			n++
		}
	}
	return n
}

// hasConflictMarker reports whether s contains a conflict-marker line.
func hasConflictMarker(s string) bool {
	return countStartMarkers([]byte(s)) > 0 || markerLines([]byte(s))
}

func markerLines(data []byte) bool {
	for _, line := range splitLines(data) {
		if markerLabel(line.text, "=======") || markerLabel(line.text, ">>>>>>>") || markerLabel(line.text, "|||||||") {
			return true
		}
	}
	return false
}
