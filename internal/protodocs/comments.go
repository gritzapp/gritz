package protodocs

import (
	"errors"
	"regexp"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
)

// docLines is an element's documentation: its leading comment, then its
// trailing comment as another paragraph. Leading detached comments are
// contributor notes and are left out. Each line has the space after the
// comment marker stripped.
func docLines(cs protogen.CommentSet) []string {
	lines := commentLines(cs.Leading)
	if trailing := commentLines(cs.Trailing); len(trailing) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, trailing...)
	}
	return lines
}

func commentLines(c protogen.Comments) []string {
	s := strings.TrimSuffix(string(c), "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, " ")
	}
	return lines
}

// paragraphs splits lines at blank lines.
func paragraphs(lines []string) [][]string {
	var paras [][]string
	var cur []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				paras = append(paras, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		paras = append(paras, cur)
	}
	return paras
}

// blockMarkdown renders a comment as block Markdown, passed through as written.
func blockMarkdown(lines []string) string {
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// firstSentence is a comment's first sentence, up to the first ". " or the end
// of the first paragraph, on one line.
func firstSentence(lines []string) string {
	paras := paragraphs(lines)
	if len(paras) == 0 {
		return ""
	}
	p := joinLines(paras[0])
	if i := strings.Index(p, ". "); i >= 0 {
		return p[:i+1]
	}
	return p
}

// blockSyntax matches the start of a line that begins a Markdown block other
// than a paragraph, which a table cell can't hold.
var blockSyntax = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`^ {0,3}#{1,6}(\s|$)`), "a heading"},
	{regexp.MustCompile(`^ {0,3}(=+|-+)\s*$`), "a heading underline or thematic break"},
	{regexp.MustCompile(`^ {0,3}([-*+]|\d{1,9}[.)])(\s|$)`), "a list item"},
	{regexp.MustCompile("^ {0,3}(```|~~~)"), "a fenced code block"},
	{regexp.MustCompile(`^ {0,3}>`), "a block quote"},
}

// indentedCode matches the first line of an indented code block.
var indentedCode = regexp.MustCompile(`^( {4}|\t)`)

// cellMarkdown renders a comment as inline Markdown for a table cell: lines
// joined with spaces, paragraphs with <br><br>, and `|` escaped. A comment with
// block syntax is an error, as the table would break.
func cellMarkdown(lines []string) (string, error) {
	var out []string
	for _, p := range paragraphs(lines) {
		if indentedCode.MatchString(p[0]) {
			return "", errors.New("comment has an indented code block, which a table cell can't hold: " + strings.TrimSpace(p[0]))
		}
		for _, l := range p {
			for _, s := range blockSyntax {
				if s.re.MatchString(l) {
					return "", errors.New("comment has " + s.name + ", which a table cell can't hold: " + strings.TrimSpace(l))
				}
			}
		}
		out = append(out, joinLines(p))
	}
	return strings.ReplaceAll(strings.Join(out, "<br><br>"), "|", `\|`), nil
}

// cellDescription is a Description cell: the notes, then the comment.
func cellDescription(notes []string, comments protogen.CommentSet) (string, error) {
	text, err := cellMarkdown(docLines(comments))
	if err != nil {
		return "", err
	}
	if text != "" {
		notes = append(notes, text)
	}
	return strings.Join(notes, " "), nil
}

func joinLines(lines []string) string {
	trimmed := make([]string, len(lines))
	for i, l := range lines {
		trimmed[i] = strings.TrimSpace(l)
	}
	return strings.Join(trimmed, " ")
}
