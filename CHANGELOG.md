# 更新日志

## 2026-05-06 (1.0.5)

v1.0.5 是定点 bug 修复版本：修复 admin metrics overview 中 24h / 7d / 15d token 统计窗口的不一致 skew。VERSION 从 `1.0.4` 升到 `1.0.5`。

### 子段 v1.0.5 修复

- **24h/7d/15d token 统计窗口一致性修复**：`/admin/metrics/overview` 的 `token_windows` 节点之前会出现 `requests` 与 `tokens` 比例错乱，且 24h/7d/15d 窗口内 token 总量比对应的请求数偏高。根因在 [`internal/chathistory/sqlite_metrics.go`](internal/chathistory/sqlite_metrics.go) 与 [`internal/chathistory/metrics.go`](internal/chathistory/metrics.go) 用了两套不同的时间字段判断窗口归属：`WindowRequests` 计数走 `created_at`，`Window` token 聚合走 `updated_at OR completed_at`。任何被后续元数据修改（status / finish_reason 重写、close 事件等）刷新过 `updated_at` 的旧行 → tokens 漏入近 24h/7d/15d 窗口、但 request 数不动 → 窗口数字对不上。本版本把两个判断都钉到单一权威时间戳：`completed_at`（账单时刻），`completed_at <= 0` 时回退到 `created_at`，使窗口归属物理上一致。新增回归测试 `TestTokenUsageStatsWindowConsistency`：构造一行 2 小时前完成、刚被刷过 `updated_at` 的行，断言它不进 1 分钟窗口（该测试在修复前必然失败）。

## 2026-05-06 (1.0.4)

v1.0.4 是 v1.0.3 milestone 之后的累积小迭代,包含上游 CJackHwang/ds2api v4.2.x → v4.4.x 选择性跟进、CNB PR #15 前端轮询频率优化、以及 GitHub Issue #9 的运维侧文档化。VERSION 从 `1.0.3` 升到 `1.0.4`,正式打 v1.0.4 tag(也是 GitHub Release 的发布载体);v1.0.3 milestone 下的所有累积工作(CDATA 管道变体兼容 / 5-store SQLite / 三层缓存粘性 / 自动拉黑 / 思考超时 7200s 全套 / 安全审计闭环 / MIT 重新许可)继续保留为 1.0.3 子段历史,在本次 release 中也包含。

### 子段 v1.0.4 增量

- **上游 v4.2.x → v4.4.x 选择性跟进(P0×2 + P1×3 / 2 项验证为 N/A)**:见 1.0.3 子段中的"上游 v4.2.x → v4.4.x 选择性跟进"条目(为方便阅读按时间挂在 1.0.3 子段下,实际发布带 v1.0.4 tag)。
- **webui 前端轮询频率优化(CNB PR #15 采纳)**:三处常量 — `OverviewContainer.HISTORY_SAMPLE_LIMIT 500 → 50` / `REFRESH_MS 2500 → 10000` / `DashboardShell.VERSION_CHECK_INTERVAL_MS 30000 → 600000`。GitHub release tag 检查从 30s 拉到 10 min,不再触发 unauthenticated REST API 60 req/h 上限的 403。
- **GitHub Issue #9 应对文档**:[`docs/client-compat/claude-code.md` §12](docs/client-compat/claude-code.md) 新增两条已知问题表项 — Claude Code 客户端 BASH 工具默认 120s 超时(用 `BASH_DEFAULT_TIMEOUT_MS` / `BASH_MAX_TIMEOUT_MS` 环境变量延长)+ 思考/工具超时后 session-affinity 卡顿(建议把 `account_max_inflight` 调到 1 让请求 fail-fast)。同步加 v1.0.2 → v1.0.3 升级警示。

## 2026-05-06 (1.0.3)

本次发布把 v1.0.4 ~ v1.0.12 期间的所有修复与新功能整合回 v1.0.3 版本号下，便于 CNB 主分支保留单一版本里程碑。下面按子段保留每次迭代的明细，方便审计。

### 子段 v1.0.3 增量（文档与 Claude Code）

- **重复违规自动拉黑 + IP 黑/白名单 WebUI 修复**：1) 自动拉黑：`internal/requestguard/guard.go` 新增 `autoBanTracker`，对触发 `content_blocked` / `content_regex_blocked` / `jailbreak_blocked` 的 IP 在滑动窗口内累计计数（默认 10 min 内 3 次），到阈值后调用新的 `safetystore.IPsStore.AddBlockedIP()` 增量写入 `safety_ips.blocked_ips`，并把 `policyCache.signature` 置空触发下一次请求重建 IP 匹配表 — 下一次同 IP 来访直接走 `ip_blocked` 终止，不再进入内容扫描。配置项 `safety.auto_ban.{enabled, threshold, window_seconds}` 暴露在 [internal/config/config.go](internal/config/config.go) 的 `SafetyAutoBanConfig` 结构里，默认 enabled=true / threshold=3 / window=600。命中白名单（`safety_ips.allowed_ips`）的 IP 触发违规仍会被当次拦截但不会被自动拉黑（`isAllowlistedLocked` 在 ban 写入前查 SQLite 兜底）。2) WebUI 修复：之前 `handler_settings_read.go` 只返回 `snap.Safety` 的 legacy 字段，v1.0.11 后真值在 `safety_ips.sqlite` / `safety_words.sqlite` 里 → 控制台看到的列表全空。本版本新增 `safetyResponse` 把 SQLite store 的 `Snapshot()` 与 legacy 列表合并去重后回填给前端；同时补全 `allowed_ips` 字段（之前 `SafetyConfig` 没有这个字段、admin 写路径也不处理它），现在 `config.SafetyConfig.AllowedIPs` / 写路径 `ReplaceAllowedIPs` / 解析路径 `stringSliceFrom(raw["allowed_ips"])` 三处对齐。3) WebUI 控件：`SafetyPolicySection.jsx` 新增"IP 白名单"文本框 + "重复违规自动拉黑"勾选 + 阈值/窗口输入；`useSettingsForm.js` 同步新增 `allowed_ips_text` / `auto_ban` 表单字段；中英 i18n 文案补齐。回归测试 `TestAutoBanTripsAfterRepeatedContentViolations` 用真实 SQLite store 验证：连续 3 次 `content_blocked` 后 `192.0.2.42` 被写入 SQLite，第 4 次以 `ip_blocked` 拦截。
- **上游 v4.2.x → v4.4.x 选择性跟进(P0×2 + P1×3 / 2 项验证为 N/A)**：基于 Sonnet 4.6 三 Agent 调研报告 [`upstream-adoption-plan-2026-05-06T00-00-00Z.md`](.claude/state/agent-reports/upstream-adoption-plan-2026-05-06T00-00-00Z.md) 在我们现有代码上重写实现,而非 cherry-pick(避免与 v1.0.3-cnb 防御项冲突)。**P0-1 思考-only 视为成功不再 429**:[`internal/httpapi/openai/shared/upstream_empty.go`](internal/httpapi/openai/shared/upstream_empty.go) 的 `ShouldWriteUpstreamEmptyOutputError(text, thinking)` 新签名同时检查文本与 thinking — Pro 推理模型偶发只产 thinking 不产 text 时不再被误判 empty,reasoning 直接当 success 返回(对应 CJackHwang/ds2api a7522b41 + a299c7d1)。同步退出重试谓词 `shouldRetryChatNonStream` / `shouldRetryResponsesNonStream` 增加 thinking 检查,避免对故意只产推理的请求空跑重试。[`handler_chat.go`](internal/httpapi/openai/chat/handler_chat.go)、[`handler_messages_direct.go`](internal/httpapi/claude/handler_messages_direct.go) 调用方对齐新签名。回归测试 4 个旧契约用例(`TestHandleNonStreamReturns429WhenUpstreamHasOnlyThinking` 等)从"必须 429"改写为"必须 200 + 不重试"。**P0-2 短横线 DSML 标签兼容**:Cherry Studio + 一些上游派生客户端发 `<dsml-tool-calls>` / `<dsml-invoke>` / `<dsml-parameter>` 短横线分隔形式。[`internal/toolcall/toolcalls_scan.go`](internal/toolcall/toolcalls_scan.go) `toolMarkupNames` 加 `tool-calls` / `tool-call`,`canonicalToolMarkupName` 映射回 `tool_calls`,新增 `consumeToolMarkupHyphenSeparator` 在前缀已识别 dsml 后接受 `-`/`_` 分隔(只在后接已识别名称时消耗,不污染普通文本)。JS sieve [`parse_payload.js`](internal/js/helpers/stream-tool-sieve/parse_payload.js) 同步加 `canonicalToolMarkupName` 与 `replaceDSMLToolMarkupOutsideIgnored` 的 hyphen 改写路径。Go + JS 各 3 个回归用例(纯 hyphen / 纯 underscore / 混合)。**P1-1 ctx-cancel 历史竞态**:[`empty_retry_runtime.go`](internal/httpapi/openai/chat/empty_retry_runtime.go) 与 [`responses/empty_retry_runtime.go`](internal/httpapi/openai/responses/empty_retry_runtime.go) 的 `recordChatStreamHistory` / `recordResponsesStreamHistory` 在 `finalErrorCode == StopReasonContextCancelled` 时早退,避免 `OnContextDone` 已写的 `stopped` 历史被 finalize 路径的 `error()` 覆盖,Admin 失败率分母不再被客户端中断污染(对应 0bca6e2c)。**P1-2 invoke body 裸 JSON 数组**:[`toolcalls_parse_markup.go`](internal/toolcall/toolcalls_parse_markup.go) `parseSingleXMLToolCall` 在 `inner.HasPrefix("[")` 时 JSON-unmarshal 后包到 `input["items"]`,MultiEdit 这种数组型 schema 工具的 invoke body 不再被丢弃(对应 1c38709d)。**P1-3 WebUI Windows MIME 钉死**:[`internal/webui/handler.go`](internal/webui/handler.go) 新增 `setExplicitContentType` 在 `http.ServeFile` 之前按扩展名硬编码 `.css/.js/.json/.svg/.woff2` 等的 Content-Type,绕过 Windows `HKEY_CLASSES_ROOT` 注册表被第三方应用篡改导致 `.css → application/xml` 拆台 admin 样式表的已知问题(对应 7870a61b)。**两项验证为 N/A**:`706e68de` `MaxKeepaliveCount=10` — 我们在 v1.0.3 思考超时统一时已设 1440;`fd0ec299` tiktoken CGo 拆分 — 我们用的 `hupe1980/go-tiktoken` 是纯 Go 库,Alpine docker 构建在 v1.0.3-cnb-r1 release 已验证可跑。Reject 项 `7e639667` 删除启发式模型名匹配 + `1286b022` 删 compat 配置层均不采(我们用户依赖这些功能)。
- **许可证 AGPLv3 → MIT**：[`LICENSE`](LICENSE) 替换为标准 MIT 文本（版权署 `2024-2026 DeepSeek_Web_To_API contributors`），README 中文版与英文版加 "许可证 / License" 章节链接到新 LICENSE，[`webui/package.json`](webui/package.json) 加 `"license": "MIT"` 字段以便包管理器与扫描工具识别。仓库历史里没有任何源码 header 标 AGPL，所以本次只动 LICENSE / README / package.json 三处即可让许可证状态自洽。
- **思考全程超时统一 7200s + 安全审计 P2 收尾**：1) `internal/deepseek/protocol/constants.go` 默认 `StreamIdleTimeout` 从 1800s（30 min）拉到 7200s（与 v1.0.6 起 `HTTPTotalTimeout` 7200s 对齐），`MaxKeepaliveCount` 从 360 拉到 1440（按 5s keep-alive 间隔等比缩放）。Pro 推理模型在 thinking + tool_use 长循环里跨越 30 min 时再也不会被 SSE idle 超时砍掉。2) `internal/responsecache/cache.go` 的 `inflightWaitTimeout` 从 30s 拉到 2h，与 HTTP 总超时一致；owner 通过 defer 关 inflight slot，panic / 早返回时 waiter 立即唤醒，所以这条上限只是"owner 真在长思考时 waiter 不要群起冲上游"的兜底，不会因 owner 死掉而无限阻塞。3) P2 收尾四项:`enforceMemoryLimitLocked` 从 O(N²)（每次驱逐扫描全表找最老）改成单次 `sort.Slice + 线性走` 的 O(N log N)；`safetystore.NewIPsStore` 加 `SetMaxOpenConns(1)` 是 load-bearing 的明确文档说明（DELETE-then-INSERT 事务原子性依赖单一连接）；`requestguard.Middleware` 的 `tracker.note()` 返回值显式 `_ =` 加注释说明不丢弃信息（tracker 内部 WARN log + SQLite 行已是完整审计轨迹）；`/admin/*` 与 WebUI 静态资源响应加 `Content-Security-Policy` 头（`default-src 'self'; script-src 'self'; connect-src 'self' https://api.github.com; frame-ancestors 'none'` 等），收住 XSS 一旦触发后的爆炸半径，与之前的 localStorage JWT 形成 defense-in-depth。
- **会话级粘性（session-wide 滑动窗口）**：上版本的 sliding window 是 per-key 的，现把粘性升到会话维度。`memoryEntry` 新增 `sessionKey` 字段；Cache 新增 `sessionEntries map[string]map[string]struct{}` 把同一逻辑会话（按 `account.SessionKey(sha256(owner), body)` 计算的指纹）的所有 cache key 挂到同一个 bucket。`bumpMemoryExpiryLocked` 在命中后除续租自身外，遍历 bucket 给所有兄弟条目都续租；新增 `bumpSingleEntryExpiryLocked` 提取单条续租逻辑供会话级和键级共用。`putMemoryWithPolicySession` 新增"store 即会话活动"语义：每次 store 都同步刷整个会话所有现存兄弟的 lease，避免长会话里早 turn 在新 turn store 之前就先按原 TTL 被 sweep。三处删除点（`sweepMemoryLocked` / `enforceMemoryLimitLocked` / `getWithPolicy` 过期分支）同步 `removeFromSessionLocked` 维护 bucket，`ApplyOptions` 重置时也清空 sessionEntries。新增 `session_hits`（会话级 bump 触发次数）/ `active_sessions`（当前活跃会话桶数）两个全局指标，admin overview 同步暴露。`/v1/embeddings` 与 `/v1/messages/count_tokens` 等 SharedAcrossCallers 路径不参与会话链接（这些路径无对话边界）。回归测试 `TestSessionWideStickyTTLRefreshesSiblingsOnHit` 用 200ms TTL 模拟两 turn 同会话场景：turn1 在 prime 后 200ms 即原 TTL 边界，但因 turn2 store + hit 触发会话级续租，turn1 在 390ms 时仍命中 memory，且 active_sessions=1、session_hits>=1。这是上版本"per-key sliding window"的自然延伸，把粘性从"同一请求重发"扩到"同一会话内任意 turn 活跃就续整个对话上下文"。
- **缓存对话粘性增强（滑动窗口 TTL + 单飞 in-flight dedup）**：[`internal/responsecache/cache.go`](internal/responsecache/cache.go) 新增两条互补的对话-缓存粘性机制。① 滑动窗口 TTL：`bumpMemoryExpiryLocked` 在每次内存命中时把条目的 `memoryExpiresAt` 续到 `now + memoryTTL`（封顶到 disk 过期），活跃对话反复命中同一前缀时该条目被无限续租，闲置后才按 TTL 自然过期；用 `memoryMaxBytes` LRU 兜住内存占用。② 单飞 in-flight dedup：新增 `inflight map[string]*inflightSlot` 与 `acquireInflight` / `releaseInflight` / `waitForInflightOwner`，相同 cache key 的并发请求只让 owner 走上游，其余 N-1 个 waiter 阻塞在 `done` channel 上（30s timeout 兜底）。owner 写完缓存关闭 channel，waiter 唤醒后 re-Get 命中并按 `inflight_hits` 统计；owner 响应不可缓存或超时则 waiter 走 fall-through 自己跑上游一次（不双倍记账）。这两条专门解决"客户端流式请求中网络抖动 → 浏览器 / SDK 自动重试 → 上游被打两次"以及"用户双击 send → 同请求并发"两个最常见的对话粘性场景。新增 `inflight_hits` / `inflight_pending` 两个全局指标 + per-path `inflight_hits`，在 [`/admin/metrics/overview`](internal/httpapi/admin/metrics/handler.go) 的 `cache` 节点暴露。两个 race-safe 回归测试固化语义：`TestSlidingWindowKeepsActiveEntryHot`（200ms memoryTTL，130ms 命中后续 130ms 仍命中，证明 sliding window）；`TestSingleFlightDedupesConcurrentIdenticalRequests`（5 个 goroutine 同 key 并发，只跑 1 次上游，`inflight_hits >= 4`）。
- **缓存策略按路径分桶 + per-path 统计 + 跨 caller 共享 embeddings/count_tokens**：基于 [`docs/cache-research.md`](docs/cache-research.md) 调研结论落地（命中率衰减是结构性问题，并非可调参数）。新增 [`internal/responsecache/path_policy.go`](internal/responsecache/path_policy.go) 定义 `pathPolicy` 与 `pathPolicyFor()`：`/v1/embeddings` / `/v1/messages/count_tokens` 是确定性函数（同一输入 → 同一输出），跨 API Key 共享是安全的，本版本把 owner 从这两条路径的 cache key 中移除，让所有 caller 共享同一份缓存项；同时把 embeddings 的 memory TTL 拉到 24 h、disk 拉到 7 d，count_tokens 拉到 2 h / 24 h（embedding 向量与 token 计数都不会随时间漂移）。LLM completions（chat / responses / messages / generateContent）继续保留 per-caller 隔离，因为每次响应是 sampling 结果，跨租户复用会泄漏一方的回答。`internal/responsecache/cache.go` 配套引入 `requestKeyWithPolicy` / `setWithPolicy` / `getWithPolicy` / `putMemoryWithPolicy`，旧 `Set` / `Get` / `RequestKey` 保留向后兼容（自动按路径解析 policy）。新增 per-path 计数桶 `pathStat`：`recordHit / recordMiss / recordStore / recordUncacheable` 都接收 path 形参，分摊到 `pathStats[path]`，admin overview 通过 `cache.paths` 字段把每条路径的 lookups / hits / misses / stores / memory_hits / disk_hits / cacheable_hit_rate / shared 一并暴露给 [`/admin/metrics/overview`](internal/httpapi/admin/metrics/handler.go)。两个回归用例固化语义：`TestEmbeddingsCacheSharesAcrossCallers` 确认 alice / bob 两个 caller 发同一个 embeddings body，第二次必须命中第一次的缓存；`TestChatCompletionsCacheStaysPerCaller` 确认 chat 路径仍是 per-caller 边界，相同 body 不同 caller 各自走上游一次。最高 ROI 改动：embeddings 跨 caller 共享意味着同一段文本不再被 N 个 key 各算一次，N 个 key 时理论收益 ~(N-1)/N。
- **CDATA 管道变体兼容（Claude Code v2.1.128 子代理名乱码修复）**：DeepSeek 模型在产出 DSML 工具调用时，会把外层 `<|DSML|...|>` 的管道惯例渗入 CDATA opener，产出 `<![CDATA|VALUE]]>` / `<![CDATA|VALUE|]]>` / `<![CDATA｜VALUE]]>`（全角竖线）等近似 CDATA。原解析器严格要求 `[`，导致变体未被剥离，`subagent_type` 等字符串参数把 wrapper 字面值带回 Claude Code，UI 显示 `<![CDATA|general-purpose]]>` 而非 `general-purpose`。本次修复在 [`internal/toolcall/toolcalls_markup.go`](internal/toolcall/toolcalls_markup.go) 与 [`internal/toolcall/toolcalls_parse_markup.go`](internal/toolcall/toolcalls_parse_markup.go) 加 `cdataPipeVariantPattern` / `cdataPipeOpenerByteLen` / `cdataOpenerByteLenAt`，扩展 `extractStandaloneCDATA` 与 `skipXMLIgnoredSection`，同步在 [`internal/js/helpers/stream-tool-sieve/parse_payload.js`](internal/js/helpers/stream-tool-sieve/parse_payload.js) 的 `CDATA_PIPE_VARIANT_PATTERN` 与 `extractStandaloneCDATA`。Go + Node 双端均加 3 个回归用例（ASCII 单管道 / ASCII 双管道 / 全角管道变体）。
- **Thinking-Injection 提示词扩写工具链纪律 + 工具链模式 + MCP 调用规范**：[`internal/promptcompat/thinking_injection.go`](internal/promptcompat/thinking_injection.go) 的 `DefaultThinkingInjectionPrompt` 在原有"最大化推理"基础上追加三段:① 6 条工具链纪律（CALL / FORMAT / PARALLEL vs SEQUENTIAL / AFTER A RESULT / STOP / FAILURE MODES TO AVOID）;② 5 个常见工具链模式 A-E（READ-BEFORE-EDIT / SEARCH→NARROW→INSPECT / BASH 命令与结果诊断 / 并行多源研究 / 条件型串行追加）每个模式给出完整 DSML invoke 范例;③ MCP 调用规范（`<server>.<tool>` 点号命名空间、参数名必须按 input_schema、不能臆造未声明的 server 名、可与常规工具在同一 `<|DSML|tool_calls>` 块并行）;④ 终止判据（用户问题已答完 / 同子问题连 3 次无进展 / 同参数同工具失败 2 次禁止第三次 / 输出可预测则跳过）。其中 FORMAT 段显式警告 `<![CDATA|VALUE|]]>` 是无效写法（外层 wrapper 用 `|` 但 CDATA 必须用 `[`），从源头降低 CDATA 变体出现频率，与解析器兼容形成"解析容错 + 提示纠正"双层防御。一些客户端反馈"模型调用不来工具链"主要是缺少 READ-BEFORE-EDIT 这种约束顺序的明示，以及 MCP `<server>.<tool>` 点号格式的范例;本版本一并补齐。
- **CNB CI 增加 PR Docker 构建检查**：合并请求 #12 的 [`.cnb.yml`](.cnb.yml) 改动经评估只有 `pull_request:` 段落是非冲突有效贡献（其余 `handler.go` / `deps.go` 改动 main 已是超集），本版本 cherry-pick 该段并以普通推送方式落地，PR 在 CNB 上以"已采纳"语义关闭，不走 merge 路径，避免无谓的合并冲突解析与历史污染。
- **Claude Code v2.x 错误码补全**：[`internal/httpapi/claude/handler_errors.go`](internal/httpapi/claude/handler_errors.go) 的 `claudeErrorCode()` 新增 `case 529: code = "overloaded_error"`。Anthropic 用 HTTP 529 表示上游 overloaded，Claude Code 客户端的官方 retry / back-off 路径 keys on `code: "overloaded_error"`；之前没显式映射会落到 `invalid_request`，导致客户端不重试。
- **新增 Claude Code v2.x 专题调研**：[`docs/client-compat/claude-code.md`](docs/client-compat/claude-code.md)（643 行 / ~4000 字）覆盖请求体 `system` 数组形态、`betas` 完整列表（v2.1.68 → v2.1.69+ 版本差异）、`cache_control.scope: "turn"`、`mcp_servers` 双格式（`mcp-client-2025-04-04` vs `mcp-client-2025-11-20`）、流式事件序列、错误信封、count_tokens、Task 子代理、thinking 与 tool_use 顺序等 11 个维度，末尾 P0/P1/P2 清单已逐项标注 ds2api 实现状态。
- **客户端兼容总索引**：新建 [`docs/client-compat/README.md`](docs/client-compat/README.md)，集中索引 5 份客户端报告（Codex / OpenCode / OpenClaw / Cherry Studio / Claude Code），按"已实现 / P1 待实现 / P2 待实现"三栏汇总跨客户端的 ds2api 适配项。
- **顶层文档同步刷新**：
  - [`docs/README.md`](docs/README.md)：导航补 client-compat 与 claude-code.md 链接，反映 v1.0.5 ~ v1.0.12 所有新功能。
  - [`docs/storage-cache.md`](docs/storage-cache.md)：从 2 个 SQLite 文件扩写为 5 套独立 SQLite（accounts / chat_history / token_usage / safety_words / safety_ips），含每套 schema、迁移路径、性能与故障排查；TTL 默认值更新为 30 min / 24 h。
  - [`docs/security.md`](docs/security.md)：补 v1.0.5 路径豁免 + 越狱默认开 + v1.0.8 localStorage 迁移 + v1.0.9 file upload 内联 + v1.0.11 安全策略 SQLite 拆分 + 敏感字段保护提醒。
  - [`docs/configuration.md`](docs/configuration.md)：缓存默认值表更新；新增"存储路径"和"服务器开关"两小节；明确 `.env` CONFIG_JSON 单引号明文形态。
  - [`docs/prompt-compatibility.md`](docs/prompt-compatibility.md) / [`docs/toolcall-semantics.md`](docs/toolcall-semantics.md) / [`docs/deployment.md`](docs/deployment.md)：各加 v1.0.5 ~ v1.0.12 关键变更段，方便老用户对照升级。



### 子段 v1.0.12

### 性能 / 运维

- **响应缓存 TTL 调长**：`memory_ttl_seconds` 5 min → 30 min（6×），`disk_ttl_seconds` 4 h → 24 h（6×）。生产探测显示之前 `memory_items=23 / stores=41` 有 18 个被 5 分钟 TTL 淘汰；延长后短时间内重发的相同请求命中率会显著提高。LLM agent 工作流本身因每轮 prompt 不同导致命中率天然受限（10–15% 是物理上限），这次优化目标是把"5 分钟内重试"场景的命中率从 ~12% 提升到 30%+。
- 模板 `config.example.json` / `.env.example` 同步对齐新 TTL 值。

### 客户端兼容

基于 Sonnet 4.6 调研报告（`docs/client-compat/`）落地 4 个客户端的 P0 适配项：

- **Codex CLI（OpenAI 官方）`docs/client-compat/codex.md`**：
  - 路由层新增 `/v1/responses/compact`、`/v1/v1/responses/compact`、`/responses/compact` 三处 stub，统一返回 `501 Not Implemented` + OpenAI 风格 error envelope，避免 Codex 在 `context_management.compact_threshold` 场景下命中 404 误判为路由缺失。
  - `internal/promptcompat/responses_input_items.go` 新增 `case "compaction", "reasoning":` 分支，显式返回 nil（静默跳过 Codex 注入到 input 数组的服务端加密占位项），避免被误当作 user content 进入 prompt。
- **OpenCode（sst/opencode）`docs/client-compat/opencode.md`**：
  - 新增 `/api/messages` + `/api/messages/count_tokens` Anthropic 路由别名，覆盖 OpenCode 在自定义 baseURL 不带 `/v1` 后缀场景的请求。
- **OpenClaw（github.com/openclaw/openclaw）`docs/client-compat/claude-coding-clients.md`**：
  - 共享 `/api/messages` 别名（OpenClaw 通过 `ANTHROPIC_BASE_URL` 配置 ds2api 时同样命中此路径）。
- **Cherry Studio（CherryHQ/cherry-studio）`docs/client-compat/cherry-studio.md`**：
  - `/chat/completions` 无 `/v1` 前缀的别名已在历史版本注册，本版本保持兼容。
  - `stream_options.include_usage` 透传：Go json.Unmarshal 默认忽略未知字段，已天然兼容；本版本未做改动。

### 文档

- `docs/client-compat/` 目录新增 4 份调研简报（约 1.0 万字），覆盖 8 个维度：API 接口面 / 工具调用格式 / 流式事件 / HTTP 头 / 模型名 / 推理字段 / 上下文管理 / 已知不兼容点 + ds2api 适配清单。后续 P1/P2 适配项（part-id 一致性、anthropic-beta 白名单、assistant 消息 thinking 块 reorder、内嵌 base64 截断）已记录在各报告的 checklist。

### 子段 v1.0.11

### 架构

- **违禁字词与 IP 黑白名单从 `config.json` 拆分为独立 SQLite**（与账号库 / Token 统计库 / 历史记录库一致的"每类一个独立文件"布局）：
  - 新增 `internal/safetystore` 包（`WordsStore` + `IPsStore`）。
  - **`data/safety_words.sqlite`**：表 `banned_entries(kind, value)` 复用 `kind ∈ {content, regex, jailbreak}`，承载违禁字面量、违禁正则、越狱模式；元表 `words_meta` 记录一次性迁移标记。
  - **`data/safety_ips.sqlite`**：三张表 `blocked_ips`、`allowed_ips`（白名单预留）、`blocked_conversation_ids`；元表 `ips_meta` 记录迁移标记。
  - `StorageConfig` 新增 `safety_words_sqlite_path` 与 `safety_ips_sqlite_path`，环境变量 `DEEPSEEK_WEB_TO_API_SAFETY_WORDS_SQLITE_PATH` / `DEEPSEEK_WEB_TO_API_SAFETY_IPS_SQLITE_PATH` 可覆盖。
- **运行时数据流**：
  - `requestguard.Options` 新增 `SafetyWords` / `SafetyIPs` 字段；`policyCache.load` 调 `mergeSafetySources` 把 SQLite 列表 union 进 `config.SafetyConfig` 后再走原有 `buildPolicy`。`appendUnique` 去重，避免双源造成重复扫描。
  - `admin/settings.handler.updateSettings` 在写入 `c.Safety` 之后**镜像写入**到两个 SQLite store（`mirrorWarn` 失败仅日志告警，不阻塞 admin 请求）。
  - `server.NewApp` 启动时调用 `SafetyWords.MigrateLegacyOnce` / `SafetyIPs.MigrateLegacyOnce`（幂等，靠 `_meta.migrated_from_config` 标记守卫）：把当前 `config.SafetyConfig` 中已有的列表数据一次性迁入 SQLite。
- **生产部署的 5 个独立 SQLite 文件布局**：
  | 文件 | 内容 |
  |---|---|
  | `data/accounts.sqlite` | 账号 + token |
  | `data/token_usage.sqlite` | Token 用量 rollup |
  | `data/chat_history.sqlite` | 历史记录 + detail blob |
  | `data/safety_words.sqlite` | 违禁字面量 / 正则 / 越狱模式 |
  | `data/safety_ips.sqlite` | 黑名单 IP / CIDR / 会话 ID + 白名单预留 |

### 运维

- 服务器 `.env` 中 `DEEPSEEK_WEB_TO_API_CONFIG_JSON` **从 `base64:` 编码切换为单引号包裹的明文紧凑 JSON**（dotenv 单引号语义保证字面量），同时保留 `DEEPSEEK_WEB_TO_API_ENV_WRITEBACK=true` 与 `DEEPSEEK_WEB_TO_API_CONFIG_PATH=data/config.json`，`data/config.json` 作为镜像保持人类可读。
- 仓库模板 [.env.example](.env.example) 与 [config.example.json](config.example.json) 同步对齐：
  - `server` 块加 `remote_file_upload_enabled: false`（v1.0.9 新增，生产默认禁用）。
  - `storage` 块加 `token_usage_sqlite_path`（v1.0.5）+ 本版本两个新路径 `safety_words_sqlite_path` / `safety_ips_sqlite_path`，并新增 `DEEPSEEK_WEB_TO_API_TOKEN_USAGE_SQLITE_PATH` / `DEEPSEEK_WEB_TO_API_SAFETY_WORDS_SQLITE_PATH` / `DEEPSEEK_WEB_TO_API_SAFETY_IPS_SQLITE_PATH` 三个 env override 注释 + `DEEPSEEK_WEB_TO_API_REMOTE_FILE_UPLOAD_ENABLED` 注释。
  - 注释加上每个 SQLite 文件"独立备份/轮转"说明，与生产 5-store 布局保持一致。

### 子段 v1.0.10

### 修复

- **配置中心恢复（`POST /admin/config/import` merge 模式）补齐 `storage` 与 `server` 块**：
  - `storage` 整段恢复（含 `data_dir`、`chat_history_path`、`chat_history_sqlite_path`、`accounts_sqlite_path`、`raw_stream_sample_root`、`token_usage_sqlite_path`），新增字段（v1.0.5+ 的 `token_usage_sqlite_path` 等）能在导出 → 导入往返中存活。
  - `server` 选择性字段恢复：`log_level` / `http_total_timeout_seconds` / `auto_build_webui` / `static_admin_dir` / `remote_file_upload_enabled` 提供则覆盖；**`port` 与 `bind_addr` 始终不动**，避免一台机器导出的部署敏感字段污染另一台。

### 运维

- 服务器 `.env` 中 `DEEPSEEK_WEB_TO_API_CONFIG_JSON` 同步加入 `server.remote_file_upload_enabled: false` 与 `storage.token_usage_sqlite_path: data/token_usage.sqlite`，与 v1.0.5 ~ v1.0.9 引入的新配置项保持一致。

### 子段 v1.0.9

### 修复

- **失败率高（生产 ~50% 成功率）的主因：DeepSeek upload_file 速率限制**。生产 SQLite 抽样显示近 2 小时 4116 次 `upload_file failed` + `biz_msg="rate limit reached"`，每个长对话请求都被 `current_input_file` 触发上传 `DEEPSEEK_WEB_TO_API_HISTORY.txt` 拖垮。
- **将 history.txt 与内联文件改为内联式注入**：
  - `internal/httpapi/openai/history/current_input_file.go` `ApplyCurrentInputFile`：当远端上传未启用时，把整段 transcript 直接拼到 user 消息内容里（追加 `currentInputFileInlinePrompt()` 引导模型继续），不再调 `DS.UploadFile`，HistoryText 仍持久化以供管理台查看。
  - `internal/httpapi/openai/files/file_inline_upload.go` `tryUploadBlock`：内联文件块当远端上传未启用时由 `inlineFileTextReplacement` 转为 `{type: "text", text: ...}`——文本类型 mime（`text/*`、`application/{json,xml,yaml,javascript,sql,...}`、`+json`、`+xml`）以及通过空字节嗅探判定为文本的 payload 直接展开；二进制 / 图片类型替换为 `[binary attachment "<filename>" omitted; N bytes (mime). Upstream file upload is disabled to avoid rate limiting.]` 占位。
- **新增总开关 `RemoteFileUploadEnabled`**（`shared.ConfigReader` + `*config.Store`）：
  - **生产默认 `false`**（直接关闭上传，对症修复失败率）。
  - 环境变量 `DEEPSEEK_WEB_TO_API_REMOTE_FILE_UPLOAD_ENABLED=true` 或配置 `server.remote_file_upload_enabled` 可重新启用，给运营保留逃生通道。
  - 测试 mock stub 默认返回 `true`，保留对 upload_file 路径的回归覆盖。

### 调试现象

- **"对话响应成功但无内容输出"56 条（占 success ~1.4%）**：detail blob 显示 `content / reasoning_content` 全空、`finish_reason=end_turn`、`status_code=200`，多发生在 `deepseek-v4-pro-search` 模型上。本版本未直接修复该症状（量级远小于上述上传速率限制问题），将在下一版本针对性调查 toolstream sieve 在流末未 flush hold buffer 的可能性。

### 子段 v1.0.8

### 修复

- **WebUI 刷新触发 `{"detail":"authentication required"}`（cnb.cool/Neko_Kernel/DeepSeek_Web_To_API#9）**：管理台 JWT 从 `sessionStorage` 改存 `localStorage`，避免硬刷新（Ctrl+Shift+R / Firefox / 隐私模式 / 部分浏览器恢复标签页行为）清空 token 导致 SPA 在认证状态丢失下夹带空 Authorization 发出 admin 请求，被 `RequireAdmin` 拒绝并返回 `{"detail":"authentication required"}` JSON。
- 提供从 `sessionStorage` 到 `localStorage` 的**自动迁移**：旧版浏览器会话首次刷新时仍能拿到 token 并迁移过去，旧 sessionStorage 条目同步清理；登出 / token 过期路径双轨清空两种存储。

### 子段 v1.0.7

### 修复

- **工具调用偶发断流（Meow-Calculations/DeepSeek_Web_To_API#4）**：`chat_stream_runtime.onParsed` 在 `evt.ToolCalls` 完整封闭并 `sendDelta` 后，立即返回 `Stop: true, StopReason: HandlerRequested`，触发 `finalize()` 走正常 `finish_reason="tool_calls"` + `[DONE]` 路径。修复前若上游 DeepSeek 在工具块之后未发 `[DONE]`（偶发）+ 客户端 ctx 取消同时发生，会走 `engine.contextDone()` 早退并跳过 finalize，客户端读到工具块但拿不到 finish 帧。
- **DeepSeek 特殊 token 渗漏（Meow-Calculations/DeepSeek_Web_To_API#7）**：`internal/httpapi/openai/shared/leaked_output_sanitize.go` 三处增强：
  - `leakedToolResultStartMarkerPattern` / `leakedToolResultEndMarkerPattern` / `leakedMetaMarkerPattern` 的尾部斜杠从 `/?` 扩展为 `[/／]?`，兼容上游偶发的全角斜杠 `／`（U+FF0F），覆盖 `<｜Tool／>` 这类形态。
  - 新增 `leakedDSMLMarkupFragmentPattern`（`(?im)` 多行模式）：清理 sieve 失败时残留的 DSML 片段，包含未闭合的 `<|tool_calls`、`<|DSML|invoke ...`、`</|DSML|parameter>` 等已知 token 关键字开头的孤立片段（行尾或 `>` 收尾）。
  - 新增 `leakedTrailingPipeTagPattern`：处理 `<|end_of_tool_result|tool_use_error: ...` 这类两个 `|` 之间夹文本无 `>` 收尾的形态。
- 上述 sanitize 在 `CleanVisibleOutput` 中被 OpenAI Chat / Responses / Claude / Gemini 四条产物路径统一调用，覆盖 Claude Code 等下游客户端见到的所有泄漏场景。

### 子段 v1.0.6

### 修复

- **Pro 模型 120s 超时**：`cmd/DeepSeek_Web_To_API/main.go` 把 `http.Server.{ReadTimeout,WriteTimeout}` 从硬编码 120s 改为读取 `Store.HTTPTotalTimeout()`（默认 7200s，可由 `DEEPSEEK_WEB_TO_API_HTTP_TOTAL_TIMEOUT_SECONDS` 或 `server.http_total_timeout_seconds` 调节）。Pro 模型流式补全可在 PoW + 推理 + 续写期间长时间持有连接，原 120s 写超时会被强制切断 SSE。
- **历史记录显示 IP / 会话 ID**：
  - 后端：`chathistory.SummaryEntry` 与 SQLite `chat_history` 表新增 `request_ip` / `conversation_id` 列，启动时通过 `ensureChatHistoryColumnLocked` 迁移老库（`ALTER TABLE ADD COLUMN IF NOT EXISTS`，幂等）。`summaryFromEntry` 拷贝两字段；列表 / 详情 SELECT、UPSERT 一并更新。
  - 前端：`ChatHistoryContainer.jsx` 详情卡片网格新增 `metaRequestIP` / `metaConversationID` 两块；列表项每条加一行 monospace 小字显示 `IP <...>` 与 `对话 ID <...>`，会话 ID 截断 160px 并 `title` 悬浮显示完整值。中英双语翻译键已补全。

### 子段 v1.0.5

### 修复

- **配置导入恢复**：`POST /admin/config/import` 的 merge 模式补齐 `proxies` 合并（按稳定 ID 去重追加）以及 `safety` / `cache` / `compat` / `auto_delete` / `history_split` / `current_input_file` / `thinking_injection` 七大块的整段覆盖（基于 payload 键存在性判断），导出/导入对称恢复。响应增加 `imported_proxies`。
- **账号批量导入计数**：`POST /admin/import` 不再静默返回 `imported_accounts: 0`，新增 `submitted_accounts` / `skipped_accounts` / `duplicate_accounts` / `invalid_accounts` 字段以及 `message` 中文提示（"未导入任何账号：N 个已存在"等），前端 `BatchImport.jsx` 同步显示原因。
- **违禁屏蔽范围**：
  - `collectText` 现在递归扫描所有 map 字段（不再因顶层非白名单键漏扫 `tool_result` / `functionResponse` 等容器字段）。
  - 中间件对 `/admin`、`/webui`、`/healthz`、`/readyz`、`/static/`、`/assets/` 路径**跳过内容扫描**（IP/会话封锁仍生效），避免管理员配置含违禁词时被自身策略锁死。
  - `safety.enabled = true` 而 `jailbreak.enabled` 未设时，越狱检测**默认开启**。
- **MCP 调用**：`mcp_servers[]` 字段不再被静默丢弃。`normalizeClaudeRequest` 把每个 MCP 服务器的 `tool_configuration.allowed_tools` 与 `tools[]` 展开为虚拟工具描述（命名形式 `<server>.<tool>`），追加到请求 `tools[]` 让模型在系统提示中可见这些工具，进而以标准 `tool_use` 块输出，由客户端 SDK 自行调度到对应 MCP 服务器。

### 架构

- **Token 统计独立 SQLite**：新增 `data/token_usage.sqlite`（路径可由 `DEEPSEEK_WEB_TO_API_TOKEN_USAGE_SQLITE_PATH` 或 `storage.token_usage_sqlite_path` 覆盖），表 `token_rollup` 持有按模型分组的累计用量。SQLite chathistory 启动时一次性把旧 `chat_history_meta` 中的 `pruned_token_total_*` 数据迁移到新库（幂等，靠 `token_meta` 中的 `migrated_from_chat_history` 标记守卫），之后剪枝触发的 token rollup 同步双写到独立库。读取路径优先独立库，旧 meta 作为 fallback，确保聊天历史被裁剪/清空也不丢失累计数据。

## 2026-05-05

### 修复

- 修复 `cnb/main` 合并后 `internal/chathistory.StartParams` 与 `internal/httpapi/admin/shared.ConfigStore` 接口未同步导致的编译失败。
- `chathistory.Entry` 与 `StartParams` 增加 `RequestIP` 与 `ConversationID` 字段，并在内存版与 SQLite 版的 `Start` 中持久化（随 `detail_blob` 序列化落库），保留请求来源 IP 与会话 ID 用于审计。
- `adminshared.ConfigStore` 接口补齐 `CompatWideInputStrictOutput` / `ResponsesStoreTTLSeconds` / `EmbeddingsProvider` 与 6 个 `ResponseCache*` 访问器；新增 `ResponseCacheRuntimeProvider` 接口（含 `Stats` + `ApplyOptions`）。
- `admin/handler.go` 将 `Handler.ResponseCache` 升级到 `ResponseCacheRuntimeProvider` 并把实例注入 `settingsHandler`，使运行时缓存设置变更可即时生效，UI 也能展示缓存运行态。

## 2026-05-02

### 安全

- 为 SQLite 聊天历史 gzip 详情增加解压后大小上限，避免异常压缩数据导致内存放大。
- 为 DeepSeek 非流式 JSON 响应读取增加解压后 16MB 上限，避免上游异常响应占用过量内存。
- 为 Claude/Gemini 经 OpenAI 兼容通道代理的请求体增加 `http.MaxBytesReader` 限制，超限返回 413。
- 修复 Admin metrics 中 `uint -> int64` 统计值转换的溢出风险。

### 稳定性

- 总览成功率统计排除用户侧 `401 / 403 / 502 / 504 / 524`，这些状态单独计数，不再拖低服务成功率。
- 总览缓存指标新增可缓存未命中率，并默认展示可缓存命中/未命中口径，避免不可缓存上游错误污染缓存判断。
- 为无效 direct token 增加短期负缓存，避免未配置或失效的客户端 token 反复穿透上游并持续拉低成功率。
- 规范化响应缓存请求体 key，消除 JSON 字段顺序、空白和无效顶层元数据导致的缓存碎片，提升重复请求命中概率。
- 响应缓存新增总命中率、可缓存命中率、不可缓存未命中原因和写入次数统计，避免客户端取消、上游错误等不可缓存请求污染缓存判断。
- 响应缓存 key 递归忽略 Claude / Claude Code 的 `cache_control`、`cache_reference`、`context_management`、`cache_edits` 和 `betas` 等传输字段，同时保留真实 JSON `null` 语义。

### 文档

- 刷新安全审计报告、源码全景分析、Admin WebUI 文档和 SQLite 迁移分析，统一当前 SQLite + gzip 详情存储事实。
- 记录本轮 `gosec`、`govulncheck`、`npm audit`、密钥扫描、SQL 注入扫描和内存放大扫描结果。
