#!/usr/bin/env bash
set -euo pipefail

POSTGRES_PORT="${DB_PORT:-5433}"
REDIS_PORT="${REDIS_PORT:-6380}"
MAX_RETRIES=30
RETRY_DELAY=1

echo "Waiting for PostgreSQL on port ${POSTGRES_PORT}..."
retries=0
until docker exec csd-postgres pg_isready -U postgres -d stampededb >/dev/null 2>&1 || nc -z localhost "${POSTGRES_PORT}" >/dev/null 2>&1; do
    retries=$((retries + 1))
    if [ "$retries" -ge "$MAX_RETRIES" ]; then
        echo "Error: Timed out waiting for PostgreSQL."
        exit 1
    fi
    sleep "$RETRY_DELAY"
done
echo "PostgreSQL is ready."

echo "Waiting for Redis on port ${REDIS_PORT}..."
retries=0
until docker exec csd-redis redis-cli ping >/dev/null 2>&1 || nc -z localhost "${REDIS_PORT}" >/dev/null 2>&1; do
    retries=$((retries + 1))
    if [ "$retries" -ge "$MAX_RETRIES" ]; then
        echo "Error: Timed out waiting for Redis."
        exit 1
    fi
    sleep "$RETRY_DELAY"
done
echo "Redis is ready."
