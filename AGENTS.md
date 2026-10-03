# AGENTS.md — openshift/karpenter-operator

Guidance for AI agents working in this repository.
See [README.md](./README.md) for user documentation and [CONTRIBUTING.md](./CONTRIBUTING.md) for contribution workflow.

## Project overview

`karpenter-operator` manages [Red Hat build of Karpenter](https://www.redhat.com/en/blog/introducing-red-hat-build-karpenter) on OpenShift.
It installs the required CRDs, deploys the [AWS operand](https://github.com/openshift/aws-karpenter-provider-aws), and reports operator health through a `ClusterOperator` object in standalone mode.
Karpenter scheduling logic runs in the operand.

## Architecture

Controller selection depends on deployment mode:

- `pkg/controllers/crd` installs Karpenter CRDs in both modes.
- `pkg/controllers/karpenter` contains mode-specific operand reconcilers: `OCPController` for standalone clusters and `HCPController` for management clusters.
- `pkg/controllers/clusteroperator` reports operator health in standalone mode.

In standalone mode, the operand controller reconciles the singleton `autoscaling.openshift.io/v1alpha1` `Karpenter` resource named `default`.
In management-cluster mode, the CRD controller uses the hosted-cluster client configured by `--target-kubeconfig`, while the operand controller watches `HostedControlPlane` objects in the management cluster.
`ClusterOperator` status is not reported in management-cluster mode.

Two API groups serve different purposes:

- `pkg/apis/autoscaling`: `autoscaling.openshift.io/v1alpha1`, the operator lifecycle API
- `api/karpenter`: `karpenter.hypershift.openshift.io/v1`, Karpenter APIs installed for the operand

`api/` is a separate Go module.
Cloud-specific behavior belongs behind `pkg/cloudprovider/common.CloudProvider`; AWS is implemented in `pkg/cloudprovider/aws`.

## Repository map

```text
cmd/                     Binary entry point
api/                     Separate Go module for operand-facing APIs
pkg/apis/autoscaling/    Operator lifecycle API
pkg/controllers/         Controller wiring; CRD, OCP/HCP operand, and ClusterOperator controllers
pkg/cloudprovider/       Cloud-provider interface and implementations
pkg/assets/              Embedded CRDs and RBAC
install/                 Operator installation manifests
hack/                    Generation, verification, and E2E tooling
test/                    E2E suites and shared helpers
```

## Common pitfalls

1. Do not conflate the two API groups: `autoscaling.openshift.io` controls the operator lifecycle, while `karpenter.hypershift.openshift.io` is consumed by the operand.
2. Do not assume both modes use the lifecycle `Karpenter` resource.
   Management-cluster mode deploys operands from `HostedControlPlane` objects and does not register the `ClusterOperator` controller.
3. Root-module Go commands do not traverse `api/`; run module-specific commands there when needed.
4. Use `hosted cluster`, not `guest cluster`, for HyperShift terminology.
5. Follow the test naming and fixture guidance in [CONTRIBUTING.md](./CONTRIBUTING.md) when adding or modifying tests.

## Generated files

Never edit these files directly:

- `api/karpenter/v1/zz_generated.deepcopy.go` and `pkg/apis/autoscaling/v1alpha1/zz_generated.deepcopy.go` — `make generate`
- `install/00_autoscaling.openshift.io_karpenters.yaml` and `pkg/assets/hypershift/*.yaml` — `make manifests`
- `pkg/assets/karpenter/*.yaml`, `pkg/assets/aws/*.yaml`, and `pkg/assets/crds/*.yaml` — `make manifest-diff-sync`
- `install/04_rbac.yaml` — `make manifest-diff-sync`

`make verify` checks generated content and working-tree cleanliness.

## Human review required

Stop and ask before:

- changing either CRD API contract, because these are cluster-facing compatibility commitments
- changing RBAC except through the documented generation workflow, because permission changes require security review
- changing OpenShift fork replacements in `go.mod`, because they control downstream dependency provenance
- changing standalone versus management-cluster controller or client selection, because both deployment modes must remain correct

## Paired changes

- Changes under `pkg/apis/autoscaling/` require `make generate && make manifests`.
- Changes to types under `api/karpenter/` require `make generate && make manifests`.
- Operand branch or manifest changes require `make manifest-diff-sync`; review all changes under `pkg/assets/` and `install/04_rbac.yaml`.
- Dependency changes under `api/` require dependency commands from that module.
- Run `make verify` before submitting changes.
