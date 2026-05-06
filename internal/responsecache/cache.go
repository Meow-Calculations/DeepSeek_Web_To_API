package responsecache

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

const (
	defaultMemoryTTL      = 5 * time.Minute
	defaultDiskTTL        = 4 * time.Hour
	defaultMaxBody        = 64 << 20
	defaultMemoryMaxBytes = int64(3800 * 1000 * 1000)
	defaultDiskMaxBytes   = int64(16 * 1000 * 1000 * 1000)
	recordVersion         = 1

	// inflightWaitTimeout bounds how long a deduplicated waiter blocks on
	// the owner's upstream call before falling through to its own upstream
	// dispatch. Aligned with the global HTTP total timeout (7200s default
	// since v1.0.6) so long-reasoning Pro-model requests do NOT trigger a
	// thundering herd of waiters dispatching duplicate upstream calls
	// when the owner is still legitimately thinking. The owner closes the
	// inflight slot via defer in Wrap, so panics / early returns wake
	// every waiter immediately regardless of this ceiling.
	inflightWaitTimeout = 2 * time.Hour
)

type CallerResolver interface {
	DetermineCaller(req *http.Request) (*auth.RequestAuth, error)
}

type HitFunc func(req *http.Request, entry Entry, source string)

type Options struct {
	Dir            string
	MemoryTTL      time.Duration
	DiskTTL        time.Duration
	MaxBody        int64
	MemoryMaxBytes int64
	DiskMaxBytes   int64
	SemanticKey    bool
	OnHit          HitFunc
}

type Cache struct {
	mu             sync.Mutex
	dir            string
	memoryTTL      time.Duration
	diskTTL        time.Duration
	maxBody        int64
	memoryMaxBytes int64
	diskMaxBytes   int64
	memoryBytes    int64
	hits           int64
	misses         int64
	stores         int64
	memoryHits     int64
	diskHits       int64
	inflightHits   int64
	uncacheable    map[string]int64
	pathStats      map[string]*pathStat
	items          map[string]memoryEntry
	inflight       map[string]*inflightSlot
	sessionEntries map[string]map[string]struct{}
	sessionHits    int64
	lastDiskSweep  time.Time
	onHit          HitFunc
	semanticKey    bool
}

// inflightSlot collapses concurrent identical requests onto a single
// upstream call ("single-flight"). The owner runs the upstream handler;
// waiters block on `done` until the owner releases. After release, waiters
// re-check the cache and serve the freshly-stored entry. This is a major
// component of session stickiness: when a flaky client retries an in-flight
// request, the second arrival waits for the first to complete instead of
// dispatching its own upstream call.
type inflightSlot struct {
	done chan struct{}
}

// pathStat aggregates lifecycle counters for a single canonical request
// path. Per-path breakdown is what makes "why did the hit rate drop"
// answerable: embeddings vs. chat completions are different workloads with
// different ceilings, and aggregate stats hide which path is the bottleneck.
type pathStat struct {
	Hits         int64
	Misses       int64
	Stores       int64
	MemoryHits   int64
	DiskHits     int64
	InflightHits int64
	Uncacheable  map[string]int64
}

type Entry struct {
	Status        int
	Header        http.Header
	Body          []byte
	DiskExpiresAt time.Time
}

type memoryEntry struct {
	entry           Entry
	memoryExpiresAt time.Time
	size            int64
	// sessionKey ties this entry to a logical conversation as derived from
	// (owner, body fingerprint). When a session is alive, every memory hit
	// on ANY of its entries refreshes the lease on ALL of its entries —
	// session-wide sliding window. Empty for shared-across-callers paths
	// (embeddings, count_tokens) where session affinity does not apply.
	sessionKey string
}

type diskRecord struct {
	Version       int         `json:"version"`
	Key           string      `json:"key"`
	CreatedAtUnix int64       `json:"created_at_unix"`
	ExpiresAtUnix int64       `json:"expires_at_unix"`
	Status        int         `json:"status"`
	Header        http.Header `json:"header"`
	Body          []byte      `json:"body"`
}

func New(opts Options) *Cache {
	opts = normalizeOptions(opts)
	return &Cache{
		dir:            opts.Dir,
		memoryTTL:      opts.MemoryTTL,
		diskTTL:        opts.DiskTTL,
		maxBody:        opts.MaxBody,
		memoryMaxBytes: opts.MemoryMaxBytes,
		diskMaxBytes:   opts.DiskMaxBytes,
		uncacheable:    map[string]int64{},
		pathStats:      map[string]*pathStat{},
		items:          map[string]memoryEntry{},
		inflight:       map[string]*inflightSlot{},
		sessionEntries: map[string]map[string]struct{}{},
		onHit:          opts.OnHit,
		semanticKey:    opts.SemanticKey,
	}
}

func normalizeOptions(opts Options) Options {
	memoryTTL := opts.MemoryTTL
	if memoryTTL <= 0 {
		memoryTTL = defaultMemoryTTL
	}
	diskTTL := opts.DiskTTL
	if diskTTL <= 0 {
		diskTTL = defaultDiskTTL
	}
	maxBody := opts.MaxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}
	memoryMaxBytes := opts.MemoryMaxBytes
	if memoryMaxBytes <= 0 {
		memoryMaxBytes = defaultMemoryMaxBytes
	}
	diskMaxBytes := opts.DiskMaxBytes
	if diskMaxBytes <= 0 {
		diskMaxBytes = defaultDiskMaxBytes
	}
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		dir = config.ResponseCacheDir()
	}
	opts.Dir = filepath.Clean(dir)
	opts.MemoryTTL = memoryTTL
	opts.DiskTTL = diskTTL
	opts.MaxBody = maxBody
	opts.MemoryMaxBytes = memoryMaxBytes
	opts.DiskMaxBytes = diskMaxBytes
	return opts
}

func (c *Cache) ApplyOptions(opts Options) {
	if c == nil {
		return
	}
	opts = normalizeOptions(opts)
	c.mu.Lock()
	defer c.mu.Unlock()

	c.dir = opts.Dir
	c.memoryTTL = opts.MemoryTTL
	c.diskTTL = opts.DiskTTL
	c.maxBody = opts.MaxBody
	c.memoryMaxBytes = opts.MemoryMaxBytes
	c.diskMaxBytes = opts.DiskMaxBytes
	c.semanticKey = opts.SemanticKey
	if opts.OnHit != nil {
		c.onHit = opts.OnHit
	}

	c.items = map[string]memoryEntry{}
	c.memoryBytes = 0
	c.lastDiskSweep = time.Time{}
	if c.pathStats == nil {
		c.pathStats = map[string]*pathStat{}
	}
	if c.inflight == nil {
		c.inflight = map[string]*inflightSlot{}
	}
	c.sessionEntries = map[string]map[string]struct{}{}
}

func (c *Cache) Middleware(resolver CallerResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return c.Wrap(resolver, next)
	}
}

func (c *Cache) Wrap(resolver CallerResolver, next http.Handler) http.Handler {
	if c == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CacheableRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if c.requestBodyTooLarge(r) {
			next.ServeHTTP(w, r)
			return
		}
		rawBody, tooLarge, err := c.readRequestBody(r)
		if err != nil {
			r.Body = replayBody(rawBody, r.Body)
			next.ServeHTTP(w, r)
			return
		}
		if tooLarge {
			r.Body = replayBody(rawBody, r.Body)
			next.ServeHTTP(w, r)
			return
		}
		if closeErr := r.Body.Close(); closeErr != nil {
			config.Logger.Warn("[response_cache] close request body failed", "error", closeErr)
		}
		r.Body = io.NopCloser(bytes.NewReader(rawBody))

		policy := pathPolicyFor(canonicalRequestPath(r.URL.Path))

		owner := ""
		if resolver != nil {
			if a, authErr := resolver.DetermineCaller(r); authErr == nil && a != nil {
				owner = strings.TrimSpace(a.CallerID)
			}
		}
		// For per-caller paths (LLM completions) we still require a resolved
		// caller — leaking a sampled response across tenants is a privacy
		// boundary. For shared paths (embeddings, count_tokens) the entry is
		// a deterministic function of the body alone, so an unresolved
		// caller is fine.
		if !policy.SharedAcrossCallers && owner == "" {
			next.ServeHTTP(w, r)
			return
		}

		key := c.requestKeyWithPolicy(r, owner, rawBody, policy)
		sessionKey := ""
		if !policy.SharedAcrossCallers && owner != "" {
			sessionKey = account.SessionKey(sha256.Sum256([]byte(owner)), rawBody)
		}
		if entry, source, ok := c.getWithPolicy(key, policy); ok {
			if c.onHit != nil {
				c.onHit(r, cloneEntry(entry), source)
			}
			writeCachedResponse(w, entry, source)
			return
		}

		// Single-flight: if another request with this exact cache key is
		// already running upstream, wait for its result instead of
		// dispatching a duplicate. This is the second arm of the session
		// stickiness contract — flaky-client retries during streaming
		// collapse onto the in-flight call.
		slot, isOwner := c.acquireInflight(key)
		if !isOwner {
			waited, served := c.waitForInflightOwner(w, r, slot, key, policy)
			if served {
				return
			}
			if !waited {
				return
			}
			// Owner finished but did not produce a cacheable entry, OR we
			// timed out. Try to claim ownership ourselves before falling
			// through to upstream — if we succeed, subsequent waiters
			// (including any other timed-out ones) collapse onto us
			// instead of all stampeding the LLM backend simultaneously.
			slot, isOwner = c.acquireInflight(key)
			if !isOwner {
				// Another goroutine became the owner while we were
				// waking up. Wait on it once more with a fresh budget.
				waited, served = c.waitForInflightOwner(w, r, slot, key, policy)
				if served {
					return
				}
				if !waited {
					return
				}
				// Two sequential owners both produced nothing cacheable
				// — at this point we accept the fall-through and run
				// upstream ourselves without holding the slot.
			}
		}

		r.Body = io.NopCloser(bytes.NewReader(rawBody))
		cw := newCaptureResponseWriter(w, c.maxBody)
		if isOwner {
			defer c.releaseInflight(key)
		}
		next.ServeHTTP(cw, r)
		if entry, ok, reason := cw.cacheEntry(); ok {
			c.setWithPolicySession(key, sessionKey, entry, policy)
		} else {
			c.recordUncacheable(policy.Path, reason)
		}
	})
}

// waitForInflightOwner blocks the caller until the in-flight owner closes
// the slot's done channel, the wait times out, or the client cancels.
// Returns (continueToUpstream, alreadyServed). When the owner produced a
// cacheable response, the function serves it from cache and returns
// (false, true). When the wait times out or the owner produced nothing
// cacheable, returns (true, false) so Wrap falls through to upstream. When
// the client cancelled, returns (false, false) so Wrap aborts cleanly.
func (c *Cache) waitForInflightOwner(w http.ResponseWriter, r *http.Request, slot *inflightSlot, key string, policy pathPolicy) (continueUpstream, alreadyServed bool) {
	timer := time.NewTimer(inflightWaitTimeout)
	defer timer.Stop()
	select {
	case <-slot.done:
		if entry, source, ok := c.getWithPolicy(key, policy); ok {
			c.recordInflightHit(policy.Path)
			if c.onHit != nil {
				c.onHit(r, cloneEntry(entry), source)
			}
			writeCachedResponse(w, entry, source)
			return false, true
		}
		return true, false
	case <-timer.C:
		return true, false
	case <-r.Context().Done():
		return false, false
	}
}

func CacheableRequest(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Method != http.MethodPost {
		return false
	}
	if requestForcesBypass(r) {
		return false
	}
	path := canonicalRequestPath(r.URL.Path)
	switch path {
	case "/v1/chat/completions", "/v1/responses", "/v1/embeddings":
		return true
	case "/anthropic/v1/messages", "/v1/messages", "/messages",
		"/anthropic/v1/messages/count_tokens", "/v1/messages/count_tokens", "/messages/count_tokens":
		return true
	}
	if strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/") {
		return strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent")
	}
	return false
}

func RequestKey(r *http.Request, owner string, body []byte) string {
	policy := pathPolicy{}
	if r != nil && r.URL != nil {
		policy = pathPolicyFor(canonicalRequestPath(r.URL.Path))
	}
	return requestKey(r, owner, body, true, policy)
}

func (c *Cache) requestKeyWithPolicy(r *http.Request, owner string, body []byte, policy pathPolicy) string {
	if c == nil {
		return requestKey(r, owner, body, true, policy)
	}
	return requestKey(r, owner, body, c.semanticKey, policy)
}

func requestKey(r *http.Request, owner string, body []byte, semanticKey bool, policy pathPolicy) string {
	h := sha256.New()
	body = canonicalRequestBodyForKey(r, body, semanticKey)
	version := "v1"
	if semanticKey {
		version = "v2-semantic"
	}
	writeKeyPart(h, version)
	// Shared paths intentionally omit the owner from the key so the same
	// deterministic body produces the same key across API keys; per-caller
	// paths keep the owner as a hard tenant boundary.
	if policy.SharedAcrossCallers {
		writeKeyPart(h, "shared")
	} else {
		writeKeyPart(h, strings.TrimSpace(owner))
	}
	if r != nil {
		writeKeyPart(h, strings.ToUpper(strings.TrimSpace(r.Method)))
		if r.URL != nil {
			writeKeyPart(h, canonicalRequestPath(r.URL.Path))
			writeKeyPart(h, canonicalQuery(r.URL.Query()))
		}
		for _, header := range varyRequestHeaders() {
			writeKeyPart(h, strings.ToLower(header.Name)+":"+strings.Join(requestHeaderValues(r.Header, header.Name, header.Legacy...), "\x1f"))
		}
	}
	bodySum := sha256.Sum256(body)
	writeKeyPart(h, hex.EncodeToString(bodySum[:]))
	return hex.EncodeToString(h.Sum(nil))
}

func canonicalRequestBodyForKey(r *http.Request, body []byte, semanticKey bool) []byte {
	if len(body) == 0 || !cacheKeyShouldNormalizeJSON(r) {
		return body
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return body
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return body
	}
	value, keep := normalizeCacheKeyJSONValue(value, true, "", semanticKey)
	if !keep {
		return body
	}
	normalized, err := json.Marshal(value)
	if err != nil || len(normalized) == 0 {
		return body
	}
	return normalized
}

func cacheKeyShouldNormalizeJSON(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if isJSONContentType(r.Header.Get("Content-Type")) {
		return true
	}
	return CacheableRequest(r)
}

func isJSONContentType(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return raw == "application/json" || strings.Contains(raw, "+json") || strings.Contains(raw, "/json")
}

func normalizeCacheKeyJSONValue(value any, topLevel bool, key string, semanticKey bool) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		if cacheKeyMapShouldDrop(v) {
			return nil, false
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			if ignoredCacheKeyField(key, topLevel, semanticKey) {
				continue
			}
			normalized, keep := normalizeCacheKeyJSONValue(item, false, key, semanticKey)
			if !keep {
				continue
			}
			out[key] = normalized
		}
		return out, true
	case []any:
		out := make([]any, 0, len(v))
		for i := range v {
			normalized, keep := normalizeCacheKeyJSONValue(v[i], false, key, semanticKey)
			if !keep {
				continue
			}
			out = append(out, normalized)
		}
		return out, true
	case string:
		if semanticKey && semanticStringKey(key) {
			return normalizeSemanticString(v), true
		}
		if semanticKey && semanticLowerStringKey(key) {
			return strings.ToLower(strings.TrimSpace(v)), true
		}
		return value, true
	default:
		return value, true
	}
}

func cacheKeyMapShouldDrop(value map[string]any) bool {
	typ, _ := value["type"].(string)
	return strings.EqualFold(strings.TrimSpace(typ), "cache_edits")
}

func ignoredCacheKeyField(key string, topLevel bool, semanticKey bool) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "cache_control", "cache_reference", "context_management":
		return true
	}
	if semanticKey {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "id", "call_id", "tool_call_id", "tool_use_id", "message_id", "request_id", "trace_id", "event_id",
			"conversation_id", "conversationid", "chat_id", "chatid", "thread_id", "threadid", "session_id", "sessionid":
			return true
		}
	}
	if !topLevel {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "metadata", "user", "service_tier", "parallel_tool_calls", "seed", "store", "betas":
		return true
	default:
		return false
	}
}

func semanticStringKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "content", "text", "input", "prompt", "query", "instructions", "system":
		return true
	default:
		return false
	}
}

func semanticLowerStringKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "role", "type":
		return true
	default:
		return false
	}
}

func normalizeSemanticString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	fields := strings.Fields(raw)
	if len(fields) <= 1 {
		return raw
	}
	return strings.Join(fields, " ")
}

// Get is a backward-compatible wrapper that uses no path policy. It is kept
// for external callers that look up entries without protocol-path context;
// the in-package middleware always goes through getWithPolicy.
func (c *Cache) Get(key string) (Entry, string, bool) {
	return c.getWithPolicy(key, pathPolicy{})
}

func (c *Cache) getWithPolicy(key string, policy pathPolicy) (Entry, string, bool) {
	key = normalizeKey(key)
	if key == "" {
		return Entry{}, "", false
	}
	now := time.Now()
	c.mu.Lock()
	c.sweepMemoryLocked(now)
	if item, ok := c.items[key]; ok {
		if now.Before(item.memoryExpiresAt) && now.Before(item.entry.DiskExpiresAt) {
			entry := cloneEntry(item.entry)
			c.bumpMemoryExpiryLocked(key, now, policy)
			c.recordHitLocked(policy.Path, "memory")
			c.mu.Unlock()
			return entry, "memory", true
		}
		c.memoryBytes -= item.size
		c.removeFromSessionLocked(item.sessionKey, key)
		delete(c.items, key)
	}
	c.mu.Unlock()

	entry, ok := c.getDisk(key, now)
	if !ok {
		c.recordMiss(policy.Path)
		return Entry{}, "", false
	}
	c.putMemoryWithPolicySession(key, "", entry, now, policy)
	c.recordHit(policy.Path, "disk")
	return cloneEntry(entry), "disk", true
}

// bumpMemoryExpiryLocked extends a memory entry's lease on access ("sliding
// window TTL"). Active conversations that keep hitting the same prefix —
// e.g., long-context coding agents that retry within a turn, or clients
// that re-issue identical tool calls — keep their entry hot beyond the
// initial memoryTTL. The extension is capped by the entry's disk
// expiration: once disk has expired, the cache row is logically dead even
// if memory keeps requesting it. When the entry belongs to a logical
// session (sessionKey is set), every sibling entry of that session ALSO
// gets its lease refreshed — that is the session-wide stickiness contract:
// activity on any turn keeps every cached turn of the same conversation
// hot together. Callers must hold c.mu.
func (c *Cache) bumpMemoryExpiryLocked(key string, now time.Time, policy pathPolicy) {
	item, ok := c.items[key]
	if !ok {
		return
	}
	memoryTTL := c.memoryTTL
	if policy.MemoryTTL > 0 {
		memoryTTL = policy.MemoryTTL
	}
	if memoryTTL <= 0 {
		return
	}
	c.bumpSingleEntryExpiryLocked(key, item, now, memoryTTL)
	if item.sessionKey == "" {
		return
	}
	siblings, ok := c.sessionEntries[item.sessionKey]
	if !ok || len(siblings) <= 1 {
		return
	}
	for siblingKey := range siblings {
		if siblingKey == key {
			continue
		}
		sibling, ok := c.items[siblingKey]
		if !ok {
			continue
		}
		c.bumpSingleEntryExpiryLocked(siblingKey, sibling, now, memoryTTL)
	}
	c.sessionHits++
}

func (c *Cache) bumpSingleEntryExpiryLocked(key string, item memoryEntry, now time.Time, memoryTTL time.Duration) {
	extended := now.Add(memoryTTL)
	if item.entry.DiskExpiresAt.Before(extended) {
		extended = item.entry.DiskExpiresAt
	}
	if extended.After(item.memoryExpiresAt) {
		item.memoryExpiresAt = extended
		c.items[key] = item
	}
}

// addToSessionLocked links a cache key to a logical session bucket. Empty
// sessionKey is a no-op (the entry is unsessioned). Callers must hold c.mu.
func (c *Cache) addToSessionLocked(sessionKey, cacheKey string) {
	if sessionKey == "" || cacheKey == "" {
		return
	}
	if c.sessionEntries == nil {
		c.sessionEntries = map[string]map[string]struct{}{}
	}
	bucket, ok := c.sessionEntries[sessionKey]
	if !ok {
		bucket = map[string]struct{}{}
		c.sessionEntries[sessionKey] = bucket
	}
	bucket[cacheKey] = struct{}{}
}

// removeFromSessionLocked unlinks a cache key from its session bucket and
// drops the bucket if empty. Empty sessionKey is a no-op. Callers must hold
// c.mu.
func (c *Cache) removeFromSessionLocked(sessionKey, cacheKey string) {
	if sessionKey == "" || cacheKey == "" {
		return
	}
	bucket, ok := c.sessionEntries[sessionKey]
	if !ok {
		return
	}
	delete(bucket, cacheKey)
	if len(bucket) == 0 {
		delete(c.sessionEntries, sessionKey)
	}
}

// Set is a backward-compatible wrapper that uses no path policy (the global
// memory/disk TTLs apply). External callers that don't carry path context
// can keep using this; the in-package middleware always routes through
// setWithPolicySession so per-path TTL overrides and session linkage take
// effect.
func (c *Cache) Set(key string, entry Entry) {
	c.setWithPolicySession(key, "", entry, pathPolicy{})
}

func (c *Cache) setWithPolicySession(key, sessionKey string, entry Entry, policy pathPolicy) {
	key = normalizeKey(key)
	if key == "" || !entry.cacheable(c.maxBody) {
		return
	}
	now := time.Now()
	diskTTL := c.diskTTL
	if policy.DiskTTL > 0 {
		diskTTL = policy.DiskTTL
	}
	if entry.DiskExpiresAt.IsZero() || !entry.DiskExpiresAt.After(now) {
		entry.DiskExpiresAt = now.Add(diskTTL)
	}
	entry = cloneEntry(entry)
	c.putMemoryWithPolicySession(key, sessionKey, entry, now, policy)
	if err := c.putDisk(key, entry, now); err != nil {
		config.Logger.Warn("[response_cache] disk write failed", "error", err)
	} else {
		c.enforceDiskLimit(now)
	}
	c.recordStore(policy.Path)
	c.maybeSweepDisk(now)
}

func (c *Cache) requestBodyTooLarge(r *http.Request) bool {
	if c.maxBody <= 0 || r == nil {
		return false
	}
	return r.ContentLength > c.maxBody
}

func (c *Cache) readRequestBody(r *http.Request) ([]byte, bool, error) {
	if r == nil || r.Body == nil {
		return nil, false, nil
	}
	if c.maxBody <= 0 {
		raw, err := io.ReadAll(r.Body)
		return raw, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, c.maxBody+1))
	if err != nil {
		return raw, false, err
	}
	if int64(len(raw)) > c.maxBody {
		return raw, true, nil
	}
	return raw, false, nil
}

type replayReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r replayReadCloser) Close() error {
	if r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

func replayBody(prefix []byte, rest io.ReadCloser) io.ReadCloser {
	if rest == nil {
		return io.NopCloser(bytes.NewReader(prefix))
	}
	return replayReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), rest),
		closer: rest,
	}
}

func canonicalRequestPath(path string) string {
	path = strings.TrimSpace(path)
	switch path {
	case "/chat/completions", "/v1/v1/chat/completions":
		return "/v1/chat/completions"
	case "/responses", "/v1/v1/responses":
		return "/v1/responses"
	case "/embeddings", "/v1/v1/embeddings":
		return "/v1/embeddings"
	case "/anthropic/v1/messages", "/messages", "/v1/v1/messages":
		return "/v1/messages"
	case "/anthropic/v1/messages/count_tokens", "/messages/count_tokens", "/v1/v1/messages/count_tokens":
		return "/v1/messages/count_tokens"
	default:
		return path
	}
}

func (c *Cache) putMemoryWithPolicySession(key, sessionKey string, entry Entry, now time.Time, policy pathPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepMemoryLocked(now)
	size := entry.memorySize()
	if c.memoryMaxBytes > 0 && size > c.memoryMaxBytes {
		return
	}
	memoryTTL := c.memoryTTL
	if policy.MemoryTTL > 0 {
		memoryTTL = policy.MemoryTTL
	}
	expiresAt := now.Add(memoryTTL)
	if entry.DiskExpiresAt.Before(expiresAt) {
		expiresAt = entry.DiskExpiresAt
	}
	if expiresAt.After(now) {
		if old, ok := c.items[key]; ok {
			c.memoryBytes -= old.size
			if old.sessionKey != "" && old.sessionKey != sessionKey {
				c.removeFromSessionLocked(old.sessionKey, key)
			}
		}
		c.items[key] = memoryEntry{
			entry:           cloneEntry(entry),
			memoryExpiresAt: expiresAt,
			size:            size,
			sessionKey:      sessionKey,
		}
		c.memoryBytes += size
		c.addToSessionLocked(sessionKey, key)
		// Storing a new entry IS session activity. Refresh sibling leases
		// so a long-running conversation's earlier turns do not get swept
		// out from under it just because a new turn took longer than
		// memoryTTL to arrive.
		if sessionKey != "" {
			if siblings, ok := c.sessionEntries[sessionKey]; ok {
				for siblingKey := range siblings {
					if siblingKey == key {
						continue
					}
					sibling, ok := c.items[siblingKey]
					if !ok {
						continue
					}
					c.bumpSingleEntryExpiryLocked(siblingKey, sibling, now, memoryTTL)
				}
			}
		}
		c.enforceMemoryLimitLocked()
	}
}

func (c *Cache) sweepMemoryLocked(now time.Time) {
	for key, item := range c.items {
		if !now.Before(item.memoryExpiresAt) || !now.Before(item.entry.DiskExpiresAt) {
			delete(c.items, key)
			c.memoryBytes -= item.size
			c.removeFromSessionLocked(item.sessionKey, key)
		}
	}
	if c.memoryBytes < 0 {
		c.memoryBytes = 0
	}
}

func (c *Cache) enforceMemoryLimitLocked() {
	if c.memoryMaxBytes <= 0 || c.memoryBytes <= c.memoryMaxBytes {
		return
	}
	// Replace the previous O(N²) "scan to find oldest, delete, repeat"
	// loop with a single O(N log N) sort. Under sustained store pressure
	// the old version was a measurable DoS vector — an attacker with
	// caller-bound write access could drive the cache into the eviction
	// path on every store and pay len(items) per eviction. With an
	// up-front sort the eviction-loop cost amortizes: one sort + linear
	// walk down the list until memoryBytes is back under the cap.
	type evictCandidate struct {
		key       string
		expiresAt time.Time
		size      int64
		session   string
	}
	candidates := make([]evictCandidate, 0, len(c.items))
	for key, item := range c.items {
		candidates = append(candidates, evictCandidate{
			key:       key,
			expiresAt: item.memoryExpiresAt,
			size:      item.size,
			session:   item.sessionKey,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].expiresAt.Before(candidates[j].expiresAt)
	})
	for _, cand := range candidates {
		if c.memoryBytes <= c.memoryMaxBytes {
			break
		}
		if _, ok := c.items[cand.key]; !ok {
			continue
		}
		delete(c.items, cand.key)
		c.memoryBytes -= cand.size
		c.removeFromSessionLocked(cand.session, cand.key)
	}
	if c.memoryBytes < 0 {
		c.memoryBytes = 0
	}
}

func (c *Cache) recordHit(path, source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordHitLocked(path, source)
}

func (c *Cache) recordHitLocked(path, source string) {
	c.hits++
	switch source {
	case "memory":
		c.memoryHits++
	case "disk":
		c.diskHits++
	}
	stat := c.pathStatLocked(path)
	if stat == nil {
		return
	}
	stat.Hits++
	switch source {
	case "memory":
		stat.MemoryHits++
	case "disk":
		stat.DiskHits++
	}
}

func (c *Cache) recordMiss(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.misses++
	if stat := c.pathStatLocked(path); stat != nil {
		stat.Misses++
	}
}

func (c *Cache) recordStore(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stores++
	if stat := c.pathStatLocked(path); stat != nil {
		stat.Stores++
	}
}

func (c *Cache) recordInflightHit(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflightHits++
	if stat := c.pathStatLocked(path); stat != nil {
		stat.InflightHits++
		stat.Hits++
	}
	c.hits++
}

// acquireInflight registers the caller as the owner of a single-flight slot
// when no other request is in flight for the same key, or returns the
// existing slot for the caller to wait on. Returns (slot, isOwner). Owners
// MUST call releaseInflight in a defer so waiters can wake.
func (c *Cache) acquireInflight(key string) (*inflightSlot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight == nil {
		c.inflight = map[string]*inflightSlot{}
	}
	if slot, ok := c.inflight[key]; ok {
		return slot, false
	}
	slot := &inflightSlot{done: make(chan struct{})}
	c.inflight[key] = slot
	return slot, true
}

// releaseInflight closes the slot's done channel and removes it from the
// in-flight map. Safe to call multiple times — the second call is a no-op.
func (c *Cache) releaseInflight(key string) {
	c.mu.Lock()
	slot, ok := c.inflight[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	delete(c.inflight, key)
	c.mu.Unlock()
	close(slot.done)
}

func (c *Cache) recordUncacheable(path, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unknown"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.uncacheable == nil {
		c.uncacheable = map[string]int64{}
	}
	c.uncacheable[reason]++
	stat := c.pathStatLocked(path)
	if stat == nil {
		return
	}
	if stat.Uncacheable == nil {
		stat.Uncacheable = map[string]int64{}
	}
	stat.Uncacheable[reason]++
}

// pathStatLocked returns the per-path counter bucket, lazily creating it on
// first touch. Must be called with c.mu held. Returns nil for empty paths so
// callers can opt out cleanly when path is not known.
func (c *Cache) pathStatLocked(path string) *pathStat {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if c.pathStats == nil {
		c.pathStats = map[string]*pathStat{}
	}
	stat, ok := c.pathStats[path]
	if !ok {
		stat = &pathStat{Uncacheable: map[string]int64{}}
		c.pathStats[path] = stat
	}
	return stat
}

func (c *Cache) putDisk(key string, entry Entry, now time.Time) error {
	if c.dir == "" {
		return nil
	}
	path, ok := c.diskPath(key)
	if !ok {
		return fmt.Errorf("invalid response cache path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	gz := gzip.NewWriter(tmp)
	record := diskRecord{
		Version:       recordVersion,
		Key:           key,
		CreatedAtUnix: now.Unix(),
		ExpiresAtUnix: entry.DiskExpiresAt.Unix(),
		Status:        entry.Status,
		Header:        cloneHeader(entry.Header),
		Body:          append([]byte(nil), entry.Body...),
	}
	encodeErr := json.NewEncoder(gz).Encode(record)
	closeGZErr := gz.Close()
	closeFileErr := tmp.Close()
	if encodeErr != nil {
		return encodeErr
	}
	if closeGZErr != nil {
		return closeGZErr
	}
	if closeFileErr != nil {
		return closeFileErr
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}

func (c *Cache) getDisk(key string, now time.Time) (Entry, bool) {
	if c.dir == "" {
		return Entry{}, false
	}
	path, ok := c.diskPath(key)
	if !ok {
		return Entry{}, false
	}
	// #nosec G304 -- path is derived from a normalized SHA-256 cache key and root-checked.
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, false
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			config.Logger.Warn("[response_cache] close cache file failed", "error", closeErr)
		}
	}()
	gz, err := gzip.NewReader(f)
	if err != nil {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	defer func() {
		if closeErr := gz.Close(); closeErr != nil {
			config.Logger.Warn("[response_cache] close gzip reader failed", "error", closeErr)
		}
	}()
	var record diskRecord
	if err := json.NewDecoder(gz).Decode(&record); err != nil {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	expiresAt := time.Unix(record.ExpiresAtUnix, 0)
	if record.Version != recordVersion || record.Key != key || !expiresAt.After(now) {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	entry := Entry{
		Status:        record.Status,
		Header:        sanitizeResponseHeader(record.Header),
		Body:          append([]byte(nil), record.Body...),
		DiskExpiresAt: expiresAt,
	}
	if !entry.cacheable(c.maxBody) {
		_ = removeDiskCacheFile(c.dir, path)
		return Entry{}, false
	}
	return entry, true
}

func (c *Cache) maybeSweepDisk(now time.Time) {
	c.mu.Lock()
	if now.Sub(c.lastDiskSweep) < 10*time.Minute {
		c.mu.Unlock()
		return
	}
	c.lastDiskSweep = now
	dir := c.dir
	diskTTL := c.diskTTL
	c.mu.Unlock()

	if dir == "" {
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || !strings.HasSuffix(path, ".json.gz") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if now.Sub(info.ModTime()) > diskTTL {
			_ = removeDiskCacheFile(dir, path)
		}
		return nil
	})
}

type diskCacheFile struct {
	path    string
	size    int64
	modTime time.Time
}

func (c *Cache) enforceDiskLimit(now time.Time) {
	c.mu.Lock()
	dir := c.dir
	diskTTL := c.diskTTL
	maxBytes := c.diskMaxBytes
	c.mu.Unlock()
	if dir == "" || maxBytes <= 0 {
		return
	}
	files, total := c.diskFilesAt(dir, diskTTL, now)
	if total <= maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	for _, file := range files {
		if total <= maxBytes {
			return
		}
		if err := removeDiskCacheFile(dir, file.path); err == nil {
			total -= file.size
		}
	}
}

// diskFilesAt walks the cache directory using a snapshot of (dir, diskTTL)
// captured under c.mu by the caller. Reading these from the Cache struct
// directly inside the WalkDir callback is racy with ApplyOptions hot
// reload, which mutates them under the same mutex.
func (c *Cache) diskFilesAt(dir string, diskTTL time.Duration, now time.Time) ([]diskCacheFile, int64) {
	files := []diskCacheFile{}
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() || !strings.HasSuffix(path, ".json.gz") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if diskTTL > 0 && now.Sub(info.ModTime()) > diskTTL {
			_ = removeDiskCacheFile(dir, path)
			return nil
		}
		size := info.Size()
		total += size
		files = append(files, diskCacheFile{path: path, size: size, modTime: info.ModTime()})
		return nil
	})
	return files, total
}

func (c *Cache) diskPath(key string) (string, bool) {
	key = normalizeKey(key)
	if key == "" {
		return "", false
	}
	var path string
	if len(key) < 2 {
		path = filepath.Join(c.dir, key+".json.gz")
	} else {
		path = filepath.Join(c.dir, key[:2], key+".json.gz")
	}
	return path, pathWithinDir(c.dir, path)
}

func removeDiskCacheFile(root, path string) error {
	if !pathWithinDir(root, path) {
		return fmt.Errorf("response cache path escapes root")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	// #nosec G122 -- path is root-checked and symlinks are ignored before removal.
	return os.Remove(path)
}

func pathWithinDir(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeCachedResponse(w http.ResponseWriter, entry Entry, source string) {
	for k, vv := range sanitizeResponseHeader(entry.Header) {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("X-DeepSeek-Web-To-API-Cache", source)
	w.Header().Set("X-DeepSeek-Web-To-API-Cache-Expires-At", entry.DiskExpiresAt.UTC().Format(time.RFC3339))
	if entry.Status == 0 {
		entry.Status = http.StatusOK
	}
	w.WriteHeader(entry.Status)
	if len(entry.Body) > 0 {
		if _, err := w.Write(entry.Body); err != nil {
			config.Logger.Warn("[response_cache] replay write failed", "error", err)
		}
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (e Entry) cacheable(maxBody int64) bool {
	return e.uncacheableReason(maxBody) == ""
}

func (e Entry) uncacheableReason(maxBody int64) string {
	if e.Status < 200 || e.Status >= 300 {
		return "status_non_2xx"
	}
	if len(e.Body) == 0 {
		return "empty_body"
	}
	if maxBody > 0 && int64(len(e.Body)) > maxBody {
		return "oversized_response"
	}
	if hasHeaderToken(e.Header.Get("Cache-Control"), "no-store") {
		return "response_no_store"
	}
	if len(e.Header.Values("Set-Cookie")) > 0 {
		return "set_cookie"
	}
	return ""
}

func (e Entry) memorySize() int64 {
	size := int64(len(e.Body))
	for k, vv := range e.Header {
		size += int64(len(k))
		for _, v := range vv {
			size += int64(len(v))
		}
	}
	return size
}

type captureResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
	maxBody     int64
	truncated   bool
}

func newCaptureResponseWriter(w http.ResponseWriter, maxBody int64) *captureResponseWriter {
	return &captureResponseWriter{ResponseWriter: w, maxBody: maxBody}
}

func (w *captureResponseWriter) Header() http.Header {
	return w.ResponseWriter.Header()
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	// Defence-in-depth XSS mitigation. The cached response body is
	// upstream-LLM-generated content forwarded to the client through
	// this wrapper; while the global securityHeaders middleware already
	// sets X-Content-Type-Options: nosniff, pinning it here ensures the
	// header survives any handler that explicitly removes it AND keeps
	// CodeQL go/reflected-xss flow analysis quiet at the wrapped Write.
	if w.Header().Get("X-Content-Type-Options") == "" {
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.capture(p)
	return w.ResponseWriter.Write(p)
}

func (w *captureResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(w, r)
}

func (w *captureResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *captureResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (w *captureResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *captureResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *captureResponseWriter) capture(p []byte) {
	if w.truncated || len(p) == 0 {
		return
	}
	if w.maxBody > 0 && int64(w.body.Len()+len(p)) > w.maxBody {
		remaining := int(w.maxBody) - w.body.Len()
		if remaining > 0 {
			_, _ = w.body.Write(p[:remaining])
		}
		w.truncated = true
		return
	}
	_, _ = w.body.Write(p)
}

func (w *captureResponseWriter) cacheEntry() (Entry, bool, string) {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	entry := Entry{
		Status: status,
		Header: sanitizeResponseHeader(w.Header()),
		Body:   append([]byte(nil), w.body.Bytes()...),
	}
	if w.truncated {
		return Entry{}, false, "oversized_response"
	}
	if reason := entry.uncacheableReason(w.maxBody); reason != "" {
		return Entry{}, false, reason
	}
	return entry, true, ""
}

func requestForcesBypass(r *http.Request) bool {
	if r == nil {
		return false
	}
	cacheControl := r.Header.Get("Cache-Control")
	if hasHeaderToken(cacheControl, "no-cache") || hasHeaderToken(cacheControl, "no-store") {
		return true
	}
	if strings.EqualFold(requestHeaderValue(r.Header, "X-DeepSeek-Web-To-API-Cache-Control", "X-Ds2-Cache-Control"), "bypass") {
		return true
	}
	return false
}

func hasHeaderToken(raw, token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	for _, part := range strings.Split(raw, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == token {
			return true
		}
	}
	return false
}

type varyRequestHeader struct {
	Name   string
	Legacy []string
}

func varyRequestHeaders() []varyRequestHeader {
	return []varyRequestHeader{
		{Name: "Accept"},
		{Name: "Anthropic-Beta"},
		{Name: "Anthropic-Version"},
		{Name: "Content-Type"},
		{Name: "X-DeepSeek-Web-To-API-Session-Affinity-Key", Legacy: []string{"X-Ds2-Session-Affinity-Key"}},
		{Name: "X-DeepSeek-Web-To-API-Source", Legacy: []string{"X-Ds2-Source"}},
		{Name: "X-DeepSeek-Web-To-API-Target-Account", Legacy: []string{"X-Ds2-Target-Account"}},
	}
}

func requestHeaderValue(h http.Header, primary string, legacy ...string) string {
	if value := strings.TrimSpace(h.Get(primary)); value != "" {
		return value
	}
	for _, name := range legacy {
		if value := strings.TrimSpace(h.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func requestHeaderValues(h http.Header, primary string, legacy ...string) []string {
	if values := h.Values(primary); len(values) > 0 {
		return values
	}
	for _, name := range legacy {
		if values := h.Values(name); len(values) > 0 {
			return values
		}
	}
	return nil
}

func writeKeyPart(w io.Writer, value string) {
	_, _ = io.WriteString(w, value)
	_, _ = w.Write([]byte{0})
}

func canonicalQuery(values map[string][]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		vals := append([]string(nil), values[key]...)
		sort.Strings(vals)
		parts = append(parts, key+"="+strings.Join(vals, "\x1f"))
	}
	return strings.Join(parts, "&")
}

func sanitizeResponseHeader(h http.Header) http.Header {
	out := http.Header{}
	for k, vv := range h {
		if skipResponseHeader(k) {
			continue
		}
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	return out
}

func skipResponseHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "content-length", "date", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"x-deepseek-web-to-api-cache", "x-deepseek-web-to-api-cache-expires-at",
		"x-ds2-cache", "x-ds2-cache-expires-at":
		return true
	default:
		return false
	}
}

func cloneEntry(in Entry) Entry {
	return Entry{
		Status:        in.Status,
		Header:        cloneHeader(in.Header),
		Body:          append([]byte(nil), in.Body...),
		DiskExpiresAt: in.DiskExpiresAt,
	}
}

func cloneHeader(in http.Header) http.Header {
	if in == nil {
		return http.Header{}
	}
	out := make(http.Header, len(in))
	for k, vv := range in {
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func normalizeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(key); err != nil {
		return ""
	}
	return key
}

func (c *Cache) Stats() map[string]any {
	if c == nil {
		return map[string]any{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lookups := c.hits + c.misses
	cacheableLookups := c.hits + c.stores
	cacheableMisses := c.stores
	uncacheableTotal := int64(0)
	for _, count := range c.uncacheable {
		uncacheableTotal += count
	}
	out := map[string]any{
		"lookups":            lookups,
		"hits":               c.hits,
		"misses":             c.misses,
		"stores":             c.stores,
		"cacheable_lookups":  cacheableLookups,
		"cacheable_misses":   cacheableMisses,
		"uncacheable_misses": uncacheableTotal,
		"memory_hits":        c.memoryHits,
		"disk_hits":          c.diskHits,
		"inflight_hits":      c.inflightHits,
		"inflight_pending":   len(c.inflight),
		"session_hits":       c.sessionHits,
		"active_sessions":    len(c.sessionEntries),
		"memory_items":       len(c.items),
		"memory_bytes":       c.memoryBytes,
		"memory_max_bytes":   c.memoryMaxBytes,
		"max_body_bytes":     c.maxBody,
		"memory_ttl_seconds": int(c.memoryTTL.Seconds()),
		"disk_ttl_seconds":   int(c.diskTTL.Seconds()),
		"disk_max_bytes":     c.diskMaxBytes,
		"disk_dir":           c.dir,
		"compression":        "gzip",
		"semantic_key":       c.semanticKey,
	}
	for reason, count := range c.uncacheable {
		out["uncacheable_"+reason] = count
	}
	if len(c.pathStats) > 0 {
		paths := make(map[string]any, len(c.pathStats))
		for path, stat := range c.pathStats {
			pathLookups := stat.Hits + stat.Misses
			pathCacheableLookups := stat.Hits + stat.Stores
			uncacheableSum := int64(0)
			for _, count := range stat.Uncacheable {
				uncacheableSum += count
			}
			pathOut := map[string]any{
				"lookups":            pathLookups,
				"hits":               stat.Hits,
				"misses":             stat.Misses,
				"stores":             stat.Stores,
				"memory_hits":        stat.MemoryHits,
				"disk_hits":          stat.DiskHits,
				"inflight_hits":      stat.InflightHits,
				"cacheable_lookups":  pathCacheableLookups,
				"cacheable_misses":   stat.Stores,
				"uncacheable_misses": uncacheableSum,
				"hit_rate":           safeRate(stat.Hits, pathLookups),
				"cacheable_hit_rate": safeRate(stat.Hits, pathCacheableLookups),
				"shared":             pathPolicyFor(path).SharedAcrossCallers,
			}
			for reason, count := range stat.Uncacheable {
				pathOut["uncacheable_"+reason] = count
			}
			paths[path] = pathOut
		}
		out["paths"] = paths
	}
	return out
}

// safeRate returns hits/total rounded to 4 decimals, or 0 when total is zero.
func safeRate(hits, total int64) float64 {
	if total <= 0 {
		return 0
	}
	r := float64(hits) / float64(total)
	// Round to 4 decimal places to avoid jitter in admin UI rendering.
	return float64(int64(r*10000)) / 10000
}

func (c *Cache) String() string {
	if c == nil {
		return "response cache disabled"
	}
	return fmt.Sprintf("response cache memory=%s memory_max=%d disk=%s disk_max=%d dir=%s compression=gzip semantic_key=%t", c.memoryTTL, c.memoryMaxBytes, c.diskTTL, c.diskMaxBytes, c.dir, c.semanticKey)
}
