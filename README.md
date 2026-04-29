# localk

> Run your Kubernetes stack locally with one command.

`localk` reads your existing Kubernetes manifests and generates a working
`docker-compose.yml` so every developer gets a full local stack in seconds —
no cluster, no `kubectl`, no YAML rewrites.

```bash
localk generate ./k8s/ -o docker-compose.yml
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

# Specify output path
localk generate ./k8s/ -o compose.local.yml

# Start the stack
docker compose -f compose.local.yml up
```

## How it works

`localk` parses your Kubernetes resources and translates them into equivalent
docker-compose constructs:

| Kubernetes              | Docker Compose                            |
|-------------------------|-------------------------------------------|
| `Deployment`            | `service` (image, env, command, volumes)  |
| `StatefulSet`           | `service` with named volume               |
| `Service`               | service hostname + port mapping           |
| `ConfigMap`             | `environment` entries / mounted files     |
| `Secret`                | `.env` entries (with warning)             |
| `PersistentVolumeClaim` | named `volume`                            |
| `Ingress`               | (planned) Traefik container with routing  |

A `Deployment` named `api` and a `Service` named `api` are merged into one
compose service called `api`. Other services in the stack can reach it at
`http://api:<port>` — the same hostname they use in production.

## Configuration

Optional `localk.yaml` in your repo root lets you override per-service behavior:

```yaml
services:
  api:
    build: ./services/api      # build from local Dockerfile instead of pulling
  worker:
    skip: true                 # do not include this service locally
  postgres:
    image: postgres:15-alpine  # override the image used in prod
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

Early. v0.1 supports `Deployment`, `Service`, `ConfigMap`, `Secret`, and
`PersistentVolumeClaim`. Helm chart and Kustomize support is on the roadmap.

## License

MIT
