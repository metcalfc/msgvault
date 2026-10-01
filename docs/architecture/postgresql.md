---
title: PostgreSQL Support Removed
description: SQLite is the supported archive database.
---

This version supports SQLite archives only. PostgreSQL connection strings and
`pgvector` configuration are rejected instead of opening a database or silently
falling back to an empty SQLite archive.

Existing PostgreSQL archives need a build that supports them. This change does
not convert or delete their data, and there is no built-in migration command.

See [Storage](storage.md) for the SQLite archive, attachment files, DuckDB
analytics cache, and sqlite-vec search index.
