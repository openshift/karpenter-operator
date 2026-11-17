package runner

import (
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

func TestGetPatchedCRD(t *testing.T) {
	tests := map[string]struct {
		patches   []Patch
		wantError string
		wantScope string
	}{
		"When a replace patch targets an existing field, it should change the CRD": {
			patches:   []Patch{{Op: "replace", Path: "/spec/scope", Value: new(any("Namespaced"))}},
			wantScope: "Namespaced",
		},
		"When a patch removes a field, it should drop it from the CRD": {
			patches:   []Patch{{Op: "remove", Path: "/spec/scope"}},
			wantScope: "",
		},
		"When a replace patch targets a missing field, it should return an error": {
			patches:   []Patch{{Op: "replace", Path: "/spec/missing", Value: new(any("x"))}},
			wantError: "could not apply patch",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)

			crd, err := getPatchedCRD(filepath.Join(crdDir, "karpenter.hypershift.openshift.io_openshiftec2nodeclasses.yaml"), tc.patches)
			if tc.wantError != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tc.wantError)))
				return
			}

			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(string(crd.Spec.Scope)).To(Equal(tc.wantScope))
		})
	}
}
