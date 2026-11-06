package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

const (
	// crdDir holds the NodeClass CRDs.
	crdDir = "../../../pkg/assets/hypershift"

	ec2CRDName = "openshiftec2nodeclasses.karpenter.hypershift.openshift.io"

	createSuite = `apiVersion: apiextensions.k8s.io/v1
name: OpenshiftEC2NodeClass validation
crdName: openshiftec2nodeclasses.karpenter.hypershift.openshift.io
version: v1
tests:
  onCreate:
  - name: When version is valid, it should create the resource
    initial: |
      apiVersion: karpenter.hypershift.openshift.io/v1
      kind: OpenshiftEC2NodeClass
      spec:
        subnetSelectorTerms:
        - id: subnet-0123456789abcdef0
        securityGroupSelectorTerms:
        - id: sg-0123456789abcdef0
`

	updateSuite = `apiVersion: apiextensions.k8s.io/v1
name: OpenshiftEC2NodeClass update validation
crdName: openshiftec2nodeclasses.karpenter.hypershift.openshift.io
tests:
  onUpdate:
  - name: When the subnet changes, it should be rejected
    initial: |
      apiVersion: karpenter.hypershift.openshift.io/v1
      kind: OpenshiftEC2NodeClass
    updated: |
      apiVersion: karpenter.hypershift.openshift.io/v1
      kind: OpenshiftEC2NodeClass
`
)

func TestLoadTestSuiteSpecs(t *testing.T) {
	tests := map[string]struct {
		suite          string
		wantError      string
		wantCreateCase string
	}{
		"When the suite matches the CRD, it should load the suite": {
			suite:          createSuite,
			wantCreateCase: "When version is valid, it should create the resource",
		},
		"When the suite omits the version of a single version CRD, it should load the suite": {
			suite:          strings.Replace(createSuite, "version: v1\n", "", 1),
			wantCreateCase: "When version is valid, it should create the resource",
		},
		"When no suite files exist, it should return an error": {
			wantError: "no schema test suites found in",
		},
		"When a case has a misspelled field, it should return an error": {
			suite:     createSuite + "    expectedErorr: subnetSelectorTerms cannot be empty\n",
			wantError: "unknown field",
		},
		"When the suite has no cases, it should return an error": {
			suite:     strings.Split(createSuite, "tests:")[0] + "tests: {}\n",
			wantError: "suite has no onCreate or onUpdate cases",
		},
		"When a create case sets expected and expectedError, it should return an error": {
			suite: createSuite + "    expectedError: subnetSelectorTerms cannot be empty\n" +
				"    expected: |\n      apiVersion: karpenter.hypershift.openshift.io/v1\n      kind: OpenshiftEC2NodeClass\n",
			wantError: "expected and expectedError must not both be set",
		},
		"When an update case has no expectation, it should return an error": {
			suite:     updateSuite,
			wantError: "exactly one of expected, expectedError and expectedStatusError must be set",
		},
		"When an update case has two expectations, it should return an error": {
			suite:     updateSuite + "    expectedError: subnetSelectorTerms cannot be empty\n    expectedStatusError: status is invalid\n",
			wantError: "exactly one of expected, expectedError and expectedStatusError must be set",
		},
		"When expectedStatusError has no status payload, it should return an error": {
			suite:     updateSuite + "    expectedStatusError: status is invalid\n",
			wantError: "expectedStatusError requires updated to contain a status",
		},
		"When expectedStatusError has a null status payload, it should return an error": {
			suite:     strings.Replace(updateSuite, "    updated: |\n", "    updated: |\n      status: null\n", 1) + "    expectedStatusError: status is invalid\n",
			wantError: "expectedStatusError requires updated to contain a status",
		},
		"When a case has the wrong kind, it should return an error": {
			suite:     strings.ReplaceAll(createSuite, "kind: OpenshiftEC2NodeClass", "kind: OpenShiftAzureNodeClass"),
			wantError: `initial has kind "OpenShiftAzureNodeClass", expected "OpenshiftEC2NodeClass"`,
		},
		"When a case has the wrong apiVersion, it should return an error": {
			suite:     strings.ReplaceAll(createSuite, "apiVersion: karpenter.hypershift.openshift.io/v1", "apiVersion: karpenter.hypershift.openshift.io/v1beta1"),
			wantError: `initial has apiVersion "karpenter.hypershift.openshift.io/v1beta1", expected "karpenter.hypershift.openshift.io/v1"`,
		},
		"When the suite names a version the CRD does not have, it should return an error": {
			suite:     strings.Replace(createSuite, "version: v1\n", "version: v2\n", 1),
			wantError: `has no version "v2"`,
		},
		"When no CRD matches the crdName, it should return an error": {
			suite:     strings.Replace(createSuite, ec2CRDName, "missing.example.com", 1),
			wantError: `found 0 CRDs named "missing.example.com"`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			testsDir := writeSuite(t, tc.suite)

			suites, err := LoadTestSuiteSpecs(testsDir, crdDir)
			if tc.wantError != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tc.wantError)))
				return
			}

			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(suites).To(HaveLen(1))
			g.Expect(suites[0].Version).To(Equal("v1"))
			g.Expect(suites[0].Tests.OnCreate).To(HaveLen(1))
			g.Expect(suites[0].Tests.OnCreate[0].Name).To(Equal(tc.wantCreateCase))
		})
	}
}

// writeSuite writes the suite as tests/<crdName>/fixture.testsuite.yaml, unless it is empty, and returns the tests directory.
func writeSuite(t *testing.T, suite string) string {
	t.Helper()

	testsDir := filepath.Join(t.TempDir(), "tests")
	suiteDir := filepath.Join(testsDir, ec2CRDName)

	if err := os.MkdirAll(suiteDir, 0o750); err != nil {
		t.Fatalf("creating suite directory: %v", err)
	}

	if suite != "" {
		if err := os.WriteFile(filepath.Join(suiteDir, "fixture.testsuite.yaml"), []byte(suite), 0o600); err != nil {
			t.Fatalf("writing suite: %v", err)
		}
	}

	return testsDir
}
