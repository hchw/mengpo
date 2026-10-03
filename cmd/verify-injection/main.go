// Command verify-injection is a runnable end-to-end check of Mengpo's memory
// injection path against a live service. It only uses the public HTTP API:
//
//	dev login -> tenant context -> session -> seed a labelled corpus
//	-> N sequential injections -> effectiveness checks -> feedback
//
// Effectivenes is measured against a labelled corpus: every fixture memory
// carries one distinctive Latin token, each query names a token, so the tool
// knows exactly which memories must be injected and which must not.
//
// Usage:
//
//	go run ./cmd/verify-injection
//	go run ./cmd/verify-injection -corpus 12 -rounds 20
//	go run ./cmd/verify-injection -base http://localhost:5173 -verbose
//	go run ./cmd/verify-injection -seed=false          # never touch the database
//
// Seeding writes the corpus into the tenant schema with MEMORY_DATABASE_URL; it
// is idempotent and only used to exercise injection without a real LLM.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	baseURL     = flag.String("base", envOr("MEMORY_API_URL", "http://localhost:8080"), "Mengpo API base URL")
	email       = flag.String("email", envOr("VERIFY_EMAIL", "dev@mengpo.local"), "dev account to sign in with")
	corpusSize  = flag.Int("corpus", 12, "number of labelled fixture memories to seed (1..len(corpus))")
	rounds      = flag.Int("rounds", 8, "number of sequential injections to run")
	tokenBudget = flag.Int("token-budget", 2048, "injection token budget per round")
	longBudget  = flag.Int("long-budget", 40, "token budget used for the truncation check")
	seed        = flag.Bool("seed", true, "seed the corpus when the tenant has none")
	dsn         = flag.String("dsn", envOr("MEMORY_DATABASE_URL", "postgres://mengpo:mengpo@127.0.0.1:55432/mengpo?sslmode=disable"), "database URL used only for seeding")
	verbose     = flag.Bool("verbose", false, "print raw responses")
	timeout     = flag.Duration("timeout", 30*time.Second, "per-request timeout")
)

var failures int

func main() {
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		step("结果")
		fail("%v", err)
	}
	fmt.Printf("\n汇总: 失败 %d 项\n", failures)
	if failures > 0 {
		os.Exit(1)
	}
}

// ---- labelled corpus ----

type fixture struct {
	Token string // distinctive keyword used as the query term
	Text  string // realistic memory content, always containing Token
}

// corpus holds memories across unrelated topics so precision (not injecting the
// wrong memory) can be measured, not just recall.
var corpus = []fixture{
	{"pgvector", "本地开发一律用 pgvector 存向量,不用 faiss。pgvector 跟 PostgreSQL 同库同事务,备份、迁移、权限都复用现有体系;faiss 需要额外的索引文件生命周期管理,和主库容易不一致。新项目默认建 vector(1536) 列并加 hnsw 索引。"},
	{"dockercompose", "本地联调统一用 dockercompose 起依赖:postgres、redis、nats、console 一次拉起,端口固定映射到 55xxx 段避免和宿主机冲突。改完 compose 文件要 up -d --force-recreate,并且重建 API 容器后必须重启 console,否则 nginx 会继续用旧 IP,表现为 502。"},
	{"redis", "redis 只做缓存和限流,不做唯一数据源。缓存 key 一律带租户前缀,TTL 默认 5 分钟;凡是能回源的读都允许缓存未命中。写路径先写库再删缓存,禁止先删缓存再写库。"},
	{"nats", "nats 用来做 outbox 的唤醒信号,不是事实来源。作业真身永远落在 PostgreSQL 的 outbox 表里,消费者按租约 lease 抢占;即使 nats 丢消息,轮询也能把作业捞回来,所以不允许把业务状态放在消息体里。"},
	{"tailwindcss", "前端样式统一用 tailwindcss v4 的 @theme 令牌,禁止散落的行内颜色值。深色主题的主色取自记忆树的墨青色,组件类集中放在 styles.css 的 @layer components 里复用,业务页面只组合类名。"},
	{"goproxy", "Go 依赖统一走 goproxy 代理(GOPROXY=https://goproxy.cn,direct),CI 里也要设置,否则构建会卡在拉取。模块路径固定 github.com/hchw/mengpo,内部包按 internal 分层,adapter 不反向依赖 application。"},
	{"hnsw", "PostgreSQL 慢查询优先看执行计划:租户表全部按 schema 隔离,查询必须带 user_id 前缀索引。向量列单独建 hnsw 索引,全文检索用 simple 配置的 GIN 索引,不要在同一列上同时指望两种索引。"},
	{"grpcstream", "服务间同步调用用 grpcstream,超时和重试都在客户端声明,服务端只做幂等。流式接口必须支持取消传播,上游断开后下游 worker 要立刻停止,不允许继续占用连接。"},
	{"k8s", "生产部署走 k8s,探针区分 liveness 和 readiness:migrations 没跑完时 readiness 必须为 false,避免流量打进还没升级完的租户 schema。"},
	{"llmprompt", "模型提示词一律版本化,llmprompt 里写死输出契约并要求只返回 JSON。候选记忆必须引用真实存在的事件 id,模型不允许发明证据;调用失败要有界重试后降级到规则基线,不能让分析阻塞主链路。"},
	{"audittrail", "所有治理动作都要写 audittrail:谁、什么时候、对哪条记忆、做了什么、理由是什么。审计记录只追加不修改,租户删除时随 schema 一起销毁。"},
	{"embeddingmodel", "embeddingmodel 固定 bge-small-zh-v1.5 的 Q8_0 量化版本(512 维,中文/多语),CLS pooling,查询侧加指令前缀、文档侧不加。模型文件不入库,由构建产物提供并校验 SHA-256。换模型必须走重新嵌入流程:worker 启动时把 embedding_status 置为 stale 并重建向量,禁止混用不同模型的向量。"},
}

func corpusSlice() []fixture {
	size := *corpusSize
	if size < 1 {
		size = 1
	}
	if size > len(corpus) {
		size = len(corpus)
	}
	return corpus[:size]
}

func fixtureID(index int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", index+1)
}

func fixtureIdemKey(index int) string { return fmt.Sprintf("verify-injection-%02d", index+1) }

// ---- run ----

func run(ctx context.Context) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	items := corpusSlice()

	step("0. 健康检查 %s", c.base)
	if err := c.health(ctx); err != nil {
		return fmt.Errorf("服务不可达: %w", err)
	}
	ok("服务可达")

	step("1. 开发登录 /api/v1/auth/sso/exchange")
	var exchange struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
			Tenants []struct {
				ID string `json:"id"`
			} `json:"tenants"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/api/v1/auth/sso/exchange", map[string]any{"assertion": *email}, &exchange); err != nil {
		return fmt.Errorf("登录失败: %w", err)
	}
	if exchange.Data.User.ID == "" || len(exchange.Data.Tenants) == 0 {
		return errors.New("登录响应缺少 user/tenant")
	}
	c.user = exchange.Data.User.ID
	c.tenantID = exchange.Data.Tenants[0].ID
	ok("user=%s", c.user)
	ok("tenant=%s", c.tenantID)
	if err := c.post(ctx, "/api/v1/auth/tenant-context", map[string]any{"tenant_id": c.tenantID}, nil); err != nil {
		return fmt.Errorf("绑定租户失败: %w", err)
	}
	ok("租户上下文已绑定")

	var session struct {
		Data struct {
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/api/v1/sessions", c.envelope(map[string]any{}, nil), &session); err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	ok("session=%s", session.Data.SessionID)

	step("2. 播种标注语料(%d 条,含长文本)", len(items))
	if *seed {
		if err := seedCorpus(ctx, *dsn, c.tenantID, c.user, items); err != nil {
			return fmt.Errorf("播种失败(可用 -seed=false 跳过): %w", err)
		}
		ok("语料已写入租户 schema(幂等)")
	} else {
		info("已跳过播种,使用租户现有记忆")
	}

	step("3. 连续注入 %d 轮(每轮一个标注查询)", *rounds)
	report := newReport()
	seen := map[string][]string{}
	for round := 0; round < *rounds; round++ {
		fixture := items[round%len(items)]
		expected := map[string]bool{fixtureID(round % len(items)): true}

		started := time.Now()
		projection, err := c.project(ctx, fixture.Token, *tokenBudget, nil)
		latency := time.Since(started)
		if err != nil {
			fail("第 %d 轮 query=%q 调用失败: %v", round+1, fixture.Token, err)
			continue
		}
		report.observe(round, fixture, projection, expected, latency, *tokenBudget)
		seen[fixture.Token] = append(seen[fixture.Token], projection.idsKey())
	}

	report.print()

	step("4. 长文本截断(预算 %d token)", *longBudget)
	longest := items[0]
	for _, item := range items {
		if len(item.Text) > len(longest.Text) {
			longest = item
		}
	}
	tight, err := c.project(ctx, longest.Token, *longBudget, nil)
	if err != nil {
		return fmt.Errorf("截断检查调用失败: %w", err)
	}
	if tight.Data.Usage.TokensInjected > *longBudget {
		fail("超出预算: 注入 %d > %d", tight.Data.Usage.TokensInjected, *longBudget)
	} else if len(tight.Data.Items) == 0 {
		fail("小预算下没有注入任何内容")
	} else {
		ok("注入 %d token(≤ %d),截断=%v", tight.Data.Usage.TokensInjected, *longBudget, tight.Data.Items[0].Truncated)
	}
	full, err := c.project(ctx, longest.Token, 8192, nil)
	if err != nil {
		return fmt.Errorf("全长检查调用失败: %w", err)
	}
	if len(full.Data.Items) == 0 {
		fail("大预算下没有注入内容")
	} else if len([]rune(full.Data.Items[0].Candidate.Node.ContentText)) < len([]rune(longest.Text))*9/10 {
		fail("大预算下注入内容不完整")
	} else {
		ok("大预算下内容完整(%d token,截断=%v)", full.Data.Usage.TokensInjected, full.Data.Items[0].Truncated)
	}

	step("5. 注入反馈 POST /api/v1/feedback")
	memoryID := fixtureID(0)
	var feedback struct {
		Data struct {
			Stored bool `json:"stored"`
		} `json:"data"`
	}
	err = c.post(ctx, "/api/v1/feedback", c.envelope(map[string]any{
		"memory_id": memoryID,
		"type":      "helpful",
		"reason":    "verify-injection",
	}, nil), &feedback)
	if err != nil {
		fail("反馈失败: %v", err)
	} else if feedback.Data.Stored {
		ok("反馈已记录 memory_id=%s", memoryID)
	} else {
		fail("反馈未确认")
	}
	if *sessionFlags.enabled {
		if err := runSessionFlow(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// ---- effectiveness report ----

type report struct {
	rounds        int
	injected      int
	precision     []float64
	recall        []float64
	budgetBusts   int
	duplicates    int
	unordered     int
	emptyRounds   int
	tokenMismatch int
	latencies     []time.Duration
	cacheHits     int
	unstable      int
	modes         map[string]int
	firstKey      map[string]string
}

func newReport() *report { return &report{modes: map[string]int{}, firstKey: map[string]string{}} }

func (r *report) observe(round int, fixture fixture, p projectionResponse, expected map[string]bool, latency time.Duration, budget int) {
	query := fixture.Token
	r.rounds++
	r.latencies = append(r.latencies, latency)
	r.modes[p.Data.Metadata.Mode]++
	if p.Data.Metadata.CacheHit {
		r.cacheHits++
	}

	injectedIDs := p.injected()
	r.injected += len(injectedIDs)

	// determinism: the same query must keep injecting the same memories.
	key := p.idsKey()
	if first, ok := r.firstKey[query]; ok {
		if first != key {
			r.unstable++
			fail("第 %d 轮 query=%q 结果不稳定: %s ≠ %s", round+1, query, key, first)
		}
	} else {
		r.firstKey[query] = key
	}

	// Relevance is judged on the injected content: every injected memory must
	// mention the query term, and the labelled corpus memory must be present.
	term := strings.ToLower(fixture.Token)
	relevant := 0
	foundExpected := false
	seenIDs := map[string]bool{}
	duplicate := false
	for index, item := range p.Data.Items {
		id := item.Candidate.Node.ID
		if seenIDs[id] {
			duplicate = true
		}
		seenIDs[id] = true
		text := strings.ToLower(item.Candidate.Node.ContentText)
		if strings.Contains(text, term) {
			relevant++
		} else {
			fail("第 %d 轮 query=%q 注入了无关记忆 %s:%s", round+1, query, id, truncate(item.Candidate.Node.ContentText, 40))
		}
		if expected[id] {
			foundExpected = true
		}
		_ = index
	}
	precision := 1.0
	if len(injectedIDs) > 0 {
		precision = float64(relevant) / float64(len(injectedIDs))
	}
	recall := 0.0
	if foundExpected {
		recall = 1.0
	} else {
		fail("第 %d 轮 query=%q 未召回标注记忆 %s", round+1, query, fixtureID(round%len(corpusSlice())))
	}
	r.precision = append(r.precision, precision)
	r.recall = append(r.recall, recall)
	if duplicate {
		r.duplicates++
		fail("第 %d 轮 query=%q 注入结果有重复条目", round+1, query)
	}
	if len(injectedIDs) == 0 {
		r.emptyRounds++
		fail("第 %d 轮 query=%q 没有注入任何记忆", round+1, query)
	}

	// ordering: FinalScore must be non-increasing.
	previous := math.Inf(1)
	for _, item := range p.Data.Items {
		if item.Candidate.FinalScore > previous+1e-9 {
			r.unordered++
			fail("第 %d 轮 query=%q 注入顺序未按分数降序", round+1, query)
			break
		}
		previous = item.Candidate.FinalScore
	}

	// metering and budget.
	sum := 0
	for _, item := range p.Data.Items {
		sum += item.TokenCost
	}
	if sum != p.Data.Usage.TokensInjected {
		r.tokenMismatch++
		fail("第 %d 轮 token 记账不一致: 明细 %d ≠ usage %d", round+1, sum, p.Data.Usage.TokensInjected)
	}
	if p.Data.Usage.TokensInjected > budget {
		r.budgetBusts++
		fail("第 %d 轮超出预算: %d > %d", round+1, p.Data.Usage.TokensInjected, budget)
	}

	texts := p.texts()
	sample := ""
	if len(texts) > 0 {
		sample = texts[0]
	}
	if len(sample) > 28 {
		sample = sample[:28] + "…"
	}
	info("轮 %2d  query=%-16s 命中=%-2d 精确率=%.2f 召回率=%.2f token=%-4d 候选=%-2d 排序=%-2d 延迟=%s",
		round+1, query, len(injectedIDs), precision, recall, p.Data.Usage.TokensInjected,
		p.Data.Usage.CandidatesSeen, p.Data.Usage.CandidatesRanked, latency.Round(time.Millisecond))
	info("        · %s", sample)
}

func (r *report) print() {
	step("3.1 有效性汇总")
	if r.rounds == 0 {
		fail("没有成功完成任何一轮注入")
		return
	}
	mean := func(values []float64) float64 {
		if len(values) == 0 {
			return 0
		}
		sum := 0.0
		for _, value := range values {
			sum += value
		}
		return sum / float64(len(values))
	}
	tokens := fmt.Sprintf("%d 条记忆 / %d 轮注入", *corpusSize, r.rounds)
	ok("覆盖:%s", tokens)
	ok("平均精确率=%.2f 平均召回率=%.2f(无关记忆注入 %d 次)",
		mean(r.precision), mean(r.recall), r.rounds-countTrue(r.precision, 1))
	ok("预算超限=%d 重复条目=%d 乱序=%d 记账不一致=%d 空注入=%d 结果不稳定=%d",
		r.budgetBusts, r.duplicates, r.unordered, r.tokenMismatch, r.emptyRounds, r.unstable)
	ok("延迟 p50=%s p95=%s;缓存命中 %d/%d",
		percentile(r.latencies, 50), percentile(r.latencies, 95), r.cacheHits, r.rounds)
	modes := make([]string, 0, len(r.modes))
	for mode, count := range r.modes {
		modes = append(modes, fmt.Sprintf("%s×%d", mode, count))
	}
	sort.Strings(modes)
	ok("检索模式:%s", strings.Join(modes, " "))
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func countTrue(values []float64, target float64) int {
	count := 0
	for _, value := range values {
		if math.Abs(value-target) < 1e-9 {
			count++
		}
	}
	return count
}

func percentile(values []time.Duration, p int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(float64(p)/100*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index].Round(time.Millisecond)
}

// ---- seeding ----

func seedCorpus(ctx context.Context, dsn, tenantID, userID string, items []fixture) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	schema := "tenant_" + strings.ReplaceAll(tenantID, "-", "")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for index, item := range items {
		confidence := 0.6 + float64(index%4)*0.1
		_, err := tx.ExecContext(ctx, `
INSERT INTO `+schema+`.memory_nodes
  (id, idempotency_key, user_id, scope_type, scope_id, memory_type, status,
   visibility, confidence, content, content_text, default_retrieval, provenance)
VALUES ($1, $2, $3, 'user-global', $3, 'insight', 'active', 'private', $4,
        jsonb_build_object('text', $5::text), $5, true,
        jsonb_build_object('source', 'verify-injection'))
ON CONFLICT (id) DO UPDATE
SET content = EXCLUDED.content,
    content_text = EXCLUDED.content_text,
    confidence = EXCLUDED.confidence,
    status = 'active',
    default_retrieval = true,
    deleted_at = NULL,
    updated_at = now()`,
			fixtureID(index), fixtureIdemKey(index), userID, confidence, item.Text)
		if err != nil {
			return fmt.Errorf("insert fixture %d: %w", index, err)
		}
	}
	return tx.Commit()
}

// ---- HTTP plumbing ----

type client struct {
	http         *http.Client
	base         string
	user         string
	tenantID     string
	scopeType    string
	scopeSession string
}

func newClient() (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &client{http: &http.Client{Jar: jar, Timeout: *timeout}, base: strings.TrimRight(*baseURL, "/")}, nil
}

func (c *client) health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *client) post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *client) put(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, http.MethodPut, path, body, out)
}

func (c *client) do(ctx context.Context, method, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if *verbose {
		info("POST %s -> %d: %s", path, response.StatusCode, string(raw))
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *client) scope() map[string]any {
	scopeType := c.scopeType
	if scopeType == "" {
		scopeType = "user-global"
	}
	scope := map[string]any{
		"tenant_id": c.tenantID,
		"user_id":   c.user,
		"type":      scopeType,
	}
	if scopeType == "session" {
		scope["session_id"] = c.scopeSession
	}
	return scope
}

// postInScope sends a request bound to a specific session scope.
func (c *client) postInScope(ctx context.Context, path, sessionID string, body any, out any) error {
	previousType, previousSession := c.scopeType, c.scopeSession
	c.scopeType, c.scopeSession = "session", sessionID
	defer func() { c.scopeType, c.scopeSession = previousType, previousSession }()
	return c.post(ctx, path, body, out)
}

func (c *client) envelope(payload any, hint *memoryHint) map[string]any {
	body := map[string]any{
		"version":         "v1",
		"request_id":      newUUID(),
		"idempotency_key": newUUID(),
		"principal":       map[string]any{"type": "user", "id": c.user},
		"scope":           c.scope(),
		"privacy":         map[string]any{"visibility": "private"},
		"payload":         payload,
	}
	if hint != nil {
		body["memory_hint"] = hint
	}
	return body
}

type memoryHint struct {
	Mode      string   `json:"mode,omitempty"`
	Topics    []string `json:"topics,omitempty"`
	MemoryIDs []string `json:"memory_ids,omitempty"`
}

type projectionResponse struct {
	Data struct {
		Items []struct {
			Candidate struct {
				Node struct {
					ID          string `json:"ID"`
					ContentText string `json:"ContentText"`
				} `json:"Node"`
				FinalScore     float64 `json:"FinalScore"`
				Included       bool    `json:"Included"`
				ExcludedReason string  `json:"ExcludedReason"`
			} `json:"Candidate"`
			Text           string `json:"Text"`
			TokenCost      int    `json:"TokenCost"`
			Truncated      bool   `json:"Truncated"`
			ExcludedReason string `json:"ExcludedReason"`
		} `json:"items"`
		Usage struct {
			CandidatesSeen     int `json:"CandidatesSeen"`
			CandidatesRanked   int `json:"CandidatesRanked"`
			CandidatesInjected int `json:"CandidatesInjected"`
			TokensInjected     int `json:"TokensInjected"`
		} `json:"usage"`
		Metadata struct {
			Mode     string `json:"mode"`
			Degraded bool   `json:"degraded"`
			CacheHit bool   `json:"cache_hit"`
		} `json:"metadata"`
	} `json:"data"`
}

func (p projectionResponse) injected() []string {
	ids := make([]string, 0, len(p.Data.Items))
	for _, item := range p.Data.Items {
		if item.Candidate.Included {
			ids = append(ids, item.Candidate.Node.ID)
		}
	}
	return ids
}

func (p projectionResponse) texts() []string {
	texts := make([]string, 0, len(p.Data.Items))
	for _, item := range p.Data.Items {
		text := item.Candidate.Node.ContentText
		if text == "" {
			text = item.Text
		}
		texts = append(texts, text)
	}
	return texts
}

// idsKey returns a stable key of the injected ids, used for determinism checks.
func (p projectionResponse) idsKey() string {
	ids := p.injected()
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func (c *client) project(ctx context.Context, query string, tokens int, hint *memoryHint) (projectionResponse, error) {
	body := c.envelope(map[string]any{"query": query, "memory_type": "user-global"}, hint)
	body["budget"] = map[string]any{"injection_tokens": tokens}
	var response projectionResponse
	err := c.post(ctx, "/api/v1/project", body, &response)
	return response, err
}

// ---- tiny helpers ----

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func newUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func step(format string, args ...any) { fmt.Printf("\n\033[1m"+format+"\033[0m\n", args...) }
func info(format string, args ...any) { fmt.Printf("       "+format+"\n", args...) }
func ok(format string, args ...any) {
	fmt.Printf("  \033[32m[OK]\033[0m   "+format+"\n", args...)
}
func fail(format string, args ...any) {
	failures++
	fmt.Printf("  \033[31m[FAIL]\033[0m "+format+"\n", args...)
}
