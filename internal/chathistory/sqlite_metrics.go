package chathistory

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *sqliteStore) TokenUsageStats(window time.Duration) (TokenUsageStats, error) {
	if s == nil {
		return TokenUsageStats{}, errors.New("chat history sqlite store is nil")
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
	tx, err := s.db.Begin()
	if err != nil {
		return TokenUsageStats{}, fmt.Errorf("begin chat history sqlite token stats: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stats := TokenUsageStats{
		WindowSeconds: int64(window.Seconds()),
		WindowByModel: map[string]TokenUsageTotals{},
		TotalByModel:  map[string]TokenUsageTotals{},
	}
	rollupTotal, rollupByModel, err := s.tokenRollupLocked(tx)
	if err != nil {
		return TokenUsageStats{}, err
	}
	stats.Total.add(rollupTotal)
	for model, usage := range rollupByModel {
		addModelTotals(stats.TotalByModel, model, usage)
	}
	rows, err := tx.Query(`SELECT model, created_at, updated_at, completed_at, usage_json FROM chat_history`)
	if err != nil {
		return TokenUsageStats{}, fmt.Errorf("read chat history sqlite token stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var model string
		var createdAt, updatedAt, completedAt int64
		var usageJSON string
		if err := rows.Scan(&model, &createdAt, &updatedAt, &completedAt, &usageJSON); err != nil {
			return TokenUsageStats{}, fmt.Errorf("scan chat history sqlite token stats: %w", err)
		}
		// Use a single canonical timestamp for window membership so the
		// request counter and token aggregates always agree. completedAt is
		// the moment tokens were finalized/billed; for in-flight rows that
		// have not finished yet, fall back to createdAt so they still count
		// in the most recent windows. Earlier code split these two checks
		// across createdAt vs updatedAt|completedAt, which made e.g. an old
		// row whose metadata was rewritten today contribute tokens to the
		// 24h window without contributing a request — producing the 24h /
		// 7d / 15d skew seen in the admin metrics overview.
		_ = updatedAt
		refTime := completedAt
		if refTime <= 0 {
			refTime = createdAt
		}
		inWindow := refTime >= windowStart
		if inWindow {
			stats.WindowRequests++
		}
		usage := tokenUsageFromMap(decodeUsageJSON(usageJSON))
		if usage.TotalTokens <= 0 && usage.InputTokens <= 0 && usage.OutputTokens <= 0 {
			continue
		}
		usage.Requests = 1
		metricModel := normalizedMetricModel(model)
		stats.Total.add(usage)
		addModelTotals(stats.TotalByModel, metricModel, usage)
		if inWindow {
			stats.Window.add(usage)
			addModelTotals(stats.WindowByModel, metricModel, usage)
		}
	}
	if err := rows.Err(); err != nil {
		return TokenUsageStats{}, fmt.Errorf("scan chat history sqlite token stat rows: %w", err)
	}
	return stats, nil
}

func (s *sqliteStore) OutcomeStats() (OutcomeStats, error) {
	if s == nil {
		return OutcomeStats{}, errors.New("chat history sqlite store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return OutcomeStats{}, s.err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return OutcomeStats{}, fmt.Errorf("begin chat history sqlite outcome stats: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stats, err := s.outcomeRollupLocked(tx)
	if err != nil {
		return OutcomeStats{}, err
	}
	rows, err := tx.Query(`SELECT status, status_code, finish_reason FROM chat_history`)
	if err != nil {
		return OutcomeStats{}, fmt.Errorf("read chat history sqlite outcome stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var item SummaryEntry
		if err := rows.Scan(&item.Status, &item.StatusCode, &item.FinishReason); err != nil {
			return OutcomeStats{}, fmt.Errorf("scan chat history sqlite outcome stats: %w", err)
		}
		stats.addSummary(item)
		stats.RetainedTotal++
	}
	if err := rows.Err(); err != nil {
		return OutcomeStats{}, fmt.Errorf("scan chat history sqlite outcome stat rows: %w", err)
	}
	stats.finalize()
	return stats, nil
}

func (s *sqliteStore) outcomeRollupLocked(tx *sql.Tx) (OutcomeStats, error) {
	stats := newOutcomeStats()
	for _, item := range []struct {
		key string
		dst *int64
	}{
		{sqliteMetaPrunedTotal, &stats.Total},
		{sqliteMetaPrunedSuccess, &stats.Success},
		{sqliteMetaPrunedFailed, &stats.Failed},
		{sqliteMetaPrunedActive, &stats.Active},
		{sqliteMetaPrunedNeutral, &stats.Neutral},
		{sqliteMetaPrunedExcludedRate, &stats.ExcludedFromFailureRate},
	} {
		value, err := s.metaInt64Locked(tx, item.key, 0)
		if err != nil {
			return OutcomeStats{}, err
		}
		*item.dst = value
	}
	stats.PrunedTotal = stats.Total
	return stats, nil
}
