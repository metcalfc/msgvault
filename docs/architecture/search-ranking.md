---
title: Search Ranking
description: How full-text weights and vector distance order matching messages.
---

`msgvault search`, the Web UI, the TUI, the HTTP API, and the MCP server
use the archive's full-text index or its optional vector index. The selected
search mode determines which scoring model orders the results.

## Full-Text Ranking

SQLite uses FTS5 `bm25()` over the `messages_fts` virtual table. msgvault passes
column weights so subject and sender hits outrank body and recipient hits:

```sql
bm25(messages_fts, 1.0, 10.0, 1.0, 4.0, 1.0, 1.0)
```

The important weights are:

| Field | Weight |
|---|---|
| Subject | 10 |
| From address | 4 |
| Body, To, Cc | 1 |

The first `1.0` is the slot for the unindexed `message_id` column. Lower BM25
scores are more relevant.

BM25 also applies document length normalization. A query term in a short body
can score better than the same term in a long quoted thread because the long
document is penalized.

Use `subject:` when subject recall matters more than broad recall:

```bash
msgvault search 'subject:"quarterly review"'
```

## Vector Ranking

The sqlite-vec index uses L2 distance: smaller distances indicate closer
vectors. The embedding model and its normalization determine how those
distances correspond to semantic similarity.

Hybrid search combines full-text and vector result ranks. Its order can differ
from either signal used alone. Use `--explain` with vector or hybrid search to
inspect the contributing scores.

## Practical Guidance

- Long quoted threads affect full-text scores through document length
  normalization.
- Use field operators such as `subject:` and `from:` when you remember a
  specific message field.
- Use `--explain` with vector or hybrid search to inspect per-signal scores.
