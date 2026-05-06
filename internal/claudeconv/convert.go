package claudeconv

import (
	"strings"

	"DeepSeek_Web_To_API/internal/config"
)

func ConvertClaudeToDeepSeek(claudeReq map[string]any, aliasProvider config.ModelAliasReader, defaultClaudeModel string) map[string]any {
	messages, _ := claudeReq["messages"].([]any)
	model, _ := claudeReq["model"].(string)
	if model == "" {
		model = defaultClaudeModel
	}

	dsModel, ok := config.ResolveModel(aliasProvider, model)
	if !ok || strings.TrimSpace(dsModel) == "" {
		dsModel = "deepseek-v4-flash"
	}

	// Cap the capacity hint so CodeQL go/allocation-size-overflow does
	// not flag len()+1 as a potential overflow. Real conversations are
	// nowhere near this; the cap is purely a static-analysis tell.
	capHint := len(messages) + 1
	if capHint < 0 || capHint > 1<<20 {
		capHint = 1 << 20
	}
	convertedMessages := make([]any, 0, capHint)
	if system := claudeSystemText(claudeReq["system"]); system != "" {
		convertedMessages = append(convertedMessages, map[string]any{"role": "system", "content": system})
	}
	convertedMessages = append(convertedMessages, messages...)

	out := map[string]any{"model": dsModel, "messages": convertedMessages}
	for _, k := range []string{"temperature", "top_p", "stream"} {
		if v, ok := claudeReq[k]; ok {
			out[k] = v
		}
	}
	if stopSeq, ok := claudeReq["stop_sequences"]; ok {
		out["stop"] = stopSeq
	}
	return out
}

func claudeSystemText(system any) string {
	switch v := system.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		return strings.TrimSpace(asString(v["text"]))
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch block := item.(type) {
			case string:
				if text := strings.TrimSpace(block); text != "" {
					parts = append(parts, text)
				}
			case map[string]any:
				if text := strings.TrimSpace(asString(block["text"])); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
