# Examples

Reference manifests showing the major features localk handles. Each
folder is a self-contained input you can run through `localk
generate <folder>` to see the conversion in action — handy for
contributing or for sanity-checking that a feature still works on
the version of localk you have installed.

| Folder              | Demonstrates                                                  |
|---------------------|---------------------------------------------------------------|
| `simple/k8s/`       | Deployments + Services + ConfigMaps + Secrets + a PVC. Also the input for the converter's golden test. |
| `with-statefulset/` | A `StatefulSet` with a `volumeClaimTemplate`. The VCT becomes a top-level named compose volume. |
| `with-sidecars/`    | Multi-container pod (main + log-shipper + metrics-exporter) with two `initContainers`, downward-API env vars, and a shared `emptyDir` between main and sidecar. |
| `with-ingress/`     | Path-based `Ingress` (one host, multiple paths). Output gets a generated Caddy proxy + `Caddyfile`, with backend host-ports stripped. |

## Try them out

```bash
# Build the binary first
make build

# Convert any example and print the dry-run preview
./bin/localk generate ./examples/with-sidecars --dry-run

# Or write the output to a temp dir and inspect the files directly
./bin/localk generate ./examples/with-ingress --out-dir /tmp/localk-out
cat /tmp/localk-out/docker-compose.yml
cat /tmp/localk-out/Caddyfile

# Run the stack
./bin/localk up --out-dir /tmp/localk-out
```

These are intentionally small. For a battle-tested input, point
localk at your own cluster's manifests via `localk generate -k`.
