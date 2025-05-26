/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// liferayMetadataTypeLabelKey is the label key to identify ConfigMaps representing Liferay Virtual Instances.
	liferayMetadataTypeLabelKey = "lxc.liferay.com/metadataType"
	// liferayMetadataTypeLabelValue is the expected value for the liferayVirtualInstanceLabelKey.
	liferayMetadataTypeLabelValue = "dxp"
	// liferayVirtualInstanceIdLabelKey is the key in ConfigMap.Data that holds the Virtual Instance ID.
	liferayVirtualInstanceIdLabelKey = "dxp.lxc.liferay.com/virtualInstanceId"
	applicationAlias                 = "cx" // Default application alias for the namespace
	managedByLabelKey                = "app.kubernetes.io/managed-by"
	controllerName                   = "liferay-cx-ns-controller"
)

// ClientExtensionNamespaceReconciler reconciles a ConfigMap object
type ClientExtensionNamespaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// RBAC for ConfigMaps: We only need to read them.
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// RBAC for Namespaces: We need to manage their lifecycle.
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;delete;update;patch
// RBAC for Events: Optional, but good for emitting events.
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClientExtensionNamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("configmap", req.NamespacedName)

	// 1. Fetch the ConfigMap instance
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, cm); err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap not found, likely deleted. Owned namespaces will be garbage collected by Kubernetes.
			log.Info("ConfigMap not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get ConfigMap")
		return ctrl.Result{}, err
	}

	// 2. Check if this ConfigMap is a Liferay Virtual Instance ConfigMap
	// This check is also performed by the predicate, but it's good for defensive programming.
	if !isLiferayVirtualInstanceCM(cm) {
		// log.V(1).Info("ConfigMap is not a Liferay Virtual Instance ConfigMap, skipping.") // Use V(1) for debug level
		return ctrl.Result{}, nil
	}

	// 3. Extract virtualInstanceId
	virtualInstanceID := getLabel(cm, liferayVirtualInstanceIdLabelKey)
	if virtualInstanceID == "" {
		log.Info("ConfigMap is missing or has empty virtualInstanceId in labels, skipping", "dataKey", liferayVirtualInstanceIdLabelKey)
		// Consider emitting a Kubernetes event here to warn the user.
		// r.Recorder.Eventf(cm, corev1.EventTypeWarning, "MissingLabel", "ConfigMap %s/%s is missing virtualInstanceId", cm.Namespace, cm.Name)
		return ctrl.Result{}, nil // Do not requeue if data is malformed permanently.
	}

	log = log.WithValues("virtualInstanceID", virtualInstanceID)

	// 4. Construct the desired Namespace name
	namespaceName := fmt.Sprintf("%s-%s", virtualInstanceID, applicationAlias)
	log = log.WithValues("targetNamespace", namespaceName)

	// 5. Check if the Namespace already exists
	namespace := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: namespaceName}, namespace)

	if err != nil {
		if errors.IsNotFound(err) {
			// Namespace does not exist, create it
			log.Info("Target namespace not found, creating new one.")
			ns := r.newNamespaceForConfigMap(namespaceName, virtualInstanceID)
			if err := ctrl.SetControllerReference(cm, ns, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner reference on new Namespace")
				return ctrl.Result{}, err
			}

			log.Info("Creating Namespace", "namespace", ns.Name)
			if err := r.Create(ctx, ns); err != nil {
				log.Error(err, "Failed to create Namespace")
				return ctrl.Result{}, err
			}
			log.Info("Successfully created Namespace")
			return ctrl.Result{}, nil // No need to requeue immediately, Owns() will trigger if needed.
		}
		// Some other error occurred when trying to get the Namespace
		log.Error(err, "Failed to get Namespace")
		return ctrl.Result{}, err
	}

	// 6. Namespace already exists. Ensure it's correctly owned and labeled.
	log.Info("Target namespace already exists. Ensuring owner reference and labels.")

	// Make a copy to compare for changes later to avoid unnecessary updates
	existingNamespace := namespace.DeepCopy()
	needsUpdate := false

	// Ensure OwnerReference
	// ctrl.SetControllerReference is idempotent.
	if err := ctrl.SetControllerReference(cm, namespace, r.Scheme); err != nil {
		log.Error(err, "Failed to ensure owner reference on existing Namespace")
		return ctrl.Result{}, err
	}
	if !metav1.IsControlledBy(namespace, cm) { // A more explicit check
		// This condition might be redundant if SetControllerReference always makes it controlled,
		// but helps in understanding if an update is truly because of ownership.
		// For simplicity, we rely on comparing the whole object later.
	}

	// Ensure labels
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
	}
	desiredLabels := r.desiredNamespaceLabels(virtualInstanceID)
	for k, v := range desiredLabels {
		if namespace.Labels[k] != v {
			namespace.Labels[k] = v
		}
	}

	// Check if an update is actually needed by comparing the modified object with its original state.
	// This avoids unnecessary API calls if SetControllerReference or label setting didn't change anything.
	// A simple way is to compare relevant parts or use a deep equal if performance is not critical for this comparison.
	// For now, let's assume if we went through the motions, an update might be needed if anything changed.
	// A more robust check would be a deep equal of existingNamespace and namespace.
	// For simplicity, if owner references or labels could have changed, we'll try an update.
	// Let's refine this: only update if ownerReferences or labels actually changed.

	if !ownerReferencesDeepEqual(existingNamespace.OwnerReferences, namespace.OwnerReferences) ||
		!labelsDeepEqual(existingNamespace.Labels, namespace.Labels) {
		needsUpdate = true
	}

	if needsUpdate {
		log.Info("Updating existing Namespace due to owner reference or label changes.")
		if err := r.Update(ctx, namespace); err != nil {
			log.Error(err, "Failed to update existing Namespace")
			return ctrl.Result{}, err
		}
		log.Info("Successfully updated existing Namespace")
	} else {
		log.Info("Namespace is already in the desired state.")
	}

	return ctrl.Result{}, nil
}

// newNamespaceForConfigMap constructs a new Namespace object.
func (r *ClientExtensionNamespaceReconciler) newNamespaceForConfigMap(name, virtualInstanceID string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: r.desiredNamespaceLabels(virtualInstanceID),
		},
	}
}

// desiredNamespaceLabels returns the set of labels that should be on the namespace.
func (r *ClientExtensionNamespaceReconciler) desiredNamespaceLabels(virtualInstanceID string) map[string]string {
	return map[string]string{
		liferayVirtualInstanceIdLabelKey: virtualInstanceID,
		managedByLabelKey:                controllerName,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClientExtensionNamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Predicate to filter for ConfigMaps that are Liferay Virtual Instances.
	liferayVICMPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return isLiferayVirtualInstanceCM(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Reconcile if the new object is a Liferay VI CM,
			// or if the old one was and the new one isn't (e.g. label removed),
			// or if relevant data like virtualInstanceId changed within a VI CM.
			isNewVI := false
			if e.ObjectNew != nil {
				isNewVI = isLiferayVirtualInstanceCM(e.ObjectNew)
			}
			isOldVI := false
			if e.ObjectOld != nil {
				isOldVI = isLiferayVirtualInstanceCM(e.ObjectOld)
			}
			// Process if it is or was a Liferay VI CM.
			// Also consider if data changed for an existing VI CM.
			if isNewVI || isOldVI {
				if e.ObjectOld != nil && e.ObjectNew != nil {
					oldCM, okOld := e.ObjectOld.(*corev1.ConfigMap)
					newCM, okNew := e.ObjectNew.(*corev1.ConfigMap)
					if okOld && okNew {
						// If it's still a VI CM, check if the ID changed
						if isNewVI && getLabel(oldCM, liferayVirtualInstanceIdLabelKey) != getLabel(newCM, liferayVirtualInstanceIdLabelKey) {
							return true // ID changed, reconcile
						}
					}
				}
				return true // Label change or it's a relevant CM
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// When a ConfigMap is deleted, owner references handle Namespace cleanup.
			// Reconciling on delete of the CM itself is useful if we had finalizers on the CM.
			// For this controller, the OwnerReference on Namespace handles deletion of Namespace.
			// We return true to allow the reconcile loop to see the CM is gone.
			return isLiferayVirtualInstanceCM(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return isLiferayVirtualInstanceCM(e.Object)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Owns(&corev1.Namespace{}). // Watch for Namespace events that this controller owns
		WithEventFilter(liferayVICMPredicate).
		// Named("clientextensionnamespace") // Optional: give the controller a specific name for metrics/logging
		Complete(r)
}

func getLabel(cm *corev1.ConfigMap, key string) string {
	if cm.Labels == nil {
		return ""
	}
	return cm.Labels[key]
}

// isLiferayVirtualInstanceCM checks if the object is a ConfigMap representing a Liferay Virtual Instance.
func isLiferayVirtualInstanceCM(obj client.Object) bool {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return false // Not a ConfigMap
	}

	labels := cm.GetLabels()
	if labels == nil {
		return false
	}

	return labels[liferayMetadataTypeLabelKey] == liferayMetadataTypeLabelValue
}

// ownerReferencesDeepEqual checks if two slices of OwnerReference are semantically equal.
func ownerReferencesDeepEqual(expected, actual []metav1.OwnerReference) bool {
	if len(expected) != len(actual) {
		return false
	}
	// This is a simplified check. For a truly robust check, you might need to
	// sort them or use a library that handles unordered slice comparison if order doesn't matter.
	// However, for owner references, the list is usually small and order might be preserved by SetControllerReference.
	// For this controller, we primarily care that *our* controller reference is present.
	// metav1.IsControlledBy is a good check for a specific owner.
	// A full deep equal is more complex if order can vary.
	// Let's assume order is consistent or check for presence of each expected ref in actual.
	for _, expRef := range expected {
		found := false
		for _, actRef := range actual {
			if expRef.APIVersion == actRef.APIVersion &&
				expRef.Kind == actRef.Kind &&
				expRef.Name == actRef.Name &&
				expRef.UID == actRef.UID &&
				(expRef.Controller == nil && actRef.Controller == nil || (expRef.Controller != nil && actRef.Controller != nil && *expRef.Controller == *actRef.Controller)) &&
				(expRef.BlockOwnerDeletion == nil && actRef.BlockOwnerDeletion == nil || (expRef.BlockOwnerDeletion != nil && actRef.BlockOwnerDeletion != nil && *expRef.BlockOwnerDeletion == *actRef.BlockOwnerDeletion)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// labelsDeepEqual checks if two label maps are semantically equal.
func labelsDeepEqual(expected, actual map[string]string) bool {
	if len(expected) != len(actual) {
		// This check is only valid if we expect 'actual' to not have extra labels.
		// If 'actual' can have more labels than 'expected', this check is too strict.
		// For ensuring 'expected' labels are present and correct:
		// return false
	}
	for k, v := range expected {
		if actualVal, ok := actual[k]; !ok || actualVal != v {
			return false
		}
	}
	// If we also want to ensure no extra labels in actual:
	if len(expected) != len(actual) && actual != nil { // Check actual != nil for the case where expected is empty
		for k := range actual {
			if _, ok := expected[k]; !ok {
				return false // actual has a key not in expected
			}
		}
	}
	return true
}
