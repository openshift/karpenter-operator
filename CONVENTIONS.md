# Conventions

## Controller conventions

Controllers live in dedicated `pkg/controllers/` subpackages (e.g. `nodeclass/default`). You must update `AGENTS.md` when adding/removing controllers or changing their responsibilities, watches, or registration/client requirements.

For new controllers:

- Use `controller.go` for the main implementation and `Reconcile`, and `controller_test.go` for tests; split helpers as needed.
- Define a `Controller` struct for clients, config, and dependencies, and a `ControllerConfig` struct for configuration.
- Use `func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller` to initialize dependencies from the manager/config, not globals.
- Implement `Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)`, `Name() string`, and `SetupWithManager(mgr ctrl.Manager) error`. Put watches/predicates in setup and reconciliation logic in `Reconcile` or helpers.
- Wire into `NewControllers` in [`pkg/controllers/controllers.go`](./pkg/controllers/controllers.go) with a `ControllerConfig`; `Setup` calls `SetupWithManager` to register controllers.

## Logging

- Use `ctrl.LoggerFrom(ctx)` in controllers to preserve reconcile context; avoid repeating fields already provided by the logger.
- Use `Info` (V(0)) for meaningful events and state changes, and `V(1).Info` for expected waits and diagnostic details. Prefer these two levels over higher verbosity levels.
- Keep messages constant, concise, and capitalized; put variable content in key-value pairs, not formatted messages. Use lowercase, space-separated keys with consistent terminology (e.g. `"api version"`).
- Log successful operations after they complete. Avoid routine reconcile-entry logs, no-op logs, and repeated update logs that do not represent real state changes.
- Log Kubernetes objects directly with descriptive resource keys, or use `"object"` for generic objects. The production Zap encoder records object identity; development mode may include full contents. Avoid dumping large non-Kubernetes values.
- Use `Error(err, "Message", ...)` for failures that need logging. Prefer returning wrapped reconcile errors over logging them twice; controller-runtime logs returned errors. Wrap with `%w` and concise lowercase operation context: prefer a message like `fmt.Errorf("building deployment: %w", err)` over `fmt.Errorf("failed to build deployment: %w", err)`. The error already indicates failure; repeating `"failed to"` at each wrapping layer adds noise.

## Test conventions

Every Go test case name must follow this format:

```go
"When <condition>, it should <expected behavior>": {},
```

Use `map[string]struct{...}` for table-driven tests, with each test case name as its map key.

Use real-world values in test fixtures when possible, such as `quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64` instead of `example.com/image:latest`.
Real values catch edge cases that synthetic values miss.

### Fixture tests

- For new cases, exercise actual rendering or reconciliation and use [`testutil.CompareWithFixture`](./test/pkg/testutil/fixtures.go).
- Objects are marshaled as YAML.
- Strings and bytes are compared verbatim.
- Each subtest uses `testdata/zz_fixture_<sanitized test name>.yaml` relative to its package.

## Code style

- Run `make fmt` for root-module Go changes.
- Run `make lint` or `make lint-fix`; `.golangci.yml` defines lint and import-order rules.
- Use lowercase error strings without trailing punctuation, and wrap errors with context and `%w`.
- Use structured logging with constant messages and key-value pairs.
- Controllers utilize [Server Side Apply (SSA)][SSA] when applying and reconciling owned resources.
- Match surrounding code and test style.

[SSA]: https://kubernetes.io/blog/2022/10/20/advanced-server-side-apply/