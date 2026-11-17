# Manifest Generation

This repo uses two scripts to keep `pkg/assets/` and `install/` manifests in sync with upstream and the operator's RBAC requirements.

## Architecture

```none
  openshift/aws-karpenter-provider-aws       vendor/github.com/aws/karpenter-provider-aws/pkg/apis/crds
  └── charts/karpenter/templates/*.yaml      vendor/github.com/Azure/karpenter-provider-azure/pkg/apis/crds
      (Helm RBAC templates)                  (provider CRDs incl. wrapped core CRDs)
          │                                    │
          │ ─ hack/manifest-diff-upstream.sh ─ │
          │                                    │
          ▼                                    ▼
  pkg/assets/karpenter/*.yaml      pkg/assets/aws/*.yaml   (AWS RBAC + AWS CRD set)
  (core operand RBAC)              pkg/assets/azure/*.yaml (Azure CRD set)
          │
          │         pkg/assets/operator/rbac.yaml
          │         (hand-maintained)
          │              |
          ├────────┬─────┘
          │        |
          │   hack/manifest-diff.sh
          │        │
          │        ▼
          │    ┌───────────────────────────────────────────────────┐
          │    │ install/ (CVO-managed)                            │
          │    │   04_rbac.yaml (operator + escalation privs)      │
          │    │   + CVO annotations                               │
          │    └───────────────────────────────────────────────────┘
          ▼
  Operator reconciles operand RBAC + CRDs at runtime
```

## What lives where

| Location | Owner | Applied by | Contains |
| -------- | ----- | ---------- | -------- |
| `pkg/assets/operator/rbac.yaml` | Hand-maintained | — (source only) | Operator's own SA, Roles, Bindings |
| `pkg/assets/karpenter/*.yaml` | `manifest-diff-upstream.sh` | Operator at runtime | Core operand RBAC |
| `pkg/assets/aws/*.yaml` | `manifest-diff-upstream.sh` | Operator at runtime | AWS-specific operand RBAC and CRDs |
| `pkg/assets/azure/*.yaml` | `manifest-diff-upstream.sh` | Operator at runtime | Azure-specific CRDs |
| `pkg/assets/hypershift/*.yaml` | `make manifests` (from `api/karpenter/`) | Operator at runtime (HCP mode) | OpenshiftEC2NodeClass CRD |
| `install/04_rbac.yaml` | `manifest-diff.sh` | CVO | Operator RBAC + escalation superset |

## Key concepts

**Operator RBAC** (`pkg/assets/operator/rbac.yaml`): Hand-maintained. Contains the
operator's own SA, Roles, and Bindings (what the operator pod itself needs to
function). Also includes cross-namespace operand RBAC like the kube-dns Role
that CVO must apply statically.

**Operand RBAC** (`pkg/assets/karpenter/`, `pkg/assets/aws/`): These are the Roles and
ClusterRoles that the _karpenter operand_ needs. The operator applies them
programmatically at runtime — they never go directly into `install/`.

**Escalation superset** (`karpenter-operator-operand` ClusterRole/Role in `install/04_rbac.yaml`):
Kubernetes won't let a ServiceAccount create a Role with permissions it doesn't
already hold. So `manifest-diff.sh` reads the operand rules and generates a
mirror ClusterRole/Role bound to the operator SA, satisfying escalation checks.

**CVO annotations**: `install/04_rbac.yaml` needs CVO release annotations.
These are defined in `manifest-diff.sh` (`CVO_FEATURE_SET` variable) and stamped
on all documents via a `$CVO_ANNOTATIONS_YQ` yq expression.

## Scripts

### `hack/manifest-diff-upstream.sh`

Fetches the OpenShift AWS Karpenter fork at `OPERAND_BRANCH`, renders its Helm chart, and extracts:

- RBAC resources → `pkg/assets/karpenter/` and `pkg/assets/aws/`

Copies CRDs from the vendored provider modules:

- `vendor/github.com/aws/karpenter-provider-aws/pkg/apis/crds` → `pkg/assets/aws/`
- `vendor/github.com/Azure/karpenter-provider-azure/pkg/apis/crds` → `pkg/assets/azure/`

Fails if upstream has RBAC resources not handled by an `extract` call.

### `hack/manifest-diff.sh`

Generates `install/04_rbac.yaml` by:

1. Including `pkg/assets/operator/rbac.yaml` (operator's own RBAC)
2. Extracting rules from operand assets and emitting escalation ClusterRole/Role
3. Stamping CVO annotations on all documents

## Make targets

| Target | What it does |
| ------ | ------------ |
| `make manifest-diff` | Diff-only: exits 1 if `install/04_rbac.yaml` or `pkg/assets/` are stale |
| `make manifest-diff-sync` | Regenerates everything (sync mode) |

## Common operations

**Upstream rebase** (new Karpenter version):

```bash
# Update OPERAND_BRANCH in the Makefile, then bump forks and sync assets:
make bump-forks
```

**New upstream RBAC resource appeared** (script fails with "unhandled"):
Add an `extract KIND NAME FILE` call in the appropriate section of
`manifest-diff-upstream.sh`, then re-run `make manifest-diff-sync`.

**Graduating feature set** (DevPreview → TechPreview → GA):
Edit `CVO_FEATURE_SET` in `hack/manifest-diff.sh`, then `make manifest-diff-sync`.
Also update `install/00_namespace.yaml`, `install/05_deployment.yaml`, and
`install/06_clusteroperator.yaml` manually (static files).
