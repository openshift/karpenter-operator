package karpenter

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestBuildPodSpecInvalidSecurityContextUID(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		uid     int64
		wantErr string
	}{
		"When a root UID is supplied, it should reject the pod configuration": {
			uid:     0,
			wantErr: "UID must be positive",
		},
		"When a negative UID is supplied, it should reject the pod configuration": {
			uid:     -1,
			wantErr: "UID must be positive",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)
			pod, err := buildPodSpec(&operandConfig{
				cloudProvider:             hcpFakeCloudProvider,
				defaultSecurityContextUID: &tc.uid,
			})
			g.Expect(err).To(MatchError(ContainSubstring(tc.wantErr)))
			g.Expect(pod).To(BeNil())
		})
	}
}
