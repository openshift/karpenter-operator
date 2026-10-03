package karpenter

import (
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	monitoringv1ac "github.com/prometheus-operator/prometheus-operator/pkg/client/applyconfiguration/monitoring/v1"
)

// buildPodMonitor constructs the PodMonitor for the karpenter operand metrics endpoint.
func buildPodMonitor(cfg *operandConfig, ownerRef *metaac.OwnerReferenceApplyConfiguration) *monitoringv1ac.PodMonitorApplyConfiguration {
	scheme := monitoringv1.SchemeHTTP

	return monitoringv1ac.PodMonitor(karpenterName, cfg.namespace).
		WithOwnerReferences(ownerRef).
		WithSpec(monitoringv1ac.PodMonitorSpec().
			WithSelector(metaac.LabelSelector().WithMatchLabels(map[string]string{appLabelKey: karpenterName})).
			WithPodMetricsEndpoints(
				monitoringv1ac.PodMetricsEndpoint().
					WithPort(metricsPortName).
					WithPath("/metrics").
					WithScheme(scheme),
			),
		)
}
