package owner

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BuildRSDeployMap maps ReplicaSet name → Deployment name from RS ownerReferences.
func BuildRSDeployMap(rsOwners map[string][]metav1.OwnerReference) map[string]string {
	out := make(map[string]string, len(rsOwners))
	for rsName, owners := range rsOwners {
		if deploy, ok := controllerName(owners, "Deployment"); ok {
			out[rsName] = deploy
		}
	}
	return out
}

// ResolvePodController returns the top-level workload (Deployment / StatefulSet / DaemonSet)
// for a Pod. ReplicaSet owners are resolved via rsToDeploy when possible.
func ResolvePodController(owners []metav1.OwnerReference, rsToDeploy map[string]string) (kind, name string) {
	ref := controllingOwner(owners)
	if ref == nil {
		return "", ""
	}
	switch ref.Kind {
	case "ReplicaSet":
		if deploy, ok := rsToDeploy[ref.Name]; ok && deploy != "" {
			return "Deployment", deploy
		}
		return "ReplicaSet", ref.Name
	case "StatefulSet", "DaemonSet":
		return ref.Kind, ref.Name
	default:
		return ref.Kind, ref.Name
	}
}

func controllingOwner(owners []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range owners {
		o := &owners[i]
		if o.Controller != nil && *o.Controller {
			return o
		}
	}
	if len(owners) == 0 {
		return nil
	}
	return &owners[0]
}

func controllerName(owners []metav1.OwnerReference, kind string) (string, bool) {
	ref := controllingOwner(owners)
	if ref == nil || ref.Kind != kind || ref.Name == "" {
		return "", false
	}
	return ref.Name, true
}
