package testutil

import (
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

func TestMarshalFixture(t *testing.T) {
	tests := map[string]struct {
		output   any
		expected string
	}{
		"When output is a string, it should preserve the contents": {
			output:   "kind: Deployment\n",
			expected: "kind: Deployment\n",
		},
		"When output is bytes, it should preserve the contents": {
			output:   []byte("kind: Deployment\n"),
			expected: "kind: Deployment\n",
		},
		"When output is an object, it should marshal deterministic YAML": {
			output:   map[string]string{"kind": "Deployment", "apiVersion": "apps/v1"},
			expected: "apiVersion: apps/v1\nkind: Deployment\n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			actual, err := marshalFixture(tc.output)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(actual)).To(Equal(tc.expected))
		})
	}
}

func TestFixturePath(t *testing.T) {
	tests := map[string]struct {
		testName string
		filename string
	}{
		"When the test is top level, it should use its name": {
			testName: "TestHCPDeploymentFixture",
			filename: "zz_fixture_TestHCPDeploymentFixture.yaml",
		},
		"When the test is nested, it should sanitize separators and punctuation": {
			testName: "TestHCPDeploymentFixture/When HCP uses Karpenter, it should render YAML",
			filename: "zz_fixture_TestHCPDeploymentFixture_When_HCP_uses_Karpenter_it_should_render_YAML.yaml",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(fixturePath(tc.testName)).To(Equal(filepath.Join("testdata", tc.filename)))
		})
	}
}
