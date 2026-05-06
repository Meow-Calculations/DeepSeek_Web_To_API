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

	// Compute a bounded capacity hint without overflow risk.
	const maxMessageCapHint = 1 << 20
	msgLen := len(messages)
	capHint := maxMessageCapHint
	if msgLen < maxMessageCapHint {
		if msgLen == maxMessageCapHint-1 {
			capHint = maxMessageCapHint
		} else {
			capHint = msgLen
			capHint++
		}
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
