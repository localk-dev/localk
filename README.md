# localk

> Run your Kubernetes stack locally with one command.

`localk` reads your existing Kubernetes manifests — from a directory or
straight from a live cluster via `kubectl` (read-only) — and generates a
working `docker-compose.yml` so every developer gets a full local stack in
seconds, with no cluster running on their laptop and no YAML rewrites.

```bash
# From a directory of manifests
localk generate ./k8s/ -o docker-compose.yml

# Or pull straight from your live cluster (read-only)
localk generate -k -n my-namespace -o docker-compose.yml

docker compose up
```

## Why localk

You have 20+ services running in production on Kubernetes. Your dev workflow
is "run one service locally and pray CI catches the rest." That is not
sustainable.

`localk` converts the manifests you already maintain into a `docker-compose.yml`
that mirrors production closely enough to develop against — without the cost
and complexity of running a real cluster on your laptop.

- **Zero rewrites.** Point at your existing manifests, get a working compose file.
- **Realistic networking.** Kubernetes service names work as hostnames locally.
- **Selective services.** Skip services you do not need; build others from local source.
- **One command.** Generate, then `docker compose up`.

## Install

> Pre-built binaries and Homebrew formula coming with the first release.

For now, build from source:

```bash
go install github.com/localk-dev/localk/cmd/localk@latest
```

## Quick start

```bash
# Generate docker-compose.yml from a directory of k8s manifests
localk generate ./k8s/

# Or pull manifests from the active kubectl context (read-only)
localk generate -k

# Preview without writing anything to disk
localk generate ./k8s/ --dry-run
localk generate -k --dry-run

# Send both docker-compose.yml and .env to a specific folder
localk generate ./k8s/ --out-dir ./build/local

# Or override the compose filename only
localk generate ./k8s/ -o compose.local.yml

# Start the stack
docker compose -f compose.local.yml up
```

### Where files are written

| Flag | Default | Notes |
|---|---|---|
| `--out-dir <dir>` | `.` | Folder for both outputs. Created if missing. |
| `-o <file>` | `docker-compose.yml` | Compose file. Joined with `--out-dir` if relative; absolute paths win. |
| `-env-out <file>` | `.env` | Sidecar secrets file. Same precedence as `-o`. |

So `--out-dir ./build` writes `./build/docker-compose.yml` + `./build/.env`,
while `--out-dir ./build -o /tmp/foo.yml` puts the compose file at `/tmp/foo.yml`
and the env file at `./build/.env`.

## Previewing the output (`--dry-run`)

Add `--dry-run` to any `generate` invocation to print exactly what would be
written, without touching disk:

```bash
localk generate ./k8s/ --dry-run
localk generate -k -n my-namespace --dry-run
```

The compose YAML is printed in full so you can verify shape, image tags,
ports, and volumes. Secret values in the `.env` preview are replaced with
`<redacted>` so production secrets never end up in your terminal scrollback.

```text
--- DRY RUN: would write /work/docker-compose.yml (3 services) ---
services:
  api:
    image: ghcr.io/example/api:1.4.2
    ...

--- DRY RUN: would write /work/.env (values redacted) ---
DB_PASSWORD=<redacted>
JWT_SECRET=<redacted>

Dry run only — no files written. Re-run without --dry-run to save.
```

In `-k` mode the cluster confirmation prompt still fires before the read,
so dry-run is the safest way to test a new namespace or context end-to-end.

## Pulling from a live cluster

If you don't have your manifests checked into the repo (Helm-rendered,
Terraform-managed, applied by hand), `localk generate -k` pulls them straight
from your cluster via `kubectl`:

```bash
# Use the active kubeconfig context + namespace, with confirmation
localk generate -k

# Pin a specific namespace
localk generate -k -n my-namespace

# Pin both context and namespace, skip confirmation (CI / scripts)
localk generate -k --context staging -n my-namespace -y
```

Before any cluster call, localk prints the resolved context and namespace
and asks you to confirm:

```
Cluster context: my-namespace-prod
Namespace:       my-namespace
Pulling (read-only): Deployments, Services, ConfigMaps, Secrets, PVCs
localk only invokes `kubectl get` and `kubectl config view`.
It never modifies, creates, or deletes anything in the cluster.
Continue? [y/N]
```

### Safety: read-only by design

localk's kubectl integration is provably read-only. The only kubectl
subcommands it ever invokes are:

- `kubectl get <resources> -n <namespace>` — fetch manifests
- `kubectl config current-context` — read the active context
- `kubectl config view --minify` — read the active namespace

Any other verb (`apply`, `delete`, `patch`, `edit`, `exec`, `create`,
`replace`, `scale`, `rollout`, `port-forward`, ...) is rejected before any
process is spawned. The allowlist lives in
[`internal/kubectl/kubectl.go`](internal/kubectl/kubectl.go) and is enforced
by unit tests — read it, audit it, regression-test it.

> **Secrets warning.** Cluster Secrets are decoded and written to a sidecar
> `.env` file next to your compose file. These are real production values.
> Add `.env` to `.gitignore` and never commit it.

### Extra safety: a read-only kubeconfig context

If you want defense-in-depth (or your own paranoia satisfied), point localk
at a kubeconfig context tied to a read-only ServiceAccount:

```yaml
# read-only-sa.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: localk-readonly
  namespace: my-namespace
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: localk-readonly-view
  namespace: my-namespace
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: view
subjects:
- kind: ServiceAccount
  name: localk-readonly
  namespace: my-namespace
```

Apply that, then build a kubeconfig context from the ServiceAccount's token
and pass `--context` to localk. Even if localk tried to do something
destructive, the cluster would refuse it.

## How it works

`localk` parses your Kubernetes resources and translates them into equivalent
docker-compose constructs:

| Kubernetes              | Docker Compose                            |
|-------------------------|-------------------------------------------|
| `Deployment`            | `service` (image, env, command, volumes)            |
| `StatefulSet`           | `service` with named volume per `volumeClaimTemplate` |
| `Service`               | service hostname + port mapping                     |
| `ConfigMap`             | `environment` entries / mounted files               |
| `Secret`                | `.env` entries (with warning)                       |
| `PersistentVolumeClaim` | named `volume`                                      |
| `Ingress`               | `caddy` reverse proxy + generated `Caddyfile`       |

A `Deployment` named `api` and a `Service` named `api` are merged into one
compose service called `api`. Other services in the stack can reach it at
`http://api:<port>` — the same hostname they use in production. The same
applies to `StatefulSet`s: each `volumeClaimTemplate` becomes a named
compose volume prefixed with the workload name, so two stateful services
that both call their data volume "data" don't collide.

### Ingress → Caddy

When the input includes one or more `Ingress` resources, localk emits an
extra `proxy` compose service running [Caddy](https://caddyserver.com)
plus a generated `Caddyfile` next to your compose file. Caddy publishes
host port 80 and forwards traffic to the right backend service based on
host and path.

```text
# in your k8s manifests:
- host: app.example.com
  http:
    paths:
    - path: /admin
      pathType: Prefix
      backend: { service: { name: ui-admin, port: { number: 80 } } }
    - path: /api
      pathType: Prefix
      backend: { service: { name: api, port: { number: 80 } } }
```

…produces a Caddyfile that routes `http://app.example.localhost/admin` to
`ui-admin:80` and `http://app.example.localhost/api` to `api:80`.

**Hostname mapping.** Production hosts are rewritten by replacing the
last domain segment with `localhost` — `app.example.com` becomes
`app.example.localhost`. `*.localhost` resolves to `127.0.0.1` on every
major OS, so no `/etc/hosts` edits are needed.

**Path types.** `Prefix` (and unset) maps to Caddy `handle_path`, which
strips the prefix before forwarding (so `/api/users` reaches the backend
as `/users`). `Exact` maps to `handle` with no wildcard.

**Backend port collisions, fixed.** When a service is referenced as an
Ingress backend, localk strips its host-port publishing in the compose
output. This avoids the very common case where many k8s services serve
on container :80, and `docker compose up` would otherwise fail with port
collisions on host:80. Backends remain reachable through the proxy and
via intra-compose DNS for service-to-service traffic. Services *not*
behind any Ingress (databases, message queues, observability stacks)
keep their host port mappings so you can still hit them directly with
your dev tools.

## Configuration: `localk.yaml`

Drop a `localk.yaml` in your repo root to tweak how the local stack is
generated. The file is optional — without it, localk emits a faithful
translation of your k8s manifests. With it, you can:

```yaml
services:
  api:
    # Build from a local Dockerfile instead of pulling the prod image.
    # Shorthand:
    build: ./services/api
    # Or explicit form when you need a non-default Dockerfile:
    # build:
    #   context: ./services/api
    #   dockerfile: Dockerfile.dev

  worker:
    # Don't run this service locally — the developer runs it natively
    # or doesn't need it at all.
    skip: true

  postgres:
    # Use an upstream image locally instead of the custom prod build.
    image: postgres:15-alpine
```

**How service names are matched.** The keys under `services:` match the
final compose service name, which is the matched k8s `Service` name (or the
`Deployment` / `StatefulSet` name when no Service references the pod). This
is the same hostname other services use to reach it in production.

**Mismatches surface as warnings.** If your `localk.yaml` references a
service name that doesn't show up in the input manifests (typo, renamed
service, stale entry), localk prints a warning so the file doesn't silently
rot.

**Custom config path.** Pass `--config <path>` to use a different file —
useful for per-environment configs:

```bash
localk generate ./k8s/ --config localk.staging.yaml
```

## What localk does NOT do

- Rebuild your code on file changes — use [Tilt](https://tilt.dev) or
  [Skaffold](https://skaffold.dev) for that
- Replace cloud services like S3 or RDS — coming in a future release
- Seed databases with realistic data — coming in a future release
- Handle custom CRDs, HPA, NetworkPolicies, RBAC

## Comparison

| Tool       | Use case                                         |
|------------|--------------------------------------------------|
| `localk`   | Convert k8s prod manifests → local compose stack |
| Tilt       | Inner-loop dev against a local k8s cluster       |
| Skaffold   | CI/CD-oriented k8s dev workflow                  |
| Okteto     | Cloud-hosted k8s dev environments                |
| `kompose`  | One-shot k8s → compose conversion (legacy)       |

## Status

Early. Currently supports `Deployment`, `StatefulSet`, `Service`,
`ConfigMap`, `Secret`, `PersistentVolumeClaim`, and `Ingress` (via a
generated Caddy reverse proxy) — both from a directory of YAML files and
from a live cluster via `localk generate -k`. Per-service overrides via
`localk.yaml`. Helm template support and Kustomize are on the roadmap.

## License

MIT
