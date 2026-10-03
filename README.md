<p align="center">
  <img src="web/mengpotang.png" alt="Mengpo" width="660">
</p>

<h1 align="center">Mengpo · 孟婆记忆服务</h1>

<p align="center">
  <em>给智能体一碗「孟婆汤」，也给它一棵可追溯的记忆树。</em>
</p>

<p align="center">
  <a href="LICENSE">License: Apache-2.0</a> ·
  Go 1.24 · PostgreSQL + pgvector · NATS · React
</p>

---

## 项目背景

大模型应用普遍存在一个矛盾：**上下文窗口是有限的，而记忆应该是长期的、可治理的、可追溯的。**

大多数方案把「记忆」等同于「把历史消息塞回 Prompt」，结果是：

- 记忆无边界、无置信度、无来源，无法审计；
- 跨用户、跨会话、跨租户的数据容易混在一起，存在污染与合规风险；
- 检索只看相似度，无法解释「为什么想起了这件事」；
- 记忆一旦写入就难以更正、遗忘或迁移。

**Mengpo（孟婆）** 是一个独立的、面向 Multi-Agent 场景的**记忆服务**。它借用「孟婆汤」的意象：该忘的干净地忘，该记的带着证据记。Memory Service 不与任何具体应用耦合，任何 Agent、Workflow 或应用都可以通过 HTTP / 事件接口接入。

它把「记忆」当成一等公民：每条记忆都有领域模型、治理状态、证据链、置信度、适用范围、置信衰减与租户隔离；每次召回都可解释、可回放、可评估。

## 核心功能

### 🧠 有治理的记忆，而不是一堆向量
- **领域模型**：记忆节点区分 User Global Memory（用户在该租户内跨 Session）与 Session Memory 两个维度。
- **治理状态机**：候选 → 激活 → 失效 / 归档，变更全程审计。
- **证据与置信度**：每条记忆携带 evidence、confidence 与适用条件，支持置信衰减与冲突消解。
- **Scope 授权**：principal 对 tenant / user-global / session 三层 Scope 均需显式授权。

### 🔒 租户级物理隔离
- **Tenant-per-Schema**：每个租户拥有独立 PostgreSQL Schema，`search_path` 事务级路由，禁止跨租户查询。
- **全链路 tenant identity**：Repository、缓存 Key、任务队列、Embedding Job、LLM 分析批次、Redis 缓存与指标全部携带租户身份，并有跨租户污染测试兜底。
- **准入与隔离**：Agent Handshake（注册 / 凭证 / 能力协商 / 租户绑定）、Level 0/1/2 接入，未绑定事件进入 Quarantine。

### 🔎 可解释的混合召回
- **五路召回**：结构化 / 全文 / 向量（pgvector）+ 关系 + 时序。
- **独立 Reranker**：排序与召回解耦，Transfer Candidate 必须经独立治理与证据校验才能晋升。
- **Focus / Divergence 编排**：安全信号强制 Divergence；Focus hint 作为有界偏好，不覆盖安全触发。
- **三层预算**：候选、排序、注入 token 预算分档控制，去重与摘要替换，超限项带原因排除。
- **降级可用**：Embedding / Reranker / 检索任一环节失败都有明确降级路径，结构化检索始终可用。

### 🧪 从观察到记忆的流水线
- **Observation Gateway**：旁路事件先落 `observed_events` 原始表，不直接写长期记忆。
- **Memory LLM 分析**：事件分类、失败分析、记忆规整、冲突分析，结构化输出校验、模型版本管理与重试降级。
- **规则降级**：Provider 不可用时退回规则分析，且降级不创建记忆。
- **异步任务**：PostgreSQL outbox/job 为可靠性事实来源，NATS 仅作唤醒通知（可退回轮询）。

### 📊 评估、可观测与运维
- **评估集与指标**：dataset / golden snapshot / feedback metrics，支持回归对比。
- **可观测性**：结构化日志、trace、按租户标注的 Prometheus 指标。
- **安全审计与告警**：安全事件日志、导出/删除授权、故障演练与告警规则。
- **一键部署**：`docker compose` 拉起 Postgres+pgvector、Redis、NATS、API、Worker 与 Console。

### 🖥️ React 记忆控制台
Dashboard、Session Explorer、Candidate Review、Projection Debugger、Failure Analysis、Memory Evaluation、Members 与 Settings 页面，支持租户登录、切换与权限管理。

## 使用方法

### 方式一：一键本地环境（推荐）

需要 Docker 与 Docker Compose：

```bash
make dev          # 构建并启动 Postgres+pgvector、Redis、NATS、API、Worker、Console
make dev-logs     # 查看日志
make dev-down     # 停止并清空数据卷
```

启动后：

- Memory API：`http://localhost:8080`
- 健康检查：`GET /healthz`
- 指标：`GET /metrics`
- React Console：见 `deploy/dev/docker-compose.yml` 中 console 服务映射端口

### 方式二：本地运行服务

```bash
cp .env.example .env      # 按需修改配置
go build ./...
go run ./cmd/memory-server   # 启动 HTTP API
go run ./cmd/memory-worker   # 启动异步 Worker
```

关键配置（环境变量，详见 `.env.example`）：

| 变量 | 说明 |
| --- | --- |
| `HTTP_ADDR` | API 监听地址，默认 `:8080` |
| `DATABASE_URL` | PostgreSQL 连接串（必需） |
| `MQ_ADAPTER` / `MQ_URL` | 队列适配器（`nats-core` / `none`）与地址；`none` 时退回数据库轮询 |
| `EMBEDDING_ENABLED` / `EMBEDDING_ARTIFACT` | 本地 Embedding（默认 `models/bge-small-zh-v1.5-Q8_0.gguf`，中文/多语，512 维，CLS pooling） |
| `MEMORY_LLM_ENABLED` / `MEMORY_LLM_BASE_URL` / `MEMORY_LLM_MODEL` | Memory LLM 分析 Provider |
| `RERANKER_ENABLED` / `RERANKER_BASE_URL` / `RERANKER_MODEL` | 独立 Reranker |
| `MEMORY_REDIS_URL` | 租户隔离缓存（可选） |
| `AUTH_SESSION_TTL` / `AUTH_COOKIE_SECURE` | 会话与 Cookie 安全策略 |

> 默认所有外部 Provider 均关闭：未显式启用时，服务以规则/结构化路径降级运行。

### 运行测试

```bash
make test         # Go 单元测试
make test-db      # 使用一次性 pgvector 容器运行集成测试
make test-web     # 前端测试（vitest）
make bench        # 召回基准
make fmt          # 格式化 Go 与前端
```

需要外部依赖的集成测试通过环境变量开启（未设置则自动跳过）：

```bash
MEMORY_TEST_DATABASE_URL='postgres://test:test@127.0.0.1:55432/test?sslmode=disable' go test ./...
MEMORY_TEST_REDIS_URL='redis://127.0.0.1:56379/0' go test ./internal/adapters/redis/...
NATS_TEST_URL='nats://127.0.0.1:54222' go test ./internal/adapters/nats/...
```

### 前端控制台

```bash
cd web
npm install
npm run dev       # 本地开发
npm run build     # 生产构建
npm test          # 测试
```

### 接入 Agent

1. Agent 先通过 Handshake 完成注册、凭证绑定与能力协商；
2. 以 Level 0（无状态）/ Level 1（会话绑定）/ Level 2（深度集成）任意一档接入；
3. 通过统一 v1 Envelope 提交 Observation、获取 Projection、回传 Feedback 或触发 Consolidate；
4. 未绑定租户的事件不会写入任何租户 Schema，而是进入 Quarantine。

## 许可

本项目基于 [Apache License 2.0](LICENSE) 开源。
