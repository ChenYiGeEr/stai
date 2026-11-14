package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"stai/internal/i18n"
)

// SuggestMergeHunk asks the model for one conflict hunk. A provider error
// or an unusable reply is returned as an error so the caller can keep
// ours/theirs/editor available and avoid writing a model guess.
func (c *Client) SuggestMergeHunk(ctx context.Context, base, ours, theirs string) (explanation, choice, resolution string, err error) {
	raw, err := c.Chat(ctx, mergeSystemPrompt(), mergeUserPrompt(base, ours, theirs))
	if err != nil {
		return "", "", "", err
	}
	return ParseMergeAdvice(raw)
}

// ParseMergeAdvice reads the mergetool schema:
//
//	EXPLANATION: ...
//	CHOICE: ours|theirs|merge
//	RESOLUTION:
//	<replacement, only when CHOICE is merge>
//
// ours and theirs discard any RESOLUTION body. A merge resolution that
// contains conflict-marker lines is unusable.
func ParseMergeAdvice(raw string) (explanation, choice, resolution string, err error) {
	raw = stripFence(raw)
	var expLines, resLines []string
	phase := ""
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "EXPLANATION:"):
			phase = "exp"
			if rest := strings.TrimSpace(strings.TrimPrefix(line, "EXPLANATION:")); rest != "" {
				expLines = append(expLines, rest)
			}
		case strings.HasPrefix(line, "CHOICE:"):
			phase = "choice"
			choice = strings.TrimSpace(strings.TrimPrefix(line, "CHOICE:"))
		case strings.HasPrefix(line, "RESOLUTION:"):
			phase = "res"
			rest := strings.TrimPrefix(line, "RESOLUTION:")
			if strings.TrimSpace(rest) != "" {
				resLines = append(resLines, strings.TrimPrefix(rest, " "))
			}
		default:
			switch phase {
			case "exp":
				expLines = append(expLines, line)
			case "res":
				resLines = append(resLines, line)
			}
		}
	}
	explanation = strings.TrimSpace(strings.Join(expLines, "\n"))
	switch choice {
	case "ours", "theirs":
		return explanation, choice, "", nil
	case "merge":
		resolution = strings.Join(resLines, "\n")
		if resolution != "" && !strings.HasSuffix(resolution, "\n") {
			resolution += "\n"
		}
		if strings.TrimSpace(resolution) == "" || adviceHasMarker(resolution) {
			return "", "", "", errors.New(i18n.T("mergetool_ai_unusable"))
		}
		return explanation, choice, resolution, nil
	default:
		return "", "", "", errors.New(i18n.T("mergetool_ai_unusable"))
	}
}

func adviceHasMarker(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "<<<<<<<" || strings.HasPrefix(line, "<<<<<<< ") ||
			line == "|||||||" || strings.HasPrefix(line, "||||||| ") ||
			line == "=======" ||
			line == ">>>>>>>" || strings.HasPrefix(line, ">>>>>>> ") {
			return true
		}
	}
	return false
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		return ""
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

const mergeSideLimit = 20000

func mergeSystemPrompt() string {
	if langIsEnglish() {
		return `You resolve a single git merge-conflict hunk. Obey these rules:
1. The EXPLANATION must be English.
2. Output exactly this schema and nothing else. Do not wrap it in a code fence:
EXPLANATION: <one short paragraph>
CHOICE: <ours or theirs or merge>
RESOLUTION:
<only when CHOICE is merge: the exact replacement lines; otherwise leave this empty>
3. ours means keep the OURS side. theirs means keep the THEIRS side. merge means RESOLUTION replaces the whole conflict hunk.
4. RESOLUTION uses newline line endings and must not contain conflict markers (<<<<<<<, |||||||, =======, >>>>>>>).`
	}
	return `你只处理一个 git 合并冲突块。严格遵守:
1. EXPLANATION 必须是中文。
2. 只输出下面的结构,不要代码围栏,不要额外段落:
EXPLANATION: <一小段说明>
CHOICE: <ours 或 theirs 或 merge>
RESOLUTION:
<仅当 CHOICE 为 merge 时给出精确替换文本;否则留空>
3. ours 表示保留 OURS 一侧。theirs 表示保留 THEIRS 一侧。merge 表示 RESOLUTION 替换整个冲突块。
4. RESOLUTION 使用换行,且不得包含冲突标记 (<<<<<<<, |||||||, =======, >>>>>>>)。`
}

func mergeUserPrompt(base, ours, theirs string) string {
	lead := "请处理下面这一个冲突块。"
	if langIsEnglish() {
		lead = "Resolve this conflict hunk."
	}
	var b strings.Builder
	b.WriteString(lead)
	b.WriteString("\n\n")
	writeMergeSide(&b, "BASE", base)
	writeMergeSide(&b, "OURS", ours)
	writeMergeSide(&b, "THEIRS", theirs)
	return b.String()
}

func writeMergeSide(b *strings.Builder, name, body string) {
	clipped := false
	if len(body) > mergeSideLimit {
		body = truncateUTF8(body, mergeSideLimit)
		clipped = true
	}
	fmt.Fprintf(b, "%s:\n", name)
	if body == "" {
		b.WriteString("(none)\n\n")
		return
	}
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	if clipped {
		b.WriteString("(Note: this side was truncated.)\n")
	}
	b.WriteByte('\n')
}
