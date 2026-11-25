package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SingletonName is the well-known name of the singleton Karpenter CR.
const SingletonName = "default"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=karpenters,scope=Cluster,shortName=karp
// +kubebuilder:metadata:annotations="exclude.release.openshift.io/internal-openshift-hosted=true"
// +kubebuilder:metadata:annotations="include.release.openshift.io/self-managed-high-availability=true"
// +kubebuilder:metadata:annotations="release.openshift.io/feature-gate=KarpenterOperator"

// Karpenter is the lifecycle object that deploys and manages the Karpenter
// operand on OpenShift.
type Karpenter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KarpenterSpec   `json:"spec,omitempty"`
	Status KarpenterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KarpenterList contains a list of Karpenter resources.
type KarpenterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Karpenter `json:"items"`
}

// KarpenterLogLevel is the log verbosity for the Karpenter operand.
// +kubebuilder:validation:Enum=debug;info;error
type KarpenterLogLevel string

const (
	LogLevelDebug KarpenterLogLevel = "debug"
	LogLevelInfo  KarpenterLogLevel = "info"
	LogLevelError KarpenterLogLevel = "error"
)

func (l KarpenterLogLevel) Arg() string {
	if l == "" {
		return "--log-level=" + string(LogLevelInfo)
	}
	return "--log-level=" + string(l)
}

// KarpenterSpec defines the desired state of the Karpenter operand.
type KarpenterSpec struct {
	// LogLevel is the log verbosity level. Can be one of 'debug', 'info', or 'error'.
	// +kubebuilder:default=info
	// +optional
	LogLevel KarpenterLogLevel `json:"logLevel,omitempty"`

	// AWS selects settings applied only when the operand runs on AWS.
	// +optional
	AWS *KarpenterAWSConfig `json:"aws,omitempty"`
}

// KarpenterAWSConfig configures AWS integrations for the standalone operand.
// +kubebuilder:validation:MinProperties=1
type KarpenterAWSConfig struct {
	// InterruptionQueueName names the SQS queue that receives EC2 interruption events from EventBridge.
	// When omitted, interruption handling is disabled. Use a queue dedicated to this cluster in its AWS account and region.
	// Grant the operand sqs:GetQueueUrl, sqs:ReceiveMessage and sqs:DeleteMessage on the queue and kms:Decrypt on the key if using SSE-KMS.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=80
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]+(\.fifo)?$`
	// +optional
	InterruptionQueueName string `json:"interruptionQueueName,omitempty"`
}

// KarpenterStatus defines the observed state of the Karpenter operand.
type KarpenterStatus struct {
	// Conditions represent the latest available observations of the operator's state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
