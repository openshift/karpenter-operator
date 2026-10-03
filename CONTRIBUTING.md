# Contributing to karpenter-operator

See [README.md](./README.md) for a project overview.

## Related resources

- [Karpenter operand](https://github.com/openshift/aws-karpenter-provider-aws)
- [OpenShift CI configuration](https://github.com/openshift/release/tree/master/ci-operator/config/openshift/karpenter-operator)
- [AI agent guidance](./AGENTS.md)

## Review and approval policy

Every change must be understood and approved by two humans.
This can be the PR author and a reviewer or, when the author does not fully understand AI-generated changes, two human reviewers.

PRs created by deterministic automation in CI and related systems whose code has been reviewed by the OpenShift engineering organization require one human review.

Review changes from several angles:

- **Product architecture:** Does the change preserve the operator/operand boundary and standalone versus Hosted Control Plane behavior?
- **Security:** Does it introduce attack surfaces, credential-handling risks, or broader privileges?
- **Thread safety:** Is shared state synchronized correctly?
- **Regressions:** Could existing behavior break?
- **Other components:** Does the change require coordinated operand, API, or CI changes?

## PR title convention

Prefix PR titles with a Jira reference:

- `AUTOSCALE-123: Fix the whatsit in the thingamajig`
- `OCPBUGS-456: Correct nil pointer in scaler shutdown`
- `NO-JIRA: Update Go dependencies`

## PR workflow

This repository uses [OpenShift CI](https://docs.ci.openshift.org/), not GitHub Actions.
CI configuration lives in [openshift/release](https://github.com/openshift/release/tree/master/ci-operator/config/openshift/karpenter-operator).

Required labels for merge:

- `lgtm` — added by an OpenShift developer after review with `/lgtm`
- `approved` — added by an [OWNERS](./OWNERS) approver with `/approve`
- `verified` — added by anyone in the OpenShift organization, typically the PR author, with `/verified`

Useful PR commands:

| Command | Effect |
| ------- | ------ |
| `/lgtm` | Add the `lgtm` label after reviewing |
| `/lgtm cancel` | Remove the `lgtm` label |
| `/approve` | Add the `approved` label (approvers only) |
| `/retest` | Re-run failed required tests |
| `/retest-required` | Re-run only failed required tests |
| `/test <test-name>` | Run a specific test |
| `/test verify` | Run `make verify` |
| `/test e2e-aws` | Run the OpenShift AWS end-to-end workflow |
| `/test e2e-aws-operator` | Run the operator end-to-end suite on AWS |
| `/hold` | Prevent the PR from merging |
| `/hold cancel` | Remove the merge hold |
| `/verified` | Mark the PR as verified |
| `/pipeline required` | Run required second-stage tests without waiting for `/lgtm` |
| `/cherry-pick <branch>` | Create a cherry-pick PR for a release branch |

**LGTM mode and E2E tests:** Repositories enrolled in [LGTM mode](https://docs.ci.openshift.org/how-tos/creating-a-pipeline/#the-pipeline-required-command) defer second-stage tests until `/lgtm` is applied.
This avoids spending CI resources on changes that have not been reviewed.
Use `/pipeline required` when tests must run earlier, such as before requesting review.

The `/verified` command may include a short description of the verification:

```text
/verified
/verified by e2e-aws-operator
/verified by unit tests
/verified later
```

To prevent premature merges:

- Prefix the PR title with `WIP:`; Prow adds the `do-not-merge/work-in-progress` label.
- Use `/hold` while waiting for review or testing.

## Testing

- Unit tests are required for new logic, bug fixes, and behavior changes.
  `make test` runs tests under `pkg/`.
- Component or integration tests are recommended when changing interactions between packages.
- End-to-end tests are expected for new features and significant behavior changes.
  `make e2e` requires access to a cluster through `KUBECONFIG`.
- `make karpenter-core-regression` runs the Karpenter core regression suite in the OpenShift Hosted Control Plane CI environment.
- `make verify` runs vet, lint, unit tests, generation, manifest checks, and verifies that the working tree remains clean.

### Test conventions

Every Go test case name must follow this format:

```go
name: "When <condition>, it should <expected behavior>"
```

Use real-world values in test fixtures when possible, such as `quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64` instead of `example.com/image:latest`.
Real values catch edge cases that synthetic values miss.

Before requesting review, run:

```shell
make build
make verify
```

Then inspect the complete diff for unintended changes, credentials, and debug code.

## Generated files

Do not edit generated files directly:

- `api/karpenter/v1/zz_generated.deepcopy.go` and `pkg/apis/autoscaling/v1alpha1/zz_generated.deepcopy.go` — run `make generate`
- `install/00_autoscaling.openshift.io_karpenters.yaml` and `pkg/assets/hypershift/*.yaml` — run `make manifests`
- `pkg/assets/karpenter/*.yaml`, `pkg/assets/aws/*.yaml`, and `pkg/assets/crds/*.yaml` — run `make manifest-diff-sync`
- `install/04_rbac.yaml` — run `make manifest-diff-sync`

Commit generated changes with their source changes.
The `api/` directory is a separate Go module; run Go dependency and formatting commands from that directory when changing its dependencies or source.

## Code style

- Run `make fmt` for root-module Go changes.
- Run `make lint` or `make lint-fix`; `.golangci.yml` defines lint and import-order rules.
- Use lowercase error strings without trailing punctuation, and wrap errors with context and `%w`.
- Use structured logging with constant messages and key-value pairs.
- Controllers utilize [Server Side Apply (SSA)][SSA] when applying and reconciling owned resources.
- Match surrounding code and test style.

[SSA]: https://kubernetes.io/blog/2022/10/20/advanced-server-side-apply/

## AI code review

[CodeRabbit](https://coderabbit.ai/) reviews pull requests.
Address actionable findings, or explain why a suggestion should not be applied, before requesting human review.
