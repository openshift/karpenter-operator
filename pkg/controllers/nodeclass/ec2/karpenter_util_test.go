package ec2nodeclass

import (
	"reflect"
	"testing"
	"time"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestKarpenterKubeletConfigurationFromNodeClassSpec(t *testing.T) {
	tests := map[string]struct {
		spec     openshiftkarpenterv1.OpenshiftEC2NodeClassSpec
		expected *awskarpenterv1.KubeletConfiguration
	}{
		"When Kubelet is nil, it should return nil": {
			spec:     openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{},
			expected: nil,
		},
		"When all karpenter-mapped fields are set, it should map them": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Kubelet: openshiftkarpenterv1.KubeletConfiguration{
					MaxPods:     110,
					PodsPerCore: 10,
					SystemReserved: map[string]string{
						"cpu":    "100m",
						"memory": "256Mi",
					},
					KubeReserved: map[string]string{
						"cpu":    "200m",
						"memory": "512Mi",
					},
					EvictionHard: map[string]openshiftkarpenterv1.EvictionThreshold{
						"memory.available": "100Mi",
					},
					EvictionSoft: map[string]openshiftkarpenterv1.EvictionThreshold{
						"memory.available": "200Mi",
					},
					EvictionSoftGracePeriod: map[string]string{
						"memory.available": "30s",
					},
					EvictionMaxPodGracePeriod:   new(int32(60)),
					ImageGCHighThresholdPercent: new(int32(85)),
					ImageGCLowThresholdPercent:  new(int32(80)),
					CPUCFSQuota:                 new(true),
				},
			},
			expected: &awskarpenterv1.KubeletConfiguration{
				MaxPods:     new(int32(110)),
				PodsPerCore: new(int32(10)),
				SystemReserved: map[string]string{
					"cpu":    "100m",
					"memory": "256Mi",
				},
				KubeReserved: map[string]string{
					"cpu":    "200m",
					"memory": "512Mi",
				},
				EvictionHard: map[string]string{
					"memory.available": "100Mi",
				},
				EvictionSoft: map[string]string{
					"memory.available": "200Mi",
				},
				EvictionSoftGracePeriod: map[string]metav1.Duration{
					"memory.available": {Duration: 30 * time.Second},
				},
				EvictionMaxPodGracePeriod:   new(int32(60)),
				ImageGCHighThresholdPercent: new(int32(85)),
				ImageGCLowThresholdPercent:  new(int32(80)),
				CPUCFSQuota:                 new(true),
			},
		},
		"When only some fields are set, it should map only those": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Kubelet: openshiftkarpenterv1.KubeletConfiguration{
					MaxPods: 50,
				},
			},
			expected: &awskarpenterv1.KubeletConfiguration{
				MaxPods: new(int32(50)),
			},
		},
		"When only overflow fields are set, it should return nil": {
			spec: openshiftkarpenterv1.OpenshiftEC2NodeClassSpec{
				Kubelet: openshiftkarpenterv1.KubeletConfiguration{
					Overflow: runtime.RawExtension{Raw: []byte(`{"podPidsLimit":4096}`)},
				},
			},
			expected: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			result := karpenterKubeletConfigurationFromNodeClassSpec(tc.spec)
			if !reflect.DeepEqual(result, tc.expected) {
				t.Errorf("expected %+v, got %+v", tc.expected, result)
			}
		})
	}
}
