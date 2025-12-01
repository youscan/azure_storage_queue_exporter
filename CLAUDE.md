# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A Prometheus exporter for Azure Storage Queue metrics. It scrapes message count and message age from all queues across configured Azure Storage Accounts and exposes them as Prometheus metrics.

## Build and Development Commands

```bash
make deps      # Install dependencies and tidy go.mod
make build     # Build binary to build/azure_storage_queue_exporter
make test      # Run tests (go test -v ./...)
make clean     # Remove build artifacts

# Lint (uses golangci-lint)
golangci-lint run
```

## Configuration

Storage account credentials are provided via environment variables with prefix `STORAGE_ACCOUNT_`:
- Variable name: `STORAGE_ACCOUNT_<account_name>`
- Variable value: the storage account key

Supports `.env` file for local development (auto-loaded via godotenv).

CLI flags:
- `-collection.interval` (default: 5s)
- `-web.listen-address` (default: :9874)
- `-web.telemetry-path` (default: /metrics)
- `-log.level` {debug|info|warn|error|fatal} (default: warn)
- `-log.format` {text|json} (default: text)

## Architecture

Single-file Go application (`azure_storage_queue_exporter.go`):

- **Exporter struct**: Holds storage account credentials and Prometheus gauge vectors. The `Collect()` method runs concurrently across all storage accounts and queues.
- **Queue struct**: Represents a queue with methods `getMessageCount()` and `getMessageTimeInQueue()` that call Azure APIs.
- **Metrics exposed**:
  - `azure_queue_message_count` (labels: storage_account, queue_name)
  - `azure_queue_message_time_in_queue` (labels: storage_account, queue_name)
- **Health endpoints**: `/healthz` (liveness), `/readyz` (readiness - true after first successful collection)

## Docker

```bash
docker build -t azure_storage_queue_exporter .
```
