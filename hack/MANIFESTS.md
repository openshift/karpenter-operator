# Manifest Generation

This repo uses two scripts to keep `install/` manifests in sync with upstream
and the operator's RBAC requirements.

## Architecture

```none
  openshift/aws-karpenter-provider-aws
  ├── charts/karpenter/crds/*.yaml      (raw CRDs)
  └── charts/karpenter/templates/*.yaml (Helm RBAC templates)
          │                                    │
          │ ─ hack/manifest-diff-upstream.sh ─ │
          │                                    │
          ▼                                    ▼
  pkg/assets/karpenter/*.yaml     pkg/assets/crds/*.yaml (core CRDs)
  pkg/assets/aws/*.yaml           pkg/assets/azure/*.yaml (Azure CRD)
  (operand RBAC + AWS CRD)       (operator applies at runtime)
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
| `pkg/assets/aws/*.yaml` | `manifest-diff-upstream.sh` | Operator at runtime | AWS-specific operand RBAC and EC2NodeClass CRD |
| `pkg/assets/azure/*.yaml` | `manifest-diff-upstream.sh` | Operator at runtime | AKSNodeClass CRD |
| `pkg/assets/crds/*.yaml` | `manifest-diff-upstream.sh` | Operator at runtime | Core Karpenter CRDs (NodePool, NodeClaim, NodeOverlay) |
| `install/04_rbac.yaml` | `manifest-diff.sh` | CVO | Operator RBAC + escalation superset |

## Key concepts

**Operator RBAC** (`pkg/assets/operator/rbac.yaml`): Hand-maintained. Contains the
operator's own SA, Roles, and Bindings (what the operator pod itself needs to
function). Also includes cross-namespace operand RBAC like the kube-dns Role
that CVO must apply statically.

**Operand RBAC** (`pkg/assets/karpenter/`, `pkg/assets/aws/`): These are the Roles and
ClusterRoles that the *karpenter operand* needs. The operator applies them
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

Fetches the OpenShift Karpenter fork, renders the Helm chart, and extracts:

- RBAC resources → `pkg/assets/karpenter/` and `pkg/assets/aws/`
- Core CRDs → `pkg/assets/crds/`
- AWS CRDs → `pkg/assets/aws/`
- Azure CRDs → `pkg/assets/azure/`

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
# Update OPERAND_BRANCH in hack/manifest-diff-upstream.sh, then:
make manifest-diff-sync
```

**New upstream RBAC resource appeared** (script fails with "unhandled"):
Add an `extract KIND NAME FILE` call in the appropriate section of
`manifest-diff-upstream.sh`, then re-run `make manifest-diff-sync`.

**Graduating feature set** (DevPreview → TechPreview → GA):
Edit `CVO_FEATURE_SET` in `hack/manifest-diff.sh`, then `make manifest-diff-sync`.
Also update `install/00_namespace.yaml`, `install/05_deployment.yaml`, and
`install/06_clusteroperator.yaml` manually (static files).
