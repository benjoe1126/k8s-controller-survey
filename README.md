# k8s-controller-survey

A static analysis tool to classify Kubernetes controllers as **State-of-the-World (SoTW)** vs **Edge-Triggered** based on their reconciliation patterns.

## Background

Kubernetes controllers using `controller-runtime` implement a `Reconcile` function that responds to resource changes. The efficiency of this function varies dramatically based on the reconciliation strategy:

- **Edge-Triggered**: Processes only the changed resource and its immediate context. Complexity is `O(|changes|)`.
- **State-of-the-World (SoTW)**: Lists all resources of a type and reconciles the entire state. Complexity is `O(|state|)`.

This tool analyzes controller source code to detect which pattern each controller uses.

## Classification Algorithm

The tool detects patterns in `Reconcile` functions and scores them:

| Pattern | Score | Interpretation |
|---------|-------|----------------|
| `client.List()` with no request-scoped selector | +3 | Strong SoTW |
| `client.List()` with only namespace from request | +1 | Weak SoTW |
| Loop containing write operations | +3 | Strong SoTW |
| `client.Get()` not derived from request | +1 | SoTW context |
| `client.Get(ctx, req.NamespacedName, ...)` | -1 | Edge-triggered |
| `client.Get()` with request-derived key | -1 | Edge-triggered |
| `if IsNotFound { return }` early return | -2 | Classic edge-triggered |
| Single write operation (not in loop) | -1 | Edge-triggered |
| Finalizer handling | -1 | Edge-triggered |

**Classification thresholds:**
- `score ≤ -3`: Edge-triggered
- `-3 < score ≤ 0`: Mostly edge-triggered
- `0 < score ≤ 3`: Mostly SoTW
- `score > 3`: SoTW

## Installation

```bash
go install github.com/rg0now/k8s-controller-survey/cmd/survey@latest
```

Or build from source:
```bash
git clone https://github.com/rg0now/k8s-controller-survey
cd k8s-controller-survey
go build -o survey ./cmd/survey
```

## Usage

### Analyze a single repository

```bash
survey analyze --repo=https://github.com/cert-manager/cert-manager
```

### Analyze multiple repositories

```bash
# From a file (one URL per line)
survey analyze --repos=repos.txt --output=results.jsonl

# Output to SQLite
survey analyze --repos=repos.txt --output-db=results.db
```

### Generate a report

```bash
survey report --input=results.jsonl
survey report --db=results.db --format=markdown
```

### Discover repositories

```bash
# Find controller-runtime users on GitHub
survey discover --github-token=$GITHUB_TOKEN --min-stars=100 --output=repos.json
```

## Output Format

Results are output as JSONL (one JSON object per line):

```json
{
  "id": "cert-manager/cert-manager#pkg/controller/certificates/controller.go#142",
  "repo": "github.com/cert-manager/cert-manager",
  "file": "pkg/controller/certificates/controller.go",
  "line": 142,
  "receiver_type": "controller",
  "score": -2,
  "classification": "mostly_edge",
  "signals": [
    {
      "type": "get_req_scoped",
      "line": 148,
      "score": -1,
      "snippet": "if err := c.client.Get(ctx, req.NamespacedName, crt); err != nil {"
    },
    {
      "type": "notfound_early_return",
      "line": 150,
      "score": -2,
      "snippet": "if apierrors.IsNotFound(err) {\n    return ctrl.Result{}, nil\n}"
    },
    {
      "type": "list_namespace_scoped",
      "line": 165,
      "score": 1,
      "snippet": "c.client.List(ctx, secrets, client.InNamespace(req.Namespace))"
    }
  ]
}
```

## Target Repositories

The `repos.txt` file contains a curated list of major Kubernetes operators including:

- **CNCF projects**: cert-manager, external-dns, ArgoCD, Flux, Crossplane, KEDA, etc.
- **Service mesh**: Istio, Linkerd
- **Ingress controllers**: Envoy Gateway, ingress-nginx, Traefik
- **Database operators**: Zalando Postgres, CrunchyData, Strimzi
- **Policy engines**: Kyverno, Gatekeeper

## Development

```bash
# Run tests
go test ./...

# Build
go build ./cmd/survey

# Test on a single repo
./survey analyze --repo=https://github.com/kubernetes-sigs/external-dns --output=test.jsonl
```

## License

Apache 2.0
