// Package runner runs the NodeClass CRD schema test suites against an envtest apiserver.
// It is adapted from github.com/openshift/api/tests and differs from the original as follows:
//   - There is no feature-gate, cluster-profile or feature-set handling. The CRD under test is the
//     file in the CRD directory whose metadata.name equals the suite's crdName.
//   - Suite files are found by walking the tests directory for `*.testsuite.yaml`, sorted by path.
//     Finding none is an error.
//   - Suite files are parsed strictly, and each suite is validated when it is loaded.
//   - `expected` is optional for onCreate cases, and `expectedError` must be a validation error.
//   - CRD patches are applied with github.com/evanphx/json-patch/v5.
//   - Each suite continues after a failed case, CRD installation is retried, and CRD removal is awaited.
package runner

import (
	"context"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ctx     = context.Background()
	timeout = 5 * time.Second
)

// Env is the envtest environment the generated suites run against.
// Its fields are set before the first suite runs.
type Env struct {
	Config *rest.Config
	Client client.Client
}
