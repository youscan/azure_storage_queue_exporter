# Azure Storage Queue Exporter

A Prometheus exporter for Azure Storage Queue metrics. Scrapes message count and message age from all queues across configured Azure Storage Accounts and exposes them as Prometheus metrics.

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `azure_queue_message_count` | Gauge | `storage_account`, `queue_name` | Number of messages in the queue |
| `azure_queue_message_time_in_queue` | Gauge | `storage_account`, `queue_name` | Age of the oldest message in seconds |

## Authentication

Two authentication methods are supported and can be mixed — some storage accounts can use keys while others use identity-based auth.

### Storage Account Keys

Set environment variables with the `STORAGE_ACCOUNT_` prefix:

```bash
export STORAGE_ACCOUNT_mystorageaccount=<base64-encoded-key>
```

The variable name suffix is the storage account name, and the value is the account key.

### Azure AD / Workload Identity (DefaultAzureCredential)

Set the environment variable with an empty value:

```bash
export STORAGE_ACCOUNT_mystorageaccount=
```

An empty value tells the exporter to use [DefaultAzureCredential](https://learn.microsoft.com/en-us/azure/developer/go/sdk/authentication/credential-chains), which automatically chains through:

1. Environment credentials (`AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TENANT_ID`)
2. Workload Identity (in AKS with workload identity configured)
3. Managed Identity
4. Azure CLI (`az login`)

### Required Azure RBAC Permissions

When using identity-based auth, the identity needs the following role:

| Role | Scope | Purpose |
|------|-------|---------|
| `Storage Queue Data Reader` | Storage Account or Subscription | Read queue metadata and peek messages |

This is the only role assignment required. It covers all data plane operations the exporter performs: listing queues, reading queue properties (message count), and peeking messages (message age).

Example role assignment:

```bash
az role assignment create \
  --assignee <principal-id> \
  --role "Storage Queue Data Reader" \
  --scope /subscriptions/<sub-id>/resourceGroups/<rg>/providers/Microsoft.Storage/storageAccounts/<account>
```

## Configuration

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-collection.interval` | `5s` | Metric collection interval |
| `-web.listen-address` | `:9874` | Address to listen on |
| `-web.telemetry-path` | `/metrics` | Path for metrics endpoint |
| `-log.level` | `warn` | Log level: `debug`, `info`, `warn`, `error`, `fatal` |
| `-log.format` | `text` | Log format: `text`, `json` |

### Environment File

A `.env` file in the working directory is auto-loaded for local development.

## Running Locally

```bash
# With storage account key
STORAGE_ACCOUNT_mystorageaccount=<key> go run . -log.level=debug

# With Azure CLI credentials
az login
STORAGE_ACCOUNT_mystorageaccount= go run . -log.level=debug
```

## Docker

```bash
docker build -t azure-storage-queue-exporter .
docker run -e STORAGE_ACCOUNT_mystorageaccount=<key> azure-storage-queue-exporter
```

Multi-arch images (amd64/arm64) are published to `ghcr.io/youscan/azure-storage-queue-exporter`.

## Helm Chart

### Installation

```bash
helm repo add azure-storage-queue-exporter https://youscan.github.io/azure_storage_queue_exporter
helm install azure-storage-queue-exporter azure-storage-queue-exporter/azure-storage-queue-exporter
```

### Storage Account Keys

```yaml
config:
  storageAccountCredentials:
    - name: mystorageaccount
      key: <storage-account-key>
    - name: anotherstorageaccount
      key: <storage-account-key>
```

### Workload Identity

```yaml
config:
  storageAccountCredentials:
    - name: mystorageaccount  # no key = use DefaultAzureCredential
    - name: anotherstorageaccount
  workloadIdentity:
    enabled: true
    clientId: "00000000-0000-0000-0000-000000000000"
```

This creates a Kubernetes ServiceAccount with the `azure.workload.identity/client-id` annotation and adds the `azure.workload.identity/use: "true"` label to the pod.

### Mixed Authentication

Key-based and identity-based accounts can coexist:

```yaml
config:
  storageAccountCredentials:
    - name: legacyaccount
      key: <storage-account-key>
    - name: modernaccount  # uses DefaultAzureCredential
  workloadIdentity:
    enabled: true
    clientId: "00000000-0000-0000-0000-000000000000"
```

## Health Endpoints

| Path | Description |
|------|-------------|
| `/healthz` | Liveness probe — returns 200 when HTTP server is up |
| `/readyz` | Readiness probe — returns 200 after first successful collection |
