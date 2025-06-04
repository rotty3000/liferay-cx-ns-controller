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
	"reflect"

	"github.com/go-logr/logr"
	"github.com/liferay/liferay-portal/liferay-cx-ns-controller/internal/predicatelog"
	"github.com/liferay/liferay-portal/liferay-cx-ns-controller/internal/utils"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// ClientExtensionNamespaceReconciler reconciles a ConfigMap object
type ClientExtensionNamespaceReconciler struct {
	RootReconciler
}

// RBAC for ConfigMaps: We only need to read them.
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// Note: Delete permission for ConfigMaps might be needed if we decide to clean up synced CMs explicitly, but OwnerReferences should handle it.
// RBAC for Namespaces: We need to manage their lifecycle.
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;delete;update;patch
// RBAC for Events: Optional, but good for emitting events.
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClientExtensionNamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("configmap", req.NamespacedName)

	sourceCM := &corev1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, sourceCM); err != nil {
		if apierrors.IsNotFound(err) {
			// ConfigMap not found, likely deleted.
			log.Info("ConfigMap not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get ConfigMap")
		return ctrl.Result{}, err
	}

	// Check if this ConfigMap is a synced copy. If so, ignore it.
	if isSyncedConfigMap(sourceCM) {
		log.Info("ConfigMap is a synced copy, skipping", "configMapName", sourceCM.Name, "namespace", sourceCM.Namespace)
		return ctrl.Result{}, nil
	}

	// Check if this ConfigMap is a Liferay Virtual Instance ConfigMap
	if !isLiferayVirtualInstanceCM(sourceCM) {
		log.V(1).Info("ConfigMap is not a Liferay Virtual Instance ConfigMap.")
		// If it's not a VI CM but has our finalizer (and not being deleted), remove the finalizer.
		if sourceCM.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(sourceCM, namespaceFinalizer) {
			log.Info("Removing finalizer from ConfigMap that is no longer a Liferay VI CM.")
			controllerutil.RemoveFinalizer(sourceCM, namespaceFinalizer)
			if err := r.Update(ctx, sourceCM); err != nil {
				log.Error(err, "Failed to remove finalizer from non-VI ConfigMap")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	virtualInstanceID := getVirtualInstanceIdLabel(&sourceCM.ObjectMeta)
	if virtualInstanceID == "" {
		log.Info("ConfigMap is missing or has empty virtualInstanceId in labels, skipping", "dataKey", liferayVirtualInstanceIdLabelKey)
		// If it has no VI ID but has our finalizer (and not being deleted), remove the finalizer.
		if sourceCM.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(sourceCM, namespaceFinalizer) {
			log.Info("ConfigMap has no virtualInstanceID but has finalizer. Removing finalizer.")
			controllerutil.RemoveFinalizer(sourceCM, namespaceFinalizer)
			if err := r.Update(ctx, sourceCM); err != nil {
				log.Error(err, "Failed to remove finalizer from ConfigMap with no virtualInstanceID")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil // Do not requeue if data is malformed permanently.
	}

	log = log.WithValues("virtualInstanceID", virtualInstanceID)

	// Handle deletion of the source ConfigMap
	if !sourceCM.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(sourceCM, namespaceFinalizer) {
			log.Info("ConfigMap is being deleted, performing cleanup of associated Namespaces")

			if err := r.cleanupAssociatedNamespaces(ctx, sourceCM, virtualInstanceID, log); err != nil {
				log.Error(err, "Failed to cleanup associated Namespaces during ConfigMap deletion")
				return ctrl.Result{}, err // Requeue to retry cleanup
			}

			log.Info("Namespace cleanup successful, removing finalizer from ConfigMap")
			controllerutil.RemoveFinalizer(sourceCM, namespaceFinalizer)
			if err := r.Update(ctx, sourceCM); err != nil {
				log.Error(err, "Failed to remove finalizer from ConfigMap")
				return ctrl.Result{}, err
			}
			log.Info("Finalizer removed from ConfigMap")
		}
		return ctrl.Result{}, nil // Stop processing if it's being deleted
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(sourceCM, namespaceFinalizer) {
		log.Info("Adding finalizer to ConfigMap", "finalizer", namespaceFinalizer)
		controllerutil.AddFinalizer(sourceCM, namespaceFinalizer)
		if err := r.Update(ctx, sourceCM); err != nil {
			log.Error(err, "Failed to add finalizer to ConfigMap")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil // Requeue to ensure CM is updated before proceeding
	}

	// Construct the desired Namespace name
	defaultNamespaceName, err := utils.VirtualInstanceIdToNamespace(sourceCM.Namespace, virtualInstanceID, applicationAlias)
	if err != nil {
		log.Error(err, "Failed to construct desired Namespace name", "virtualInstanceID", virtualInstanceID, "applicationAlias", applicationAlias, "namespaceName", defaultNamespaceName)
		return ctrl.Result{}, err
	}
	log = log.WithValues("defaultTargetNamespace", defaultNamespaceName)

	// List all Namespaces associated with this Virtual Instance ID (managed by this controller)
	namespaceList := &corev1.NamespaceList{}
	listOpts := []client.ListOption{
		client.MatchingLabels{liferayVirtualInstanceIdLabelKey: virtualInstanceID},
	}
	if err := r.List(ctx, namespaceList, listOpts...); err != nil {
		log.Error(err, "Failed to list namespaces for virtual instance ID")
		return ctrl.Result{}, err
	}

	defaultNamespaceFound := false
	for i := range namespaceList.Items {
		nsToUpdate := &namespaceList.Items[i]
		logForNs := log.WithValues("namespace", nsToUpdate.Name)

		// Store original labels for comparison
		originalLabels := make(map[string]string)
		if nsToUpdate.Labels != nil {
			for k, v := range nsToUpdate.Labels {
				originalLabels[k] = v
			}
		}

		// Ensure standard labels are present and correct (preserve other existing labels)
		if nsToUpdate.Labels == nil {
			nsToUpdate.Labels = make(map[string]string)
		}
		desiredStdLabels := desiredNamespaceLabels(&sourceCM.ObjectMeta, sourceCM.Namespace, virtualInstanceID)
		for k, v := range desiredStdLabels {
			nsToUpdate.Labels[k] = v
		}

		// Check if an update to the Namespace object is actually needed
		labelsChanged := !reflect.DeepEqual(originalLabels, nsToUpdate.Labels)

		if labelsChanged {
			if labelsChanged {
				logForNs.Info("Updating existing Namespace: labels changed.")
			}
			if err := r.Update(ctx, nsToUpdate); err != nil {
				logForNs.Error(err, "Failed to update existing Namespace")
				return ctrl.Result{}, err
			}
			logForNs.Info("Successfully updated existing Namespace")
		} else {
			logForNs.Info("Existing Namespace is already in the desired state regarding labels.")
		}

		if nsToUpdate.Name == defaultNamespaceName {
			defaultNamespaceFound = true
		}

		// Sync the sourceCM into this managed namespace (nsToUpdate)
		if err := r.syncSourceConfigMapToNamespace(ctx, sourceCM, nsToUpdate); err != nil {
			logForNs.Error(err, "Failed to sync source ConfigMap to managed namespace", "sourceCM", sourceCM.Name)
			return ctrl.Result{}, err // Requeue on error
		}
	}

	// If the default namespace was not found among the labeled namespaces, create it.
	if !defaultNamespaceFound {
		log.Info("Default target namespace not found, creating new one.")
		ns := r.newNamespaceForConfigMap(sourceCM, defaultNamespaceName, virtualInstanceID)
		// DO NOT set OwnerReference from sourceCM to ns
		log.Info("Creating default Namespace")
		if err := r.Create(ctx, ns); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// This can happen if another reconcile loop or process created it just now.
				log.Info("Default namespace was created concurrently, will reconcile again.")
				return ctrl.Result{Requeue: true}, nil
			}
			log.Error(err, "Failed to create default Namespace")
			return ctrl.Result{}, err
		}
		log.Info("Successfully created default Namespace", "createdNamespace", ns.Name)
		// After creating the default namespace, sync the sourceCM to it as well.
		// We need to fetch the newly created namespace object to set owner reference correctly for the synced CM.
		createdNs := &corev1.Namespace{}
		if err := r.Get(ctx, client.ObjectKey{Name: ns.Name}, createdNs); err != nil {
			log.Error(err, "Failed to get newly created default namespace for syncing CM", "namespace", ns.Name)
			return ctrl.Result{}, err
		}
		if err := r.syncSourceConfigMapToNamespace(ctx, sourceCM, createdNs); err != nil {
			log.Error(err, "Failed to sync source ConfigMap to newly created default namespace", "sourceCM", sourceCM.Name, "namespace", createdNs.Name)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// cleanupAssociatedNamespaces deletes all namespaces managed by this controller for a given virtualInstanceID.
func (r *ClientExtensionNamespaceReconciler) cleanupAssociatedNamespaces(ctx context.Context, cm *corev1.ConfigMap, virtualInstanceID string, log logr.Logger) error {
	log.Info("Listing namespaces for cleanup", "virtualInstanceID", virtualInstanceID)
	namespaceList := &corev1.NamespaceList{}
	listOpts := []client.ListOption{
		client.MatchingLabels{
			liferayVirtualInstanceIdLabelKey:   virtualInstanceID,
			managedByLabelKey:                  controllerName,
			managedByResourceLabelKey:          cm.Name,
			managedByResourceNamespaceLabelKey: cm.Namespace,
		},
	}

	if err := r.List(ctx, namespaceList, listOpts...); err != nil {
		log.Error(err, "Failed to list namespaces for cleanup")
		return err
	}

	if len(namespaceList.Items) == 0 {
		log.Info("No namespaces found for cleanup associated with this virtualInstanceID")
		return nil
	}

	var errs []error
	for _, ns := range namespaceList.Items {
		nsToDelete := ns // Use a new variable for the loop scope
		log.Info("Deleting namespace", "namespaceName", nsToDelete.Name)
		if err := r.Delete(ctx, &nsToDelete); err != nil {
			if !apierrors.IsNotFound(err) { // Ignore not found errors
				log.Error(err, "Failed to delete namespace", "namespaceName", nsToDelete.Name)
				errs = append(errs, err)
			}
		}
	}

	if len(errs) > 0 {
		// Consider using k8s.io/apimachinery/pkg/util/errors.NewAggregate(errs) for multiple errors
		return errs[0] // Return the first error for simplicity
	}

	// Optionally, re-list to confirm deletion before returning success.
	// For now, we assume delete calls will eventually succeed or are already gone.
	log.Info("All associated namespaces have been requested for deletion.")
	return nil
}

// newNamespaceForConfigMap constructs a new Namespace object.
func (r *ClientExtensionNamespaceReconciler) newNamespaceForConfigMap(sourceCM *corev1.ConfigMap, name, virtualInstanceID string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: desiredNamespaceLabels(&sourceCM.ObjectMeta, sourceCM.Namespace, virtualInstanceID),
		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClientExtensionNamespaceReconciler) SetupWithManager(mgr ctrl.Manager, enablePredicateLogging bool) error {
	// Predicate to filter events.
	// It will allow events if the ConfigMap:
	// 1. Is a Liferay Virtual Instance CM (has the liferayMetadataTypeLabelKey).
	// 2. Is NOT a synced copy (does NOT have the syncedFromConfigMapLabelKey).

	// Predicate to filter for ConfigMaps that are Liferay Virtual Instances.
	var eventFilterPredicate predicate.Predicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			cm, ok := e.Object.(*corev1.ConfigMap)
			if !ok {
				return false
			}
			return isLiferayVirtualInstanceCM(cm) && !isSyncedConfigMap(cm)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Reconcile if the new object is a Liferay VI CM,
			// or if the old one was and the new one isn't (e.g. label removed),
			// or if relevant data like virtualInstanceId changed within a VI CM.
			isNewVI := false
			if e.ObjectNew != nil {
				cmNew, ok := e.ObjectNew.(*corev1.ConfigMap)
				if ok {
					isNewVI = isLiferayVirtualInstanceCM(cmNew) && !isSyncedConfigMap(cmNew)
				}
			}
			isOldVI := false
			if e.ObjectOld != nil {
				cmOld, ok := e.ObjectOld.(*corev1.ConfigMap)
				if ok {
					isOldVI = isLiferayVirtualInstanceCM(cmOld) && !isSyncedConfigMap(cmOld)
				}
			}
			// Process if it is or was a Liferay VI CM.
			// Also consider if data changed for an existing VI CM.
			if isNewVI || isOldVI {
				if e.ObjectOld != nil && e.ObjectNew != nil {
					oldCM, okOld := e.ObjectOld.(*corev1.ConfigMap)
					newCM, okNew := e.ObjectNew.(*corev1.ConfigMap)
					if okOld && okNew {
						// If it's still a VI CM, check if the ID changed
						if isNewVI && getVirtualInstanceIdLabel(&oldCM.ObjectMeta) != getVirtualInstanceIdLabel(&newCM.ObjectMeta) {
							return true // ID changed, reconcile
						}
					}
				}
				return true // Label change or it's a relevant CM
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// We return true to allow the reconcile loop to see the CM is gone.
			cm, ok := e.Object.(*corev1.ConfigMap)
			if !ok {
				return false
			}
			return isLiferayVirtualInstanceCM(cm) && !isSyncedConfigMap(cm)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			cm, ok := e.Object.(*corev1.ConfigMap)
			if !ok {
				return false
			}
			return isLiferayVirtualInstanceCM(cm) && !isSyncedConfigMap(cm)
		},
	}

	// Use the manager's logger for the predicate logging
	predicateLogger := mgr.GetLogger().WithValues("predicate_controller", controllerName)
	if enablePredicateLogging {
		eventFilterPredicate = &predicatelog.LoggingPredicate{
			OriginalPredicate: eventFilterPredicate,
			Logger:            predicateLogger,
		}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		// We don't use Owns(&corev1.Namespace{}) because the CM no longer owns the Namespace.
		// We also don't need Owns(&corev1.ConfigMap{}) for synced CMs, as their lifecycle is managed by the source CM's reconcile loop.
		WithEventFilter(eventFilterPredicate).
		Named(controllerName).
		Complete(r)
}

// isLiferayVirtualInstanceCM checks if the object is a ConfigMap representing a Liferay Virtual Instance.
func isLiferayVirtualInstanceCM(cm *corev1.ConfigMap) bool {
	labels := cm.GetLabels()
	if labels == nil {
		return false
	}

	return labels[liferayMetadataTypeLabelKey] == liferayMetadataTypeLabelValue
}

// isSyncedConfigMap checks if the ConfigMap is a synced copy.
func isSyncedConfigMap(cm *corev1.ConfigMap) bool {
	if cm == nil || cm.Labels == nil {
		return false
	}
	_, ok := cm.Labels[syncedFromConfigMapLabelKey]
	return ok
}
