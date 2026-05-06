package chathistory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/util"
)

const (
	FileVersion             = 2
	DisabledLimit           = 0
	DefaultLimit            = 20_000
	MaxLimit                = 20_000
	BurstPruneRetainedLimit = 500
	defaultPreviewAt        = 160
)

var allowedLimits = map[int]struct{}{
	DisabledLimit: {},
	10:            {},
	20:            {},
	50:            {},
	MaxLimit:      {},
}

var ErrDisabled = errors.New("chat history disabled")

type Entry struct {
	ID               string         `json:"id"`
	Revision         int64          `json:"revision"`
	CreatedAt        int64          `json:"created_at"`
	UpdatedAt        int64          `json:"updated_at"`
	CompletedAt      int64          `json:"completed_at,omitempty"`
	Status           string         `json:"status"`
	CallerID         string         `json:"caller_id,omitempty"`
	AccountID        string         `json:"account_id,omitempty"`
	RequestIP        string         `json:"request_ip,omitempty"`
	ConversationID   string         `json:"conversation_id,omitempty"`
	Model            string         `json:"model,omitempty"`
	Stream           bool           `json:"stream"`
	UserInput        string         `json:"user_input,omitempty"`
	Messages         []Message      `json:"messages,omitempty"`
	HistoryText      string         `json:"history_text,omitempty"`
	FinalPrompt      string         `json:"final_prompt,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	Content          string         `json:"content,omitempty"`
	Error            string         `json:"error,omitempty"`
	StatusCode       int            `json:"status_code,omitempty"`
	ElapsedMs        int64          `json:"elapsed_ms,omitempty"`
	FinishReason     string         `json:"finish_reason,omitempty"`
	Usage            map[string]any `json:"usage,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type SummaryEntry struct {
	ID             string         `json:"id"`
	Revision       int64          `json:"revision"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
	CompletedAt    int64          `json:"completed_at,omitempty"`
	Status         string         `json:"status"`
	CallerID       string         `json:"caller_id,omitempty"`
	AccountID      string         `json:"account_id,omitempty"`
	RequestIP      string         `json:"request_ip,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	Model          string         `json:"model,omitempty"`
	Stream         bool           `json:"stream"`
	UserInput      string         `json:"user_input,omitempty"`
	Preview        string         `json:"preview,omitempty"`
	StatusCode     int            `json:"status_code,omitempty"`
	ElapsedMs      int64          `json:"elapsed_ms,omitempty"`
	FinishReason   string         `json:"finish_reason,omitempty"`
	DetailRevision int64          `json:"detail_revision"`
	Usage          map[string]any `json:"usage,omitempty"`
}

type File struct {
	Version  int            `json:"version"`
	Limit    int            `json:"limit"`
	Revision int64          `json:"revision"`
	Items    []SummaryEntry `json:"items"`
}

type StartParams struct {
	CallerID       string
	AccountID      string
	RequestIP      string
	ConversationID string
	Status         string
	Model          string
	Stream         bool
	UserInput      string
	Messages       []Message
	HistoryText    string
	FinalPrompt    string
}

type UpdateParams struct {
	CallerID         string
	AccountID        string
	Status           string
	HistoryText      string
	ReasoningContent string
	Content          string
	Error            string
	StatusCode       int
	ElapsedMs        int64
	FinishReason     string
	Usage            map[string]any
	Completed        bool
}

type detailEnvelope struct {
	Version int   `json:"version"`
	Item    Entry `json:"item"`
}

type legacyFile struct {
	Version int     `json:"version"`
	Limit   int     `json:"limit"`
	Items   []Entry `json:"items"`
}

type legacyProbe struct {
	Items []map[string]json.RawMessage `json:"items"`
}

type Store struct {
	sqlite    *sqliteStore
	mu        sync.Mutex
	path      string
	detailDir string
	state     File
	details   map[string]Entry
	dirty     map[string]struct{}
	deleted   map[string]struct{}
	err       error
}

func New(path string) *Store {
	s := &Store{
		path:      strings.TrimSpace(path),
		detailDir: strings.TrimSpace(path) + ".d",
		state: File{
			Version:  FileVersion,
			Limit:    DefaultLimit,
			Revision: 0,
			Items:    []SummaryEntry{},
		},
		details: map[string]Entry{},
		dirty:   map[string]struct{}{},
		deleted: map[string]struct{}{},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = s.loadLocked()
	return s
}

func NewSQLite(path, legacyPath string) *Store {
	return NewSQLiteWithTokenStats(path, legacyPath, "")
}

// NewSQLiteWithTokenStats opens chat history as SQLite and additionally wires
// up a dedicated token-usage SQLite at tokenStatsPath. When tokenStatsPath is
// empty the store falls back to the legacy chat_history_meta rollup keys.
func NewSQLiteWithTokenStats(path, legacyPath, tokenStatsPath string) *Store {
	path = strings.TrimSpace(path)
	legacyPath = strings.TrimSpace(legacyPath)
	tokenStatsPath = strings.TrimSpace(tokenStatsPath)
	s := &Store{
		path:      path,
		detailDir: legacyPath + ".d",
		state: File{
			Version:  FileVersion,
			Limit:    DefaultLimit,
			Revision: 0,
			Items:    []SummaryEntry{},
		},
		details: map[string]Entry{},
		dirty:   map[string]struct{}{},
		deleted: map[string]struct{}{},
	}
	sqliteStore, err := newSQLiteStore(path, legacyPath, tokenStatsPath)
	if err != nil {
		s.err = err
		return s
	}
	s.sqlite = sqliteStore
	return s
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	if s.sqlite != nil {
		return s.sqlite.Path()
	}
	return s.path
}

func (s *Store) DetailDir() string {
	if s == nil {
		return ""
	}
	return s.detailDir
}

func (s *Store) Close() error {
	if s == nil || s.sqlite == nil {
		return nil
	}
	return s.sqlite.Close()
}

func (s *Store) Err() error {
	if s == nil {
		return errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Store) Snapshot() (File, error) {
	if s == nil {
		return File{}, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Snapshot()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return File{}, s.err
	}
	return cloneFile(s.state), nil
}

func (s *Store) SnapshotPage(offset, limit int) (File, int, error) {
	if s == nil {
		return File{}, 0, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.SnapshotPage(offset, limit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return File{}, 0, s.err
	}
	total := len(s.state.Items)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	pageItems := make([]SummaryEntry, end-offset)
	copy(pageItems, s.state.Items[offset:end])
	return File{
		Version:  s.state.Version,
		Limit:    s.state.Limit,
		Revision: s.state.Revision,
		Items:    pageItems,
	}, total, nil
}

func (s *Store) Revision() (int64, error) {
	if s == nil {
		return 0, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Revision()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	return s.state.Revision, nil
}

func (s *Store) Enabled() bool {
	if s == nil {
		return false
	}
	if s.sqlite != nil {
		return s.sqlite.Enabled()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false
	}
	return s.state.Limit != DisabledLimit
}

func (s *Store) Get(id string) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Get(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return Entry{}, s.err
	}
	target := strings.TrimSpace(id)
	if target == "" {
		return Entry{}, errors.New("history id is required")
	}
	item, err := s.readDetailLocked(target)
	if err != nil {
		return Entry{}, err
	}
	return cloneEntry(item), nil
}

func (s *Store) DetailRevision(id string) (int64, error) {
	if s == nil {
		return 0, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.DetailRevision(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	target := strings.TrimSpace(id)
	summary, _, ok := s.findSummaryLocked(target)
	if !ok {
		return 0, errors.New("chat history entry not found")
	}
	return summary.DetailRevision, nil
}

func (s *Store) Start(params StartParams) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Start(params)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return Entry{}, s.err
	}
	if s.state.Limit == DisabledLimit {
		return Entry{}, ErrDisabled
	}
	now := time.Now().UnixMilli()
	revision := s.nextRevisionLocked()
	status := strings.TrimSpace(params.Status)
	if status == "" {
		status = "streaming"
	}
	entry := Entry{
		ID:             "chat_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Revision:       revision,
		CreatedAt:      now,
		UpdatedAt:      now,
		Status:         status,
		CallerID:       strings.TrimSpace(params.CallerID),
		AccountID:      strings.TrimSpace(params.AccountID),
		RequestIP:      strings.TrimSpace(params.RequestIP),
		ConversationID: strings.TrimSpace(params.ConversationID),
		Model:          strings.TrimSpace(params.Model),
		Stream:         params.Stream,
		UserInput:      strings.TrimSpace(params.UserInput),
		Messages:       cloneMessages(params.Messages),
		HistoryText:    params.HistoryText,
		FinalPrompt:    strings.TrimSpace(params.FinalPrompt),
	}
	s.details[entry.ID] = entry
	s.markDetailDirtyLocked(entry.ID)
	s.rebuildIndexLocked()
	if err := s.saveLocked(); err != nil {
		return cloneEntry(entry), err
	}
	return cloneEntry(entry), nil
}

func (s *Store) Update(id string, params UpdateParams) (Entry, error) {
	if s == nil {
		return Entry{}, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Update(id, params)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return Entry{}, s.err
	}
	target := strings.TrimSpace(id)
	if target == "" {
		return Entry{}, errors.New("history id is required")
	}
	item, err := s.loadDetailForUpdateLocked(target)
	if err != nil {
		return Entry{}, err
	}
	now := time.Now().UnixMilli()
	item.Revision = s.nextRevisionLocked()
	item.UpdatedAt = now
	if params.CallerID != "" {
		item.CallerID = strings.TrimSpace(params.CallerID)
	}
	if params.AccountID != "" {
		item.AccountID = strings.TrimSpace(params.AccountID)
	}
	if params.Status != "" {
		item.Status = params.Status
	}
	if params.HistoryText != "" {
		item.HistoryText = params.HistoryText
	}
	if params.ReasoningContent != "" || item.ReasoningContent == "" {
		item.ReasoningContent = params.ReasoningContent
	}
	if params.Content != "" || item.Content == "" {
		item.Content = params.Content
	}
	item.Error = strings.TrimSpace(params.Error)
	item.StatusCode = params.StatusCode
	item.ElapsedMs = params.ElapsedMs
	item.FinishReason = strings.TrimSpace(params.FinishReason)
	if params.Usage != nil {
		item.Usage = cloneMap(params.Usage)
	}
	if params.Completed {
		item.CompletedAt = now
	}
	s.details[target] = item
	s.markDetailDirtyLocked(target)
	s.upsertSummaryLocked(item)
	if err := s.saveLocked(); err != nil {
		return Entry{}, err
	}
	return cloneEntry(item), nil
}

func (s *Store) Delete(id string) error {
	if s == nil {
		return errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Delete(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	target := strings.TrimSpace(id)
	if target == "" {
		return errors.New("history id is required")
	}
	if _, _, ok := s.findSummaryLocked(target); !ok {
		return errors.New("chat history entry not found")
	}
	if !isSafeDetailID(target) {
		return errors.New("chat history entry id is invalid")
	}
	s.markDetailDeletedLocked(target)
	delete(s.details, target)
	s.removeSummaryLocked(target)
	s.nextRevisionLocked()
	if err := s.saveLocked(); err != nil {
		return err
	}
	return nil
}

func (s *Store) Clear() error {
	if s == nil {
		return errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.Clear()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	for _, item := range s.state.Items {
		s.markDetailDeletedLocked(item.ID)
	}
	for id := range s.details {
		s.markDetailDeletedLocked(id)
	}
	s.details = map[string]Entry{}
	s.state.Items = []SummaryEntry{}
	s.nextRevisionLocked()
	if err := s.saveLocked(); err != nil {
		return err
	}
	return nil
}

func (s *Store) SetLimit(limit int) (File, error) {
	if s == nil {
		return File{}, errors.New("chat history store is nil")
	}
	if s.sqlite != nil {
		return s.sqlite.SetLimit(limit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return File{}, s.err
	}
	if !isAllowedLimit(limit) {
		return File{}, fmt.Errorf("unsupported chat history limit: %d", limit)
	}
	s.state.Limit = limit
	s.nextRevisionLocked()
	s.normalizeIndexLocked()
	if err := s.saveLocked(); err != nil {
		return File{}, err
	}
	return cloneFile(s.state), nil
}

func (s *Store) loadLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return errors.New("chat history path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil && filepath.Dir(s.path) != "." {
		return fmt.Errorf("create chat history dir: %w", err)
	}
	if err := os.MkdirAll(s.detailDir, 0o700); err != nil {
		return fmt.Errorf("create chat history detail dir: %w", err)
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if saveErr := s.saveLocked(); saveErr != nil {
				config.Logger.Warn("[chat_history] bootstrap write failed", "path", s.path, "error", saveErr)
			}
			return nil
		}
		return fmt.Errorf("read chat history index: %w", err)
	}

	legacy, legacyOK, legacyErr := parseLegacy(raw)
	if legacyErr != nil {
		return legacyErr
	}
	if legacyOK {
		s.loadLegacyLocked(legacy)
		s.stopUnfinishedLocked("server restarted before request completed")
		if err := s.saveLocked(); err != nil {
			config.Logger.Warn("[chat_history] legacy migration writeback failed", "path", s.path, "error", err)
		}
		return nil
	}

	var state File
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("decode chat history index: %w", err)
	}
	if state.Version == 0 {
		state.Version = FileVersion
	}
	if !isAllowedLimit(state.Limit) {
		state.Limit = DefaultLimit
	}
	s.state = cloneFile(state)
	s.details = map[string]Entry{}
	s.normalizeIndexLocked()
	s.stopUnfinishedLocked("server restarted before request completed")
	if saveErr := s.saveLocked(); saveErr != nil {
		config.Logger.Warn("[chat_history] index rewrite failed", "path", s.path, "error", saveErr)
	}
	return nil
}

func (s *Store) loadLegacyLocked(legacy legacyFile) {
	s.state.Version = FileVersion
	s.state.Limit = legacy.Limit
	if !isAllowedLimit(s.state.Limit) {
		s.state.Limit = DefaultLimit
	}
	s.details = map[string]Entry{}
	s.dirty = map[string]struct{}{}
	s.deleted = map[string]struct{}{}
	maxRevision := int64(0)
	for _, item := range legacy.Items {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		item.Messages = cloneMessages(item.Messages)
		if item.Revision == 0 {
			if item.UpdatedAt > 0 {
				item.Revision = item.UpdatedAt
			} else {
				item.Revision = time.Now().UnixNano()
			}
		}
		if item.Revision > maxRevision {
			maxRevision = item.Revision
		}
		s.details[item.ID] = item
		s.markDetailDirtyLocked(item.ID)
	}
	s.state.Revision = maxRevision
	s.rebuildIndexLocked()
}

func (s *Store) stopUnfinishedLocked(reason string) {
	now := time.Now().UnixMilli()
	for _, summary := range append([]SummaryEntry(nil), s.state.Items...) {
		if summary.Status != "streaming" && summary.Status != "queued" {
			continue
		}
		item, err := s.loadDetailForUpdateLocked(summary.ID)
		if err != nil {
			item = entryFromSummary(summary)
		}
		item.Revision = s.nextRevisionLocked()
		item.UpdatedAt = now
		if item.CompletedAt == 0 {
			item.CompletedAt = now
		}
		if item.ElapsedMs <= 0 && item.CreatedAt > 0 && now >= item.CreatedAt {
			item.ElapsedMs = now - item.CreatedAt
		}
		item.Status = "stopped"
		item.FinishReason = "server_restart"
		if strings.TrimSpace(item.Error) == "" {
			item.Error = reason
		}
		s.details[summary.ID] = item
		s.markDetailDirtyLocked(summary.ID)
		s.upsertSummaryLocked(item)
	}
}

func (s *Store) saveLocked() error {
	s.state.Version = FileVersion
	if !isAllowedLimit(s.state.Limit) {
		s.state.Limit = DefaultLimit
	}
	for _, id := range sortedDetailIDs(s.dirty) {
		if item, ok := s.details[id]; ok {
			s.upsertSummaryLocked(item)
		}
	}
	for _, id := range sortedDetailIDs(s.deleted) {
		s.removeSummaryLocked(id)
		delete(s.details, id)
	}
	s.normalizeIndexLocked()

	if err := os.MkdirAll(s.detailDir, 0o700); err != nil {
		return fmt.Errorf("create chat history detail dir: %w", err)
	}
	for _, id := range sortedDetailIDs(s.deleted) {
		if !isSafeDetailID(id) {
			return fmt.Errorf("invalid chat history detail id: %s", id)
		}
		path, ok := joinSafeDetailPath(s.detailDir, id)
		if !ok {
			return fmt.Errorf("invalid chat history detail path for id: %s", id)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale chat history detail: %w", err)
		}
	}
	for _, id := range sortedDetailIDs(s.dirty) {
		if _, deleting := s.deleted[id]; deleting {
			continue
		}
		item, ok := s.details[id]
		if !ok {
			continue
		}
		if !isSafeDetailID(id) {
			return fmt.Errorf("invalid chat history detail id: %s", id)
		}
		path, ok := joinSafeDetailPath(s.detailDir, id)
		if !ok {
			return fmt.Errorf("invalid chat history detail path for id: %s", id)
		}
		payload, err := json.MarshalIndent(detailEnvelope{
			Version: FileVersion,
			Item:    item,
		}, "", "  ")
		if err != nil {
			return fmt.Errorf("encode chat history detail: %w", err)
		}
		if err := writeFileAtomic(path, append(payload, '\n')); err != nil {
			return err
		}
		if !shouldKeepDetailCached(item) {
			delete(s.details, id)
		}
	}

	payload, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode chat history index: %w", err)
	}
	if err := writeFileAtomic(s.path, append(payload, '\n')); err != nil {
		return err
	}
	s.clearPendingDetailChangesLocked()
	return nil
}

func (s *Store) rebuildIndexLocked() {
	for _, item := range s.details {
		s.upsertSummaryLocked(item)
	}
	s.normalizeIndexLocked()
}

func (s *Store) nextRevisionLocked() int64 {
	next := time.Now().UnixNano()
	if next <= s.state.Revision {
		next = s.state.Revision + 1
	}
	s.state.Revision = next
	return next
}

func summaryFromEntry(item Entry) SummaryEntry {
	return SummaryEntry{
		ID:             item.ID,
		Revision:       item.Revision,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		CompletedAt:    item.CompletedAt,
		Status:         item.Status,
		CallerID:       item.CallerID,
		AccountID:      item.AccountID,
		RequestIP:      item.RequestIP,
		ConversationID: item.ConversationID,
		Model:          item.Model,
		Stream:         item.Stream,
		UserInput:      item.UserInput,
		Preview:        buildPreview(item),
		StatusCode:     item.StatusCode,
		ElapsedMs:      item.ElapsedMs,
		FinishReason:   item.FinishReason,
		DetailRevision: item.Revision,
		Usage:          cloneMap(item.Usage),
	}
}

func buildPreview(item Entry) string {
	candidate := strings.TrimSpace(item.Content)
	if candidate == "" {
		candidate = strings.TrimSpace(item.ReasoningContent)
	}
	if candidate == "" {
		candidate = strings.TrimSpace(item.Error)
	}
	if candidate == "" {
		candidate = strings.TrimSpace(item.UserInput)
	}
	if truncated, ok := util.TruncateRunes(candidate, defaultPreviewAt); ok {
		return truncated + "..."
	}
	return candidate
}

// readDetailFile reads + decodes a single detail file. The path argument
// MUST be the result of joinSafeDetailPath (or equivalent containment-
// validated join) — readDetailFile re-asserts containment via the
// detailDir parameter so CodeQL's go/path-injection flow analysis can
// trace the safety of the os.ReadFile call from this site.
func readDetailFile(detailDir, id string) (Entry, error) {
	path, ok := joinSafeDetailPath(detailDir, id)
	if !ok {
		return Entry{}, fmt.Errorf("invalid chat history detail path for id: %s", id)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, fmt.Errorf("read chat history detail: %w", err)
	}
	var env detailEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Entry{}, fmt.Errorf("decode chat history detail: %w", err)
	}
	return cloneEntry(env.Item), nil
}

func (s *Store) readDetailLocked(id string) (Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Entry{}, errors.New("history id is required")
	}
	if item, ok := s.details[id]; ok {
		return cloneEntry(item), nil
	}
	if _, _, ok := s.findSummaryLocked(id); !ok {
		return Entry{}, errors.New("chat history entry not found")
	}
	return readDetailFile(s.detailDir, id)
}

func (s *Store) loadDetailForUpdateLocked(id string) (Entry, error) {
	item, err := s.readDetailLocked(id)
	if err != nil {
		return Entry{}, err
	}
	s.details[item.ID] = item
	return item, nil
}

func (s *Store) findSummaryLocked(id string) (SummaryEntry, int, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return SummaryEntry{}, -1, false
	}
	for i, item := range s.state.Items {
		if item.ID == id {
			return cloneSummary(item), i, true
		}
	}
	return SummaryEntry{}, -1, false
}

func (s *Store) upsertSummaryLocked(item Entry) {
	summary := summaryFromEntry(item)
	if strings.TrimSpace(summary.ID) == "" {
		return
	}
	for i := range s.state.Items {
		if s.state.Items[i].ID == summary.ID {
			s.state.Items[i] = summary
			return
		}
	}
	s.state.Items = append(s.state.Items, summary)
}

func (s *Store) removeSummaryLocked(id string) {
	id = strings.TrimSpace(id)
	if id == "" || len(s.state.Items) == 0 {
		return
	}
	out := s.state.Items[:0]
	for _, item := range s.state.Items {
		if item.ID != id {
			out = append(out, item)
		}
	}
	s.state.Items = out
}

func (s *Store) normalizeIndexLocked() {
	s.state.Version = FileVersion
	if !isAllowedLimit(s.state.Limit) {
		s.state.Limit = DefaultLimit
	}
	seen := map[string]struct{}{}
	summaries := make([]SummaryEntry, 0, len(s.state.Items))
	for _, item := range s.state.Items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		summaries = append(summaries, cloneSummary(item))
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].UpdatedAt == summaries[j].UpdatedAt {
			return summaries[i].CreatedAt > summaries[j].CreatedAt
		}
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	retain := retainedHistoryCountAfterPrune(len(summaries), s.state.Limit)
	if retain >= 0 && len(summaries) > retain {
		for _, item := range summaries[retain:] {
			s.markDetailDeletedLocked(item.ID)
			delete(s.details, item.ID)
		}
		summaries = summaries[:retain]
	}
	s.state.Items = summaries
}

func entryFromSummary(item SummaryEntry) Entry {
	return Entry{
		ID:           item.ID,
		Revision:     item.DetailRevision,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
		CompletedAt:  item.CompletedAt,
		Status:       item.Status,
		CallerID:     item.CallerID,
		AccountID:    item.AccountID,
		Model:        item.Model,
		Stream:       item.Stream,
		UserInput:    item.UserInput,
		Error:        item.Preview,
		StatusCode:   item.StatusCode,
		ElapsedMs:    item.ElapsedMs,
		FinishReason: item.FinishReason,
		Usage:        cloneMap(item.Usage),
	}
}

func shouldKeepDetailCached(item Entry) bool {
	switch strings.TrimSpace(item.Status) {
	case "queued", "streaming":
		return true
	default:
		return false
	}
}

func parseLegacy(raw []byte) (legacyFile, bool, error) {
	var legacy legacyFile
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return legacyFile{}, false, nil
	}
	if len(legacy.Items) == 0 {
		return legacy, false, nil
	}
	var probe legacyProbe
	if err := json.Unmarshal(raw, &probe); err == nil {
		for _, item := range probe.Items {
			if _, ok := item["detail_revision"]; ok {
				return legacy, false, nil
			}
		}
	}
	return legacy, true, nil
}

func writeFileAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create chat history dir: %w", err)
		}
	}
	tmpFile, err := os.CreateTemp(dir, ".chat-history-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp chat history: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() error {
		if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove temp chat history: %w", err)
		}
		return nil
	}
	withCleanup := func(primary error, closeErr error) error {
		errs := []error{primary}
		if closeErr != nil {
			errs = append(errs, fmt.Errorf("close temp chat history: %w", closeErr))
		}
		if cleanupErr := cleanup(); cleanupErr != nil {
			errs = append(errs, cleanupErr)
		}
		return errors.Join(errs...)
	}
	if _, err := tmpFile.Write(body); err != nil {
		return withCleanup(fmt.Errorf("write temp chat history: %w", err), tmpFile.Close())
	}
	if err := tmpFile.Sync(); err != nil {
		return withCleanup(fmt.Errorf("sync temp chat history: %w", err), tmpFile.Close())
	}
	if err := tmpFile.Close(); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return errors.Join(fmt.Errorf("close temp chat history: %w", err), cleanupErr)
		}
		return fmt.Errorf("close temp chat history: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return errors.Join(fmt.Errorf("promote temp chat history: %w", err), cleanupErr)
		}
		return fmt.Errorf("promote temp chat history: %w", err)
	}
	return nil
}

func ListETag(revision int64, offset, limit int) string {
	return fmt.Sprintf(`W/"chat-history-list-%d-%d-%d"`, revision, offset, limit)
}

func DetailETag(id string, revision int64) string {
	return fmt.Sprintf(`W/"chat-history-detail-%s-%d"`, strings.TrimSpace(id), revision)
}

// joinSafeDetailPath builds the on-disk path for a detail file ID and
// verifies the result lives strictly under detailDir. The id MUST already
// have passed isSafeDetailID; this function adds a second-line defence so
// CodeQL's go/path-injection flow analysis sees an explicit containment
// check at the os.Remove / os.ReadFile call site even though the id is
// already constrained to [A-Za-z0-9_-]{1,128}.
func joinSafeDetailPath(detailDir, id string) (string, bool) {
	if !isSafeDetailID(id) {
		return "", false
	}
	cleanDir := filepath.Clean(detailDir)
	candidate := filepath.Clean(filepath.Join(cleanDir, id+".json"))
	parent := filepath.Dir(candidate)
	if parent != cleanDir {
		return "", false
	}
	return candidate, true
}

func isSafeDetailID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 || strings.Contains(id, "..") || filepath.IsAbs(id) {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func isAllowedLimit(limit int) bool {
	_, ok := allowedLimits[limit]
	return ok
}

func retainedHistoryCountAfterPrune(total, limit int) int {
	if limit == DisabledLimit {
		return -1
	}
	if !isAllowedLimit(limit) {
		limit = DefaultLimit
	}
	if limit == MaxLimit {
		if total >= MaxLimit {
			return BurstPruneRetainedLimit
		}
		return MaxLimit
	}
	return limit
}

func (s *Store) markDetailDirtyLocked(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if s.dirty == nil {
		s.dirty = map[string]struct{}{}
	}
	if s.deleted == nil {
		s.deleted = map[string]struct{}{}
	}
	s.dirty[id] = struct{}{}
	delete(s.deleted, id)
}

func (s *Store) markDetailDeletedLocked(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	if s.dirty == nil {
		s.dirty = map[string]struct{}{}
	}
	if s.deleted == nil {
		s.deleted = map[string]struct{}{}
	}
	s.deleted[id] = struct{}{}
	delete(s.dirty, id)
}

func (s *Store) clearPendingDetailChangesLocked() {
	s.dirty = map[string]struct{}{}
	s.deleted = map[string]struct{}{}
}

func sortedDetailIDs(ids map[string]struct{}) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func cloneFile(in File) File {
	out := File{
		Version:  in.Version,
		Limit:    in.Limit,
		Revision: in.Revision,
		Items:    make([]SummaryEntry, len(in.Items)),
	}
	for i, item := range in.Items {
		out.Items[i] = cloneSummary(item)
	}
	return out
}

func cloneSummary(item SummaryEntry) SummaryEntry {
	item.Usage = cloneMap(item.Usage)
	return item
}

func cloneEntry(item Entry) Entry {
	item.Usage = cloneMap(item.Usage)
	item.Messages = cloneMessages(item.Messages)
	return item
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return []Message{}
	}
	out := make([]Message, len(messages))
	copy(out, messages)
	return out
}
