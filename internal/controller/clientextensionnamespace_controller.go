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
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
	liferayVirtualInstanceIdLabelKey     = "dxp.lxc.liferay.com/virtualInstanceId"
	applicationAlias                     = "default" // Default application alias for the namespace
	syncedFromConfigMapLabelKey          = "cx.liferay.com/synced-from-configmap"
	syncedFromConfigMapNamespaceLabelKey = "cx.liferay.com/synced-from-configmap-namespace"
	managedByLabelKey                    = "app.kubernetes.io/managed-by"
	managedByResourceLabelKey            = "app.kubernetes.io/managed-by-resource"
	managedByResourceNamespaceLabelKey   = "app.kubernetes.io/managed-by-resource-namespace"
	namespaceFinalizer                   = "cx.liferay.com/namespace-protection"
	controllerName                       = "liferay-cx-ns-controller"
)

// ClientExtensionNamespaceReconciler reconciles a ConfigMap object
type ClientExtensionNamespaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
		if sourceCM.ObjectMeta.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(sourceCM, namespaceFinalizer) {
			log.Info("Removing finalizer from ConfigMap that is no longer a Liferay VI CM.")
			controllerutil.RemoveFinalizer(sourceCM, namespaceFinalizer)
			if err := r.Update(ctx, sourceCM); err != nil {
				log.Error(err, "Failed to remove finalizer from non-VI ConfigMap")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	virtualInstanceID := getVirtualInstanceIdLabel(sourceCM)
	if virtualInstanceID == "" {
		log.Info("ConfigMap is missing or has empty virtualInstanceId in labels, skipping", "dataKey", liferayVirtualInstanceIdLabelKey)
		// If it has no VI ID but has our finalizer (and not being deleted), remove the finalizer.
		if sourceCM.ObjectMeta.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(sourceCM, namespaceFinalizer) {
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
	if !sourceCM.ObjectMeta.DeletionTimestamp.IsZero() {
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
		desiredStdLabels := r.desiredNamespaceLabels(sourceCM, virtualInstanceID)
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

// syncSourceConfigMapToNamespace ensures a copy of the sourceCM exists in the targetNamespace and is up-to-date.
func (r *ClientExtensionNamespaceReconciler) syncSourceConfigMapToNamespace(ctx context.Context, sourceCM *corev1.ConfigMap, targetNamespace *corev1.Namespace) error {
	log := logf.FromContext(ctx).WithValues("targetNamespace", targetNamespace.Name, "sourceConfigMap", sourceCM.Name)

	syncedCMName := sourceCM.Name // Use the original ConfigMap name
	syncedCM := &corev1.ConfigMap{}

	err := r.Get(ctx, client.ObjectKey{Namespace: targetNamespace.Name, Name: syncedCMName}, syncedCM)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Synced ConfigMap does not exist, create it.
			log.Info("ConfigMap copy not found in target namespace, creating.", "configMapName", syncedCMName)
			newSyncedCM := r.newSyncedConfigMap(sourceCM, targetNamespace, syncedCMName)
			if err := ctrl.SetControllerReference(targetNamespace, newSyncedCM, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner reference on new synced ConfigMap")
				return err
			}
			if errCreate := r.Create(ctx, newSyncedCM); errCreate != nil {
				log.Error(errCreate, "Failed to create synced ConfigMap in target namespace")
				return errCreate
			}
			log.Info("Successfully created ConfigMap copy in target namespace.")
			return nil
		}
		// Other error getting the synced ConfigMap
		log.Error(err, "Failed to get synced ConfigMap from target namespace")
		return err
	}

	// Synced ConfigMap exists, ensure it's up-to-date.
	log.Info("ConfigMap copy found in target namespace, ensuring it is up-to-date.", "configMapName", syncedCMName)

	// Compare Data and Labels (excluding controller-specific labels like ownerRef or our own synced-from label)
	// For simplicity, we'll update if Data or specific source labels differ.
	// A more sophisticated diff could be used if needed.
	needsUpdate := false
	if !reflect.DeepEqual(sourceCM.Data, syncedCM.Data) {
		needsUpdate = true
		syncedCM.Data = sourceCM.Data
	}

	// Ensure labels from sourceCM are present (excluding potentially problematic ones or ensuring our own are set)
	// For now, let's just ensure the synced-from label is there. A full label sync might be complex.
	if syncedCM.Labels == nil {
		syncedCM.Labels = make(map[string]string)
	}
	if syncedCM.Labels[syncedFromConfigMapLabelKey] != sourceCM.Name {
		needsUpdate = true
		syncedCM.Labels[syncedFromConfigMapLabelKey] = sourceCM.Name
	}
	if syncedCM.Labels[syncedFromConfigMapNamespaceLabelKey] != sourceCM.Namespace {
		needsUpdate = true
		syncedCM.Labels[syncedFromConfigMapNamespaceLabelKey] = sourceCM.Namespace
	}
	// Also ensure it's owned by the targetNamespace
	if err := ctrl.SetControllerReference(targetNamespace, syncedCM, r.Scheme); err != nil {
		log.Error(err, "Failed to ensure owner reference on existing synced ConfigMap")
		// If owner ref fails, we might still proceed with data update or return error
	} // We'll check if this results in a change that needs an update call.

	// A more robust way to check if an update is needed after setting owner ref:
	// originalSyncedCM := syncedCM.DeepCopy() // Before SetControllerReference
	// ... set owner ref ...
	// if !reflect.DeepEqual(originalSyncedCM, syncedCM) { needsUpdate = true }

	if needsUpdate || !metav1.IsControlledBy(syncedCM, targetNamespace) { // Check if owner ref actually changed or data changed
		log.Info("Updating ConfigMap copy in target namespace.")
		if errUpdate := r.Update(ctx, syncedCM); errUpdate != nil {
			log.Error(errUpdate, "Failed to update synced ConfigMap in target namespace")
			return errUpdate
		}
		log.Info("Successfully updated ConfigMap copy in target namespace.")
	} else {
		log.Info("ConfigMap copy is already up-to-date.")
	}
	return nil
}

// newSyncedConfigMap creates a new ConfigMap object to be synced into a managed namespace.
func (r *ClientExtensionNamespaceReconciler) newSyncedConfigMap(sourceCM *corev1.ConfigMap, targetNamespace *corev1.Namespace, syncedCMName string) *corev1.ConfigMap {
	// Copy relevant labels from source. Be careful not to copy labels that might cause issues
	// or conflict with the target namespace's own management.
	// For now, we'll start with minimal labels and the synced-from indicator.
	labelsToSync := make(map[string]string)
	labelsToSync[syncedFromConfigMapLabelKey] = sourceCM.Name
	labelsToSync[syncedFromConfigMapNamespaceLabelKey] = sourceCM.Namespace

	// Add other labels from sourceCM if they are safe and desired.
	// For example, the virtualInstanceIdLabelKey itself.
	if vid := getVirtualInstanceIdLabel(sourceCM); vid != "" {
		labelsToSync[liferayVirtualInstanceIdLabelKey] = vid
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      syncedCMName, // This is sourceCM.Name passed in
			Namespace: targetNamespace.Name,
			Labels:    labelsToSync,
			// Annotations could also be synced if needed
		},
		Data: sourceCM.Data, // Copy all data
	}
}

// newNamespaceForConfigMap constructs a new Namespace object.
func (r *ClientExtensionNamespaceReconciler) newNamespaceForConfigMap(sourceCM *corev1.ConfigMap, name, virtualInstanceID string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: r.desiredNamespaceLabels(sourceCM, virtualInstanceID),
		},
	}
}

// desiredNamespaceLabels returns the set of labels that should be on the namespace.
func (r *ClientExtensionNamespaceReconciler) desiredNamespaceLabels(sourceCM *corev1.ConfigMap, virtualInstanceID string) map[string]string {
	newLabels := make(map[string]string)
	if sourceCM.Labels != nil {
		for k, v := range sourceCM.Labels {
			newLabels[k] = v
		}
	}

	newLabels[liferayVirtualInstanceIdLabelKey] = virtualInstanceID
	newLabels[managedByLabelKey] = controllerName
	newLabels[managedByResourceLabelKey] = sourceCM.Name
	newLabels[managedByResourceNamespaceLabelKey] = sourceCM.Namespace

	return newLabels
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
						if isNewVI && getVirtualInstanceIdLabel(oldCM) != getVirtualInstanceIdLabel(newCM) {
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

	if enablePredicateLogging {
		eventFilterPredicate = &predicatelog.LoggingPredicate{
			OriginalPredicate: eventFilterPredicate,
			Logger:            logf.Log.WithValues("controller", controllerName),
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

func getVirtualInstanceIdLabel(cm *corev1.ConfigMap) string {
	if cm.Labels == nil {
		return ""
	}
	return cm.Labels[liferayVirtualInstanceIdLabelKey]
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
