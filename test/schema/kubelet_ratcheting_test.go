//go:build envtest

package schema

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/yaml"
)

const kubeletRatchetingSuiteFilename = "stable.openshiftec2nodeclasses.kubelet-ratcheting.testsuite.yaml"

// TestKubeletRatchetingSuiteCoversTypedFields guards the hand-written kubelet ratcheting suite:
// every typed KubeletConfiguration field must be set in the initial, updated and expected objects,
// otherwise the suite silently stops covering that field.
func TestKubeletRatchetingSuiteCoversTypedFields(t *testing.T) {
	g := NewWithT(t)

	_, thisFile, _, ok := runtime.Caller(0)
	g.Expect(ok).To(BeTrue(), "could not determine the path of the test file")
	suitePath := filepath.Join(filepath.Dir(thisFile), "..", "..", "pkg", "assets", "hypershift", "tests",
		"openshiftec2nodeclasses.karpenter.hypershift.openshift.io", kubeletRatchetingSuiteFilename)

	raw, err := os.ReadFile(suitePath)
	g.Expect(err).ToNot(HaveOccurred())

	var suite SuiteSpec
	g.Expect(yaml.UnmarshalStrict(raw, &suite)).To(Succeed())
	g.Expect(suite.Tests.OnUpdate).To(HaveLen(1), "the kubelet ratcheting suite must have exactly one onUpdate case")

	update := suite.Tests.OnUpdate[0]
	fields := typedKubeletFields()

	for _, document := range []string{update.Initial, update.Updated, update.Expected} {
		kubelet := kubeletOf(g, document)

		var missing []string
		for _, field := range fields {
			if _, found := kubelet[field]; !found {
				missing = append(missing, field)
			}
		}
		g.Expect(missing).To(BeEmpty(), "%s is missing typed kubelet fields in a document: %s",
			kubeletRatchetingSuiteFilename, strings.Join(missing, ", "))
	}
}

// typedKubeletFields returns the JSON names of the typed KubeletConfiguration fields,
// mirroring kubeletConfigKnownFields in api/karpenter/v1/kubelet_config.go.
func typedKubeletFields() []string {
	t := reflect.TypeFor[openshiftkarpenterv1.KubeletConfiguration]()

	var fields []string
	for field := range t.Fields() {
		tag, ok := field.Tag.Lookup("json")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	slices.Sort(fields)

	return fields
}

// kubeletOf parses a suite document and returns its spec.kubelet map.
func kubeletOf(g *WithT, document string) map[string]any {
	var obj map[string]any
	g.Expect(yaml.Unmarshal([]byte(document), &obj)).To(Succeed())

	kubelet, found, err := unstructured.NestedMap(obj, "spec", "kubelet")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(found).To(BeTrue(), "document has no spec.kubelet")

	return kubelet
}
