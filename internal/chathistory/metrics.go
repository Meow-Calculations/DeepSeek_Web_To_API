package chathistory

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

type TokenUsageTotals struct {
	Requests             int64 `json:"requests"`
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheHitInputTokens  int64 `json:"cache_hit_input_tokens"`
	CacheMissInputTokens int64 `json:"cache_miss_input_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
}

type TokenUsageStats struct {
	WindowSeconds  int64                       `json:"window_seconds"`
	WindowRequests int64                       `json:"window_requests"`
	Window         TokenUsageTotals            `json:"window"`
	Total          TokenUsageTotals            `json:"total"`
	WindowByModel  map[string]TokenUsageTotals `json:"window_by_model"`
	TotalByModel   map[string]TokenUsageTotals `json:"total_by_model"`
}

var FailureRateExcludedStatusCodes = []int{401, 403, 502, 504, 524}

type OutcomeStats struct {
	Total                   int64   `json:"total"`
	RetainedTotal           int64   `json:"retained_total,omitempty"`
	PrunedTotal             int64   `json:"pruned_total,omitempty"`
	Success                 int64   `json:"success"`
	Failed                  int64   `json:"failed"`
	Active                  int64   `json:"active"`
	Neutral                 int64   `json:"neutral"`
	ExcludedFromFailureRate int64   `json:"excluded_from_failure_rate"`
	EligibleTotal           int64   `json:"eligible_total"`
	SuccessRate             float64 `json:"success_rate"`
	ExcludedStatusCodes     []int   `json:"excluded_status_codes"`
}

func (s *Store) TokenUsageStats(window time.Duration) (TokenUsageStats, error) {
	if s == nil {
		return TokenUsageStats{}, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.TokenUsageStats(window)
	}
	if window <= 0 {
		window = time.Minute
	}
	now := time.Now().UnixMilli()
	windowStart := now - window.Milliseconds()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return TokenUsageStats{}, s.err
	}

	stats := TokenUsageStats{
		WindowSeconds: int64(window.Seconds()),
		WindowByModel: map[string]TokenUsageTotals{},
		TotalByModel:  map[string]TokenUsageTotals{},
	}
	for _, summary := range s.state.Items {
		item := entryFromSummary(summary)
		if cached, ok := s.details[summary.ID]; ok && strings.TrimSpace(cached.ID) != "" {
			item = cached
		}
		model := normalizedMetricModel(item.Model)
		// Single canonical timestamp for window membership so the request
		// counter and token aggregates always agree. See sqlite_metrics.go
		// for the longer rationale.
		refTime := item.CompletedAt
		if refTime <= 0 {
			refTime = item.CreatedAt
		}
		inWindow := refTime >= windowStart
		if inWindow {
			stats.WindowRequests++
		}

		usage := tokenUsageFromMap(item.Usage)
		if usage.TotalTokens <= 0 && usage.InputTokens <= 0 && usage.OutputTokens <= 0 {
			continue
		}
		usage.Requests = 1
		stats.Total.add(usage)
		addModelTotals(stats.TotalByModel, model, usage)
		if inWindow {
			stats.Window.add(usage)
			addModelTotals(stats.WindowByModel, model, usage)
		}
	}
	return stats, nil
}

func (s *Store) OutcomeStats() (OutcomeStats, error) {
	if s == nil {
		return OutcomeStats{}, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.OutcomeStats()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return OutcomeStats{}, s.err
	}
	stats := newOutcomeStats()
	for _, summary := range s.state.Items {
		stats.addSummary(summary)
	}
	stats.finalize()
	return stats, nil
}

func (t *TokenUsageTotals) add(next TokenUsageTotals) {
	t.Requests += next.Requests
	t.InputTokens += next.InputTokens
	t.OutputTokens += next.OutputTokens
	t.CacheHitInputTokens += next.CacheHitInputTokens
	t.CacheMissInputTokens += next.CacheMissInputTokens
	t.TotalTokens += next.TotalTokens
}

func addModelTotals(target map[string]TokenUsageTotals, model string, usage TokenUsageTotals) {
	current := target[model]
	current.add(usage)
	target[model] = current
}

func tokenUsageFromMap(usage map[string]any) TokenUsageTotals {
	if len(usage) == 0 {
		return TokenUsageTotals{}
	}

	input := firstUsageInt(usage, "prompt_tokens", "input_tokens")
	output := firstUsageInt(usage, "completion_tokens", "output_tokens")
	total := firstUsageInt(usage, "total_tokens")
	if input <= 0 && total > 0 && output > 0 {
		input = total - output
	}
	if output <= 0 && total > 0 && input > 0 {
		output = total - input
	}
	if total <= 0 {
		total = input + output
	}

	cacheHit := firstUsageInt(usage,
		"prompt_cache_hit_tokens",
		"input_cache_hit_tokens",
		"cache_hit_input_tokens",
		"cache_hit_tokens",
	)
	cacheMiss := firstUsageInt(usage,
		"prompt_cache_miss_tokens",
		"input_cache_miss_tokens",
		"cache_miss_input_tokens",
		"cache_miss_tokens",
	)
	if cacheHit <= 0 {
		cacheHit = nestedUsageInt(usage, "prompt_tokens_details", "cached_tokens")
	}
	if cacheMiss <= 0 && input > 0 {
		cacheMiss = input - cacheHit
	}
	if cacheMiss < 0 {
		cacheMiss = 0
	}

	return TokenUsageTotals{
		InputTokens:          maxInt64(input, 0),
		OutputTokens:         maxInt64(output, 0),
		CacheHitInputTokens:  maxInt64(cacheHit, 0),
		CacheMissInputTokens: maxInt64(cacheMiss, 0),
		TotalTokens:          maxInt64(total, 0),
	}
}

func firstUsageInt(usage map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if n, ok := usageInt(usage[key]); ok {
			return n
		}
	}
	return 0
}

func nestedUsageInt(usage map[string]any, parentKey, childKey string) int64 {
	parent, ok := usage[parentKey].(map[string]any)
	if !ok {
		return 0
	}
	n, _ := usageInt(parent[childKey])
	return n
}

func usageInt(value any) (int64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, false
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case uint:
		if uint64(v) > math.MaxInt64 {
			return math.MaxInt64, true
		}
		return int64(v), true
	case uint64:
		if v > math.MaxInt64 {
			return math.MaxInt64, true
		}
		return int64(v), true
	case float64:
		return int64(v), true
	case float32:
		return int64(v), true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}
		if f, err := v.Float64(); err == nil {
			return int64(f), true
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int64(f), true
		}
	}
	return 0, false
}

func normalizedMetricModel(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))
	if model == "" {
		return "deepseek-v4-flash"
	}
	return model
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func newOutcomeStats() OutcomeStats {
	return OutcomeStats{
		ExcludedStatusCodes: append([]int(nil), FailureRateExcludedStatusCodes...),
	}
}

func (s *OutcomeStats) addSummary(item SummaryEntry) {
	s.addSummaryCount(item, 1)
}

func (s *OutcomeStats) addSummaryCount(item SummaryEntry, count int64) {
	if count <= 0 {
		return
	}
	s.Total += count
	status := strings.ToLower(strings.TrimSpace(item.Status))
	switch status {
	case "success":
		s.Success += count
	case "queued", "streaming":
		s.Active += count
	case "error", "stopped":
		if IsFailureRateExcludedStatusCode(item.StatusCode) {
			s.ExcludedFromFailureRate += count
			return
		}
		if isNeutralFailureReason(item.FinishReason) {
			s.Neutral += count
			return
		}
		s.Failed += count
	default:
		s.Neutral += count
	}
}

func (s *OutcomeStats) finalize() {
	if len(s.ExcludedStatusCodes) == 0 {
		s.ExcludedStatusCodes = append([]int(nil), FailureRateExcludedStatusCodes...)
	}
	s.EligibleTotal = s.Success + s.Failed
	if s.EligibleTotal <= 0 {
		s.SuccessRate = 100
		return
	}
	s.SuccessRate = math.Round(float64(s.Success)*10000/float64(s.EligibleTotal)) / 100
}

func IsFailureRateExcludedStatusCode(statusCode int) bool {
	for _, code := range FailureRateExcludedStatusCodes {
		if statusCode == code {
			return true
		}
	}
	return false
}

func isNeutralFailureReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "context_cancelled", "server_restart":
		return true
	default:
		return false
	}
}
