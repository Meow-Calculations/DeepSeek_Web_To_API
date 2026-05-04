package toolcall

import "strings"

// toolMarkupNames is the set of recognized tag names. Order matters: longer
// prefixes must come first so matchToolMarkupName picks the longest match
// (e.g. "tool_calls" before "tool_call"). Chinese variants ("工具调用", "调用",
// "参数") are emitted by some models (notably DeepSeek-v4-pro under high
// reasoning effort) and must be canonicalized back to English so the rest of
// the parser pipeline recognizes them.
var toolMarkupNames = []string{
	"tool_calls", "tool_call",
	"invoke", "parameter",
	"工具调用", "调用", "参数",
}

var toolMarkupTokenArtifacts = []string{
	"\u200b", // zero-width space
	"\u200c", // zero-width non-joiner
	"\u200d", // zero-width joiner
	"\u2060", // word joiner
	"\ufeff", // byte order mark / zero-width no-break space
	"\u2581", // lower one eighth block, often used as a tokenized-space marker
}

// canonicalToolMarkupName maps any recognized name (including Chinese / legacy
// variants) to the canonical English form used by the downstream XML parser.
func canonicalToolMarkupName(name string) string {
	switch name {
	case "工具调用", "tool_call":
		return "tool_calls"
	case "调用":
		return "invoke"
	case "参数":
		return "parameter"
	default:
		return name
	}
}

type ToolMarkupTag struct {
	Start       int
	End         int
	NameStart   int
	NameEnd     int
	Name        string
	Closing     bool
	SelfClosing bool
	DSMLLike    bool
	Canonical   bool
}

func ContainsToolMarkupSyntaxOutsideIgnored(text string) (hasDSML, hasCanonical bool) {
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return hasDSML, hasCanonical
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.DSMLLike {
				hasDSML = true
			} else {
				hasCanonical = true
			}
			if hasDSML && hasCanonical {
				return true, true
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return hasDSML, hasCanonical
}

func ContainsToolCallWrapperSyntaxOutsideIgnored(text string) (hasDSML, hasCanonical bool) {
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return hasDSML, hasCanonical
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.Name != "tool_calls" {
				i = tag.End + 1
				continue
			}
			if tag.DSMLLike {
				hasDSML = true
			} else {
				hasCanonical = true
			}
			if hasDSML && hasCanonical {
				return true, true
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return hasDSML, hasCanonical
}

func FindToolMarkupTagOutsideIgnored(text string, start int) (ToolMarkupTag, bool) {
	lower := strings.ToLower(text)
	for i := maxInt(start, 0); i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return ToolMarkupTag{}, false
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			return tag, true
		}
		i++
	}
	return ToolMarkupTag{}, false
}

func FindMatchingToolMarkupClose(text string, open ToolMarkupTag) (ToolMarkupTag, bool) {
	if text == "" || open.Name == "" || open.Closing {
		return ToolMarkupTag{}, false
	}
	depth := 1
	for pos := open.End + 1; pos < len(text); {
		tag, ok := FindToolMarkupTagOutsideIgnored(text, pos)
		if !ok {
			return ToolMarkupTag{}, false
		}
		if tag.Name != open.Name {
			pos = tag.End + 1
			continue
		}
		if tag.Closing {
			depth--
			if depth == 0 {
				return tag, true
			}
		} else if !tag.SelfClosing {
			depth++
		}
		pos = tag.End + 1
	}
	return ToolMarkupTag{}, false
}

func scanToolMarkupTagAt(text string, start int) (ToolMarkupTag, bool) {
	if start < 0 || start >= len(text) || text[start] != '<' {
		return ToolMarkupTag{}, false
	}
	lower := strings.ToLower(text)
	i := start + 1
	for i < len(text) && text[i] == '<' {
		i++
	}
	closing := false
	if i < len(text) && text[i] == '/' {
		closing = true
		i++
	}
	i, dsmlLike := consumeToolMarkupNamePrefix(lower, text, i)
	name, nameLen := matchToolMarkupName(lower, i)
	if nameLen == 0 {
		return ToolMarkupTag{}, false
	}
	nameEnd := i + nameLen
	nameEndBeforePipes := nameEnd
	for next, ok := consumeToolMarkupPipe(text, nameEnd); ok; next, ok = consumeToolMarkupPipe(text, nameEnd) {
		nameEnd = next
	}
	hasTrailingPipe := nameEnd > nameEndBeforePipes
	if !hasToolMarkupBoundary(text, nameEnd) {
		return ToolMarkupTag{}, false
	}
	end := findXMLTagEnd(text, nameEnd)
	if end < 0 {
		if !hasTrailingPipe {
			return ToolMarkupTag{}, false
		}
		end = nameEnd - 1
	}
	if hasTrailingPipe {
		if nextLT := strings.IndexByte(text[nameEnd:], '<'); nextLT >= 0 && end >= nameEnd+nextLT {
			end = nameEnd - 1
		}
	}
	trimmed := strings.TrimSpace(text[start : end+1])
	return ToolMarkupTag{
		Start:       start,
		End:         end,
		NameStart:   i,
		NameEnd:     nameEnd,
		Name:        name,
		Closing:     closing,
		SelfClosing: strings.HasSuffix(trimmed, "/>"),
		DSMLLike:    dsmlLike,
		Canonical:   !dsmlLike,
	}, true
}

func IsPartialToolMarkupTagPrefix(text string) bool {
	if text == "" || text[0] != '<' || strings.Contains(text, ">") {
		return false
	}
	lower := strings.ToLower(text)
	i := 1
	for i < len(text) && text[i] == '<' {
		i++
	}
	if i >= len(text) {
		return true
	}
	if text[i] == '/' {
		i++
	}
	dsmlLike := false
	for i <= len(text) {
		if i == len(text) {
			return true
		}
		if hasToolMarkupNamePrefix(lower[i:]) {
			return true
		}
		if strings.HasPrefix("dsml", lower[i:]) {
			return true
		}
		next, ok := consumeToolMarkupNamePrefixOnce(lower, text, i, dsmlLike)
		if !ok {
			return false
		}
		dsmlLike = true
		i = next
	}
	return false
}

func consumeToolMarkupNamePrefix(lower, text string, idx int) (int, bool) {
	dsmlLike := false
	for {
		next, ok := consumeToolMarkupNamePrefixOnce(lower, text, idx, dsmlLike)
		if !ok {
			return idx, dsmlLike
		}
		idx = next
		dsmlLike = true
	}
}

func consumeToolMarkupNamePrefixOnce(lower, text string, idx int, allowTokenArtifacts bool) (int, bool) {
	if next, ok := consumeToolMarkupPipe(text, idx); ok {
		return next, true
	}
	if next, ok := consumeToolMarkupSpaceSeparator(text, idx); ok {
		return next, true
	}
	if strings.HasPrefix(lower[idx:], "dsml") {
		return idx + len("dsml"), true
	}
	if allowTokenArtifacts {
		if next, ok := consumeToolMarkupTokenArtifact(text, idx); ok {
			return next, true
		}
	}
	return idx, false
}

func consumeToolMarkupSpaceSeparator(text string, idx int) (int, bool) {
	if idx >= len(text) {
		return idx, false
	}
	switch text[idx] {
	case ' ', '\t', '\r', '\n':
		return idx + 1, true
	}
	return idx, false
}

func consumeToolMarkupTokenArtifact(text string, idx int) (int, bool) {
	if idx >= len(text) {
		return idx, false
	}
	for _, artifact := range toolMarkupTokenArtifacts {
		if strings.HasPrefix(text[idx:], artifact) {
			return idx + len(artifact), true
		}
	}
	return idx, false
}

func hasToolMarkupNamePrefix(lowerTail string) bool {
	for _, name := range toolMarkupNames {
		if strings.HasPrefix(lowerTail, name) || strings.HasPrefix(name, lowerTail) {
			return true
		}
	}
	return false
}

func matchToolMarkupName(lower string, start int) (string, int) {
	for _, name := range toolMarkupNames {
		if strings.HasPrefix(lower[start:], name) {
			return name, len(name)
		}
	}
	return "", 0
}

func consumeToolMarkupPipe(text string, idx int) (int, bool) {
	if idx >= len(text) {
		return idx, false
	}
	if text[idx] == '|' {
		return idx + 1, true
	}
	if strings.HasPrefix(text[idx:], "｜") {
		return idx + len("｜"), true
	}
	return idx, false
}

func hasToolMarkupBoundary(text string, idx int) bool {
	if idx >= len(text) {
		return true
	}
	switch text[idx] {
	case ' ', '\t', '\n', '\r', '>', '/', '|':
		return true
	}
	return strings.HasPrefix(text[idx:], "｜")
}
