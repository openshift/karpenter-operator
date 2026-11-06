# Testing karpenter-operator

See [CONVENTIONS.md](./CONVENTIONS.md#test-conventions) for test naming, table structure, and fixture conventions.

## Running tests

- Unit tests are required for new logic, bug fixes, and behavior changes.
  `make test` runs tests under `pkg/`.
- Component or integration tests are recommended when changing interactions between packages.
- End-to-end tests are expected for new features and significant behavior changes.
  `make e2e` requires access to a cluster through `KUBECONFIG`.
- `make karpenter-core-regression` runs the Karpenter core regression suite in the OpenShift Hosted Control Plane CI environment.
- `make test-schema` runs NodeClass CRD schema tests against OpenShift kube-apiserver versions through envtest and needs no cluster.
- `make verify` runs vet, lint, unit tests, generation, manifest checks, and verifies that the working tree remains clean.

## Before submitting

Before requesting review, run:

```shell
make build
make verify
```

Then inspect the complete diff for unintended changes, credentials, and debug code.

## Deployment fixture tests

[`TestReconcileDeployment`](./pkg/controllers/karpenter/hcp_controller_test.go) checks that repeated HCP reconciliation renders the same operand Deployment SSA payload, then compares it once with checked-in YAML to test rendering rather than API validation or operand startup.

Check fixtures:

```shell
go test ./pkg/controllers/karpenter -run '^TestReconcileDeployment$' -count=1
```

Regenerate only for intentional manifest changes:

```shell
UPDATE=true go test ./pkg/controllers/karpenter -run '^TestReconcileDeployment$' -count=1
git diff -- pkg/controllers/karpenter/testdata/
```

- Only `UPDATE=true` writes fixtures.
- Normal runs fail on missing or changed YAML.
- Review the diff, rerun without `UPDATE`, and commit fixtures with their source changes.
- Check `git status` and inspect newly generated, untracked fixtures directly, since `git diff` omits them.
- Never regenerate merely to hide a failure.
- Use distinct subtests for distinct outputs.
- Remove obsolete fixtures when renaming tests.

## Schema tests

The schema tests apply the generated NodeClass CRDs in [`pkg/assets/hypershift/`](./pkg/assets/hypershift/) to real etcd and kube-apiserver binaries from the OpenShift envtest index.
`SCHEMA_TEST_K8S_VERSIONS` lists the versions; the oldest, 1.35, is OCP 4.22, the oldest supported release.

```shell
make test-schema
make test-schema SCHEMA_TEST_K8S_VERSIONS=1.37.1
KUBEBUILDER_ASSETS=<dir> go test -tags envtest ./test/schema/...
```

- The last command reuses existing binaries.
- The `envtest` build tag keeps plain `go test ./...` from running the suite.

Suites live in `pkg/assets/hypershift/tests/<crdName>/*.testsuite.yaml`.
The format follows the [openshift/api tests](https://github.com/openshift/api/tree/master/tests), with these differences:

- `expected` is optional for passing `onCreate` cases.
- Each `onUpdate` case needs exactly one of `expected`, `expectedError`, or `expectedStatusError`.
- Unknown suite fields fail loading.
- A rejection must be an API validation error (`IsInvalid`).

Name cases as described in [CONVENTIONS.md](./CONVENTIONS.md#test-conventions).
Write one failing case per validation rule and one passing case per area.

When a `KubeletConfiguration` field becomes typed, add it to the kubelet ratcheting suite.
`TestKubeletRatchetingSuiteCoversTypedFields` fails until every typed field appears there.
