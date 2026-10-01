# Performance Benchmarks and SLOs

Run the in-process ranking benchmarks with:

```bash
make bench
# or
go test ./internal/application/recall/... -run '^$' -bench . -benchmem
```

## Measured ranking cost (this machine: Xeon E5-2686 v4, Go 1.x, 100 iterations)

| Benchmark | Path | ns/op | allocs/op |
| --- | --- | --- | --- |
| `BenchmarkRankStructuredFullText` | structured + full-text, 50 candidates | ~191,000 | 70 |
| `BenchmarkRankHybrid` | full-text + vector + relation + parent, 200 candidates | ~686,000 | 228 |
| `BenchmarkRankDegraded` | reranker-unavailable fallback, 100 candidates | ~115,000 | 11 |

These numbers cover **in-process ranking only**. The end-to-end SLOs below also
include the database round-trips, the pgvector index scan, and (when enabled)
the embedding and reranker providers.

## SLO targets and status

| Path | SLO p95 | Basis | Status |
| --- | --- | --- | --- |
| Structured / full-text recall | < 150 ms | ranking < 1 ms + indexed SQL | ✅ on track |
| Hybrid recall (vector on) | < 400 ms | ranking < 1 ms + pgvector + embed | ✅ on track |
| Degraded recall (no embedding/reranker) | < 150 ms | falls back to structured/full-text | ✅ on track |
| Memory LLM consolidation | < 20 s | asynchronous, outbox-retried | ✅ by design (not on the request path) |

## Method

- Ranking benchmarks use a deterministic candidate pool so runs are comparable.
- Degraded paths are exercised via fault injection (`RankWithFallback` with a
  failing reranker) rather than by disabling infrastructure.
- Provider latency (embedding model, LLM, reranker) is measured separately when
  those artifacts are deployed; the ranking cost is a stable lower bound.

## Capacity notes

- Hybrid ranking is ~3.6× the structured/full-text cost at 4× the candidate
  count, so candidate budgets — not CPU — are the effective latency lever.
- The degraded path is the cheapest (no provider calls) and is the safety net
  when embedding, vector index or reranker are unavailable.
- Re-run `make bench` after changing ranking weights, budgets or filters; record
  the new numbers in this file.