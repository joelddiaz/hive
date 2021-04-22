package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hivev1 "github.com/openshift/hive/apis/hive/v1"
)

const (
	// ClusterInstallContractName is the name of cluster install contract.
	ClusterInstallContractName = "clusterinstall"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterInstall is the contract definition of various installation strategies supported
// by the ClusterDeployment.
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
type ClusterInstall struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterInstallSpec   `json:"spec,omitempty"`
	Status ClusterInstallStatus `json:"status,omitempty"`
}

// ClusterInstallSpec defines the spec contract of ClusterInstall
type ClusterInstallSpec struct {
	// ClusterDeploymentRef is a reference to the ClusterDeployment.
	// +optional
	ClusterDeploymentRef *corev1.LocalObjectReference `json:"clusterDeploymentRef"`

	// ImageSetRef is a reference to a ClusterImageSet.
	ImageSetRef hivev1.ClusterImageSetReference `json:"imageSetRef"`

	// ClusterMetadata contains metadata information about the installed cluster.
	// This must be set as soon as all the information is available.
	// +optional
	ClusterMetadata *hivev1.ClusterMetadata `json:"clusterMetadata"`
}

// ClusterInstallStatus defines the status contract of ClusterInstall
type ClusterInstallStatus struct {
	// Conditions is a list of conditions associated with syncing to the cluster.
	// +optional
	Conditions []ClusterInstallCondition `json:"conditions,omitempty"`

	// InstallRestarts is the total count of container restarts on the clusters install job.
	InstallRestarts int `json:"installRestarts,omitempty"`
}

// ClusterInstallCondition contains details for the current condition of a ClusterInstall
type ClusterInstallCondition struct {
	// Type is the type of the condition.
	Type string `json:"type"`
	// Status is the status of the condition.
	Status corev1.ConditionStatus `json:"status"`
	// LastProbeTime is the last time we probed the condition.
	// +optional
	LastProbeTime metav1.Time `json:"lastProbeTime,omitempty"`
	// LastTransitionTime is the last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// Reason is a unique, one-word, CamelCase reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Message is a human-readable message indicating details about the last transition.
	// +optional
	Message string `json:"message,omitempty"`
}

const (
	ClusterInstallFailed          = "Failed"
	ClusterInstallCompleted       = "Completed"
	ClusterInstallStopped         = "Stopped"
	ClusterInstallRequirementsMet = "RequirementsMet"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterInstallList contains a list of ClusterInstall
type ClusterInstallList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterInstall `json:"items"`
}

const (
	// ClusterInstallContractLabelKey is the label that must be set to "true" on the CRDs that
	// implements the clusterinstall contract. Only these resources will be allowed to be
	// used in Hive objects.
	ClusterInstallContractLabelKey = "contracts.hive.openshift.io/" + ClusterInstallContractName
)

func init() {
	SchemeBuilder.Register(&ClusterInstall{}, &ClusterInstallList{})
}
