package diff

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	lineEndingOnly      = "line endings differ; textual content is otherwise identical\n"
	trailingNewlineOnly = "trailing newline differs; textual content is otherwise identical\n"

	// DefaultFileMode is the Ignition/MCD default when a file omits mode (0644).
	DefaultFileMode = 0o644
)

// Result is a structured comparison of expected rendered bytes vs actual bytes.
// This is the engine later tasks will reuse for --node and must-gather.
type Result struct {
	Match        bool
	ExpectedSize int
	ActualSize   int
	UnifiedDiff  string
	ExpectedMode *int
	ActualMode   *int
	// ModeMatch is true when modes are equal, when actual mode is unknown, or
	// when Compare was called without mode information.
	ModeMatch bool
}

// Compare reports whether actual matches expected.
//
// Match is a raw byte comparison (the same standard the MCD uses on disk).
// UnifiedDiff is generated after normalizing CRLF/CR to LF so line-ending-only
// drift does not produce a noisy every-line diff. Trailing-newline-only drift
// (common on /etc/resolv.conf and /etc/chrony.conf) is reported as a one-line
// message instead of a full-file rewrite. Sizes always reflect the original
// byte lengths.
func Compare(expected, actual []byte, expectedName, actualName string) Result {
	if expectedName == "" {
		expectedName = "expected"
	}
	if actualName == "" {
		actualName = "actual"
	}

	out := Result{
		Match:        bytes.Equal(expected, actual),
		ExpectedSize: len(expected),
		ActualSize:   len(actual),
		ModeMatch:    true,
	}
	if out.Match {
		return out
	}

	expN := normalizeNewlines(expected)
	actN := normalizeNewlines(actual)
	if bytes.Equal(expN, actN) {
		out.UnifiedDiff = lineEndingOnly
		return out
	}
	if bytes.Equal(bytes.TrimRight(expN, "\n"), bytes.TrimRight(actN, "\n")) {
		out.UnifiedDiff = trailingNewlineOnly
		return out
	}

	ud, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(expN)),
		B:        difflib.SplitLines(string(actN)),
		FromFile: expectedName,
		ToFile:   actualName,
		Context:  3,
		Eol:      "\n",
	})
	if err != nil {
		out.UnifiedDiff = fmt.Sprintf("failed to generate unified diff: %v\n", err)
		return out
	}
	out.UnifiedDiff = annotateMissingNewline(ud, expN, actN)
	return out
}

// WithModes records expected vs actual file modes on a content comparison.
// A nil actual mode is treated as unknown (ModeMatch stays true) so missing
// stat data does not invent a mismatch. A nil expected mode uses DefaultFileMode,
// matching the MCD on-disk check.
func WithModes(r Result, expected, actual *int) Result {
	r.ExpectedMode = copyMode(expected)
	r.ActualMode = copyMode(actual)
	r.ModeMatch = ModesMatch(expected, actual)
	return r
}

// ModesMatch reports whether permission bits agree. Unknown actual mode matches.
func ModesMatch(expected, actual *int) bool {
	if actual == nil {
		return true
	}
	return perm(EffectiveMode(expected)) == perm(*actual)
}

// EffectiveMode returns the mode the MCD would enforce: explicit Ignition mode,
// or DefaultFileMode when omitted.
func EffectiveMode(mode *int) int {
	if mode == nil {
		return DefaultFileMode
	}
	return *mode
}

func perm(mode int) int {
	return mode & 0o7777
}

func copyMode(mode *int) *int {
	if mode == nil {
		return nil
	}
	copied := *mode
	return &copied
}

func annotateMissingNewline(ud string, expected, actual []byte) string {
	if ud == "" {
		return ud
	}
	var b strings.Builder
	b.WriteString(ud)
	if !bytes.HasSuffix(expected, []byte("\n")) || !bytes.HasSuffix(actual, []byte("\n")) {
		if !strings.HasSuffix(ud, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("\\ No newline at end of file\n")
	}
	return b.String()
}

func normalizeNewlines(b []byte) []byte {
	s := string(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return []byte(s)
}
