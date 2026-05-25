# kflashback

**Kubernetes Resource History Tracker**

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/kflashback/kflashback)](https://goreportcard.com/report/github.com/kflashback/kflashback)

kflashback records the complete change history of your Kubernetes resources - deployments, services, statefulsets, daemonsets, pods, jobs, cronjobs, and more - so you can see exactly what changed, when, and compare any two points in time.

## Features

- **Declarative tracking** - Define a `FlashbackPolicy` CRD to specify which resources to track, retention settings, and field filters.
- **CRD-based configuration** - Configure the controller via a `KFlashbackConfig` custom resource. No need to edit deployment manifests.
- **Efficient delta storage** - Stores full snapshots on first capture, then only JSON merge patches for subsequent changes. Periodic full snapshots cap reconstruction cost.
- **Built-in compression** - Gzip compression for snapshots minimizes storage footprint.
- **Point-in-time reconstruction** - Reconstruct the exact state of any resource at any revision.
- **Visual diff** - Side-by-side, unified, and patch views to compare any two revisions.
- **Beautiful dashboard** - Modern React UI with timeline view, resource browser, infinite scroll, and JSON explorer.
- **Pluggable storage** - Ships with embedded SQLite (CGo-free). Add new backends by implementing the `storage.Store` interface.
- **Kubernetes-native** - Operator pattern using controller-runtime; no external dependencies required.
- **AI-powered insights** - Natural language queries, change summarization, and anomaly detection powered by any OpenAI-compatible LLM (OpenAI, Ollama, Anthropic via proxy).
- **AI guardrails** - Built-in secret redaction, prompt injection detection, rate limiting, and topic validation. Sensitive data never reaches the AI provider.
- **CNCF-aligned** - Apache 2.0 license, security policy, contribution guidelines, DCO.

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                    │
│                                                          │
│  ┌──────────────────┐  ┌──────────────────┐              │
│  │ KFlashbackConfig │  │ FlashbackPolicy  │              │
│  │   (singleton)    │  │  (per-workload)  │              │
│  └───────┬──────────┘  └────────┬─────────┘              │
│          │ config               │ what to track          │
│          ▼                      ▼                        │
│  ┌──────────────────────────────────────────┐            │
│  │          kflashback Controller           │            │
│  │                                          │            │
│  │  ┌──────────────┐  ┌─────────────────┐   │            │
│  │  │Config Loader │  │Policy Reconciler│   │            │
│  │  └──────────────┘  └───────┬─────────┘   │            │
│  │                            │             │            │
│  │  ┌─────────────────────────▼──────────┐  │            │
│  │  │    Resource Watchers (dynamic)     │  │            │
│  │  └─────────────────────────┬──────────┘  │            │
│  │                            │             │            │
│  │  ┌─────────────────────────▼──────────┐  │            │
│  │  │  Diff Engine (JSON merge patches)  │  │            │
│  │  └─────────────────────────┬──────────┘  │            │
│  │                            │             │            │
│  │  ┌─────────────────────────▼──────────┐  │            │
│  │  │  Pluggable Storage (sqlite, etc.)  │  │            │
│  │  └─────────────────────────┬──────────┘  │            │
│  │                            │             │            │
│  │  ┌─────────────────────────▼──────────┐  │            │
│  │  │        REST API + UI (:9090)       │  │            │
│  │  └────────────────────────────────────┘  │            │
│  └──────────────────────────────────────────┘            │
└──────────────────────────────────────────────────────────┘
```

## Storage Strategy

kflashback uses an incremental storage approach for maximum efficiency:

| Event   | What is stored                     | Typical size |
| ------- | ---------------------------------- | ------------ |
| Create  | Full snapshot (gzipped)            | 2-10 KB      |
| Update  | JSON merge patch only              | 50-500 bytes |
| Every N | Full snapshot (configurable, N=20) | 2-10 KB      |
| Delete  | Full snapshot (last known state)   | 2-10 KB      |

**Reconstruction**: To view a resource at revision R, kflashback finds the nearest snapshot ≤ R and applies all subsequent patches. With `snapshotEvery: 20`, worst-case reconstruction applies at most 19 patches.

---

## Deploying to a Kubernetes Cluster

### Prerequisites

- Kubernetes cluster (1.26+)
- `kubectl` configured with cluster-admin access

### 1. Install CRDs

```bash
kubectl apply -f config/crd/
```

This installs both CRDs:

- `FlashbackPolicy` - defines which resources to track
- `KFlashbackConfig` - configures the kflashback controller itself

### 2. Install RBAC

```bash
kubectl apply -f config/rbac/
```

### 3. Create the configuration

Create a `KFlashbackConfig` resource to configure the controller.

**Option A - SQLite (default, no external database needed):**

```yaml
apiVersion: flashback.io/v1alpha1
kind: KFlashbackConfig
metadata:
  name: kflashback
spec:
  storage:
    backend: sqlite
    dsn: /data/kflashback.db
  server:
    apiAddress: ":9090"
    metricsAddress: ":8080"
    healthAddress: ":8081"
  controller:
    leaderElection: true
    reconcileInterval: "5m"
```

```bash
kubectl apply -f config/samples/sample-config.yaml
```

**Option B - PostgreSQL:**

First, create a Secret with your database connection string:

```bash
kubectl create namespace kflashback-system  # if not already created
kubectl create secret generic kflashback-db-credentials \
  --namespace=kflashback-system \
  --from-literal=dsn='postgres://kflashback:YOUR_PASSWORD@your-db-host:5432/kflashback?sslmode=require'
```

Then apply the config referencing the Secret:

```yaml
apiVersion: flashback.io/v1alpha1
kind: KFlashbackConfig
metadata:
  name: kflashback
spec:
  storage:
    backend: postgres
    credentialsSecret:
      name: kflashback-db-credentials
      namespace: kflashback-system
      key: dsn
  server:
    apiAddress: ":9090"
  controller:
    leaderElection: true
```

```bash
kubectl apply -f config/samples/sample-config-postgres.yaml
```

> **Credential resolution priority** (highest to lowest):
> 
> 1. `KFLASHBACK_STORAGE_DSN` environment variable
> 2. Kubernetes Secret referenced by `spec.storage.credentialsSecret`
> 3. `spec.storage.dsn` field in the CR
> 4. CLI `--storage-dsn` flag
> 
> This means you can also inject credentials by mounting a Secret as an environment variable in the Deployment, which works well with cloud provider integrations (AWS Secrets Manager, GCP Secret Manager, HashiCorp Vault, etc.).

### 4. Build and load the container image

No pre-built image is published yet - build it locally:

```bash
make docker-build
```

Then load it into your cluster:

```bash
# kind
kind load docker-image ghcr.io/prashanthjos/kflashback:latest

# minikube
minikube image load ghcr.io/prashanthjos/kflashback:latest
```

### 5. Deploy the controller

```bash
kubectl apply -f config/manager/
```

This creates the `kflashback-system` namespace, a Deployment, PVC, and Service.

### 6. Create a tracking policy

```yaml
apiVersion: flashback.io/v1alpha1
kind: FlashbackPolicy
metadata:
  name: track-workloads
spec:
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      excludeNamespaces: [kube-system]
    - apiVersion: apps/v1
      kind: StatefulSet
    - apiVersion: v1
      kind: Service
    - apiVersion: batch/v1
      kind: Job
    - apiVersion: batch/v1
      kind: CronJob
  retention:
    maxAge: "720h"       # 30 days
    maxRevisions: 500
  storage:
    snapshotEvery: 20
    compressSnapshots: true
  fieldConfig:
    trackStatus: false
  tracking:
    creations: true
    updates: true
    deletions: true
```

```bash
kubectl apply -f config/samples/sample-policy.yaml
```

### 7. Access the dashboard

```bash
kubectl port-forward -n kflashback-system svc/kflashback-api 9090:9090
```

Open [http://localhost:9090](http://localhost:9090).

### Quick install (one command)

```bash
kubectl apply -f config/crd/ -f config/rbac/ -f config/samples/sample-config.yaml -f config/manager/ -f config/samples/sample-policy.yaml
```

### Uninstall

```bash
make undeploy
# or manually:
kubectl delete -f config/manager/
kubectl delete -f config/rbac/
kubectl delete -f config/crd/
```

---

## Local Development

### Prerequisites

- Go 1.21+
- Node.js 18+
- A Kubernetes cluster (e.g. `kind`, `minikube`, or a remote cluster)
- `kubectl` configured

### Build & Run

```bash
# Install dependencies
make ui-install

# Build the UI
make ui-build

# Build the Go binary
make build

# Run locally (uses CLI flags, skips KFlashbackConfig CR)
make run
```

The `make run` command starts kflashback with:

- `--config-name=""` - skips KFlashbackConfig CR lookup
- `--storage-backend=sqlite --storage-dsn=./kflashback.db` - local SQLite file
- `--ui-dir=./ui/dist` - serves the built UI

Open [http://localhost:9090](http://localhost:9090).

### UI development with hot reload

In a separate terminal:

```bash
make ui-dev
```

This starts the Vite dev server (usually on `:5173`) with hot module replacement. The dev server proxies API calls to the Go backend on `:9090`.

### Running tests

```bash
make test
```

### Code generation

After modifying CRD types in `api/v1alpha1/types.go`:

```bash
make generate
```

This regenerates DeepCopy methods and CRD YAML manifests.

### Docker build

```bash
make docker-build
```

---

## Configuration Reference

### KFlashbackConfig CRD

The `KFlashbackConfig` CR is a **cluster-scoped singleton** that configures the kflashback controller. The controller reads it at startup. CLI flags serve as defaults when no CR is found.

| Field                                      | Type   | Default               | Description                                                       |
| ------------------------------------------ | ------ | --------------------- | ----------------------------------------------------------------- |
| `spec.storage.backend`                     | string | `sqlite`              | Storage backend (`sqlite`, `postgres`)                            |
| `spec.storage.dsn`                         | string | `/data/kflashback.db` | DSN or file path (avoid credentials here)                         |
| `spec.storage.credentialsSecret.name`      | string | -                     | Name of Secret containing the DSN                                 |
| `spec.storage.credentialsSecret.namespace` | string | `kflashback-system`   | Namespace of the Secret                                           |
| `spec.storage.credentialsSecret.key`       | string | `dsn`                 | Key in Secret holding the connection string                       |
| `spec.server.apiAddress`                   | string | `:9090`               | API server bind address                                           |
| `spec.server.metricsAddress`               | string | `:8080`               | Metrics endpoint address                                          |
| `spec.server.healthAddress`                | string | `:8081`               | Health probe address                                              |
| `spec.controller.leaderElection`           | bool   | `false`               | Enable leader election                                            |
| `spec.controller.reconcileInterval`        | string | `5m`                  | Retention cleanup interval                                        |
| `spec.ai.enabled`                          | bool   | `false`               | Enable AI-powered features                                        |
| `spec.ai.provider`                         | string | `openai`              | Provider type (`openai`, `ollama`, `anthropic`)                   |
| `spec.ai.endpoint`                         | string | -                     | API base URL (see provider setup below)                           |
| `spec.ai.model`                            | string | `gpt-4o-mini`         | Model name                                                        |
| `spec.ai.maxTokens`                        | int    | `1024`                | Max tokens per AI response                                        |
| `spec.ai.temperature`                      | string | `0.3`                 | Response randomness (0.0–1.0)                                     |
| `spec.ai.contextMode`                      | string | `compact`             | `compact` (fast, local models) or `full` (detailed, cloud models) |
| `spec.ai.credentialsSecret.name`           | string | -                     | Secret containing the API key                                     |
| `spec.ai.credentialsSecret.namespace`      | string | `kflashback-system`   | Namespace of the Secret                                           |
| `spec.ai.credentialsSecret.key`            | string | `api-key`             | Key in Secret holding the API key                                 |

### FlashbackPolicy CRD

The `FlashbackPolicy` CR defines **which resources to track** and retention settings. You can create multiple policies.

| Field                                | Type          | Default  | Description                        |
| ------------------------------------ | ------------- | -------- | ---------------------------------- |
| `spec.resources[].apiVersion`        | string        | required | API version (e.g., `apps/v1`)      |
| `spec.resources[].kind`              | string        | required | Resource kind (e.g., `Deployment`) |
| `spec.resources[].namespaces`        | []string      | all      | Namespaces to track                |
| `spec.resources[].excludeNamespaces` | []string      | none     | Namespaces to exclude              |
| `spec.resources[].excludeNames`      | []string      | none     | Resource names to exclude          |
| `spec.resources[].includeNames`      | []string      | all      | Resource names to include          |
| `spec.resources[].labelSelector`     | LabelSelector | none     | Filter by labels                   |
| `spec.retention.maxAge`              | string        | `720h`   | Max history retention duration     |
| `spec.retention.maxRevisions`        | int32         | `1000`   | Max revisions per resource         |
| `spec.storage.snapshotEvery`         | int32         | `20`     | Full snapshot interval             |
| `spec.storage.compressSnapshots`     | bool          | `true`   | Gzip compress snapshots            |
| `spec.fieldConfig.ignoreFields`      | []string      | defaults | JSON paths to ignore               |
| `spec.fieldConfig.trackStatus`       | bool          | `false`  | Track `.status` changes            |
| `spec.tracking.creations`            | bool          | `true`   | Record creation events             |
| `spec.tracking.updates`              | bool          | `true`   | Record update events               |
| `spec.tracking.deletions`            | bool          | `true`   | Record deletion events             |
| `spec.paused`                        | bool          | `false`  | Pause all tracking                 |

### CLI Flags

These are used as defaults when no `KFlashbackConfig` CR is found, or when `--config-name=""`.

| Flag                          | Default               | Description                                           |
| ----------------------------- | --------------------- | ----------------------------------------------------- |
| `--config-name`               | `kflashback`          | Name of `KFlashbackConfig` CR to read (empty to skip) |
| `--storage-backend`           | `sqlite`              | Storage backend                                       |
| `--storage-dsn`               | `/data/kflashback.db` | DSN or path                                           |
| `--api-bind-address`          | `:9090`               | API server bind address                               |
| `--metrics-bind-address`      | `:8080`               | Metrics bind address                                  |
| `--health-probe-bind-address` | `:8081`               | Health probe bind address                             |
| `--ui-dir`                    | `/ui`                 | UI static files directory                             |
| `--leader-elect`              | `false`               | Enable leader election                                |
| `--ai-enabled`                | `false`               | Enable AI features                                    |
| `--ai-endpoint`               | -                     | AI provider URL (e.g. `http://localhost:11434/v1`)    |
| `--ai-model`                  | `qwen3:8b`            | AI model name                                         |
| `--ai-api-key`                | -                     | API key (prefer `KFLASHBACK_AI_API_KEY` env var)      |
| `--ai-context-mode`           | `compact`             | `compact` or `full`                                   |

---

## AI Features

kflashback includes optional AI-powered features for understanding cluster changes. AI is **off by default** - enable it by configuring a provider.

### What AI can do

| Feature                      | API Endpoint                                      | Description                                        |
| ---------------------------- | ------------------------------------------------- | -------------------------------------------------- |
| **Natural language queries** | `POST /api/v1/ai/query`                           | Ask questions like "What deployments are tracked?" |
| **Change summarization**     | `GET /api/v1/ai/summarize/{uid}/revisions/{rev}`  | Human-readable summary of a revision               |
| **Diff summarization**       | `GET /api/v1/ai/summarize/{uid}/diff?from=1&to=5` | Summary comparing two revisions                    |
| **Anomaly detection**        | `GET /api/v1/ai/anomalies?hours=24`               | Flag unusual changes in recent history             |

### Provider setup

All providers use the **OpenAI-compatible chat completions API**. One implementation covers all providers.

#### Ollama (local, free, no API key)

```bash
# Install and start Ollama
brew install ollama && ollama serve

# Pull a model (qwen2.5:3b is fast and lightweight at 1.9GB)
ollama pull qwen2.5:3b

# Run kflashback with AI
make run AI_FLAGS="--ai-enabled --ai-endpoint=http://localhost:11434/v1 --ai-model=qwen2.5:3b --ai-context-mode=compact"
```

Or use the sample config: `config/samples/sample-config-ollama.yaml`

#### OpenAI (GPT-4o-mini, GPT-4o)

```bash
# Create a Secret with your API key
kubectl create secret generic kflashback-ai-key \
  --namespace=kflashback-system \
  --from-literal=api-key='sk-your-openai-key'
```

Or set the env var: `export KFLASHBACK_AI_API_KEY=sk-your-openai-key`

Sample config: `config/samples/sample-config-openai.yaml`

#### Anthropic (Claude)

Anthropic's native API uses a different auth format. Use an **OpenAI-compatible proxy** like LiteLLM:

```bash
pip install litellm
export ANTHROPIC_API_KEY=sk-ant-your-key
litellm --model claude-sonnet-4-20250514 --port 4000
```

Then point kflashback at `http://localhost:4000/v1`.

Sample config: `config/samples/sample-config-anthropic.yaml`

### Context modes

| Mode      | Best for                          | What's sent to the AI                    | Speed            |
| --------- | --------------------------------- | ---------------------------------------- | ---------------- |
| `compact` | Local models (Ollama, small GPUs) | Short text summary (~200 tokens)         | 10–25s           |
| `full`    | Cloud models (GPT-4o, Claude)     | Detailed JSON with resources + revisions | Depends on model |

### Credential priority

AI API key is resolved in this order (highest to lowest):

1. `KFLASHBACK_AI_API_KEY` environment variable
2. Kubernetes Secret via `spec.ai.credentialsSecret`
3. CLI `--ai-api-key` flag

### Safety guardrails

All AI features include built-in guardrails:

- **Secret redaction** - API keys, tokens, passwords, JWTs, and connection strings are stripped before sending to the AI
- **K8s resource sanitization** - Secret `data`, sensitive env vars, and credential annotations are redacted
- **Prompt injection detection** - Blocks attempts like "ignore your instructions" or "reveal your prompt"
- **Topic validation** - Only Kubernetes-related questions are accepted
- **Rate limiting** - 30 requests/minute per instance
- **Output filtering** - Responses are scanned for leaked sensitive data
- **System prompt hardening** - AI is instructed to never output credentials or generate destructive commands

---

## API Examples

```bash
# Get stats
curl http://localhost:9090/api/v1/stats

# List tracked resources
curl http://localhost:9090/api/v1/resources?kind=Deployment

# Get revision history (with pagination and filters)
curl http://localhost:9090/api/v1/resources/{uid}/history?limit=30&offset=0
curl http://localhost:9090/api/v1/resources/{uid}/history?eventType=UPDATED&since=2024-01-01T00:00:00Z

# Reconstruct resource at revision
curl http://localhost:9090/api/v1/resources/{uid}/reconstruct/5

# Diff two revisions
curl http://localhost:9090/api/v1/resources/{uid}/diff?from=3&to=7

# AI: Ask a question
curl -X POST http://localhost:9090/api/v1/ai/query \
  -H "Content-Type: application/json" \
  -d '{"question":"What deployments are tracked in my cluster?"}'

# AI: Summarize a revision
curl http://localhost:9090/api/v1/ai/summarize/{uid}/revisions/5

# AI: Summarize diff between revisions
curl http://localhost:9090/api/v1/ai/summarize/{uid}/diff?from=1&to=5

# AI: Detect anomalies in the last 24 hours
curl http://localhost:9090/api/v1/ai/anomalies?hours=24
```

## Project Structure

```
kflashback/
├── api/v1alpha1/            # CRD types (FlashbackPolicy, KFlashbackConfig)
├── cmd/kflashback/          # Main entrypoint
├── internal/
│   ├── ai/                  # AI provider, summarizer, anomaly detector, query engine, guardrails
│   ├── config/              # KFlashbackConfig CR loader
│   ├── controller/          # Policy reconciler + resource watchers
│   ├── diff/                # JSON merge patch engine
│   ├── server/              # REST API + AI API handlers
│   └── storage/             # Storage interface, factory + SQLite/PostgreSQL backends
├── ui/                      # React + Vite + TailwindCSS dashboard
├── config/
│   ├── crd/                 # CRD manifests (FlashbackPolicy, KFlashbackConfig)
│   ├── rbac/                # RBAC manifests
│   ├── manager/             # Deployment, PVC, Service
│   └── samples/             # Example CRs (sample-config.yaml, sample-policy.yaml)
├── Dockerfile               # Multi-stage build
└── Makefile                 # Build automation
```

## Roadmap

- [x] PostgreSQL storage backend
- [x] AI-powered change summaries, anomaly detection, and natural language queries
- [x] AI guardrails (secret redaction, prompt injection detection, rate limiting)
- [x] Multi-provider AI support (OpenAI, Ollama, Anthropic via LiteLLM)
- [ ] Helm chart
- [ ] Prometheus metrics
- [ ] Webhook notifications on changes
- [ ] RBAC-aware UI (multi-tenant)
- [ ] Resource rollback (restore to previous revision)
- [ ] Event correlation (link related resource changes)
- [ ] OpenTelemetry integration
- [ ] Grafana dashboard plugin

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## Security

See [SECURITY.md](SECURITY.md) for our security policy and how to report vulnerabilities.

## License

Apache License 2.0. See [LICENSE](LICENSE) for full text.
