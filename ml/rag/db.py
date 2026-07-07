"""Postgres connection pool for retrieval (pgvector kNN + full-text search).

DATABASE_URL must point at the same database the Go API and dictum-import
write to — this module only reads.
"""
from __future__ import annotations

import os

from pgvector.psycopg import register_vector
from psycopg import Connection
from psycopg_pool import ConnectionPool

_pool: ConnectionPool | None = None


def _configure(conn: Connection) -> None:
    register_vector(conn)


def get_pool() -> ConnectionPool:
    global _pool
    if _pool is None:
        dsn = os.environ.get("DATABASE_URL", "postgresql://dictum:dictum@localhost:5432/dictum")
        _pool = ConnectionPool(dsn, open=True, configure=_configure)
    return _pool
