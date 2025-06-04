package controller

import (
	"context"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
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
	namespaceFinalizer                   = "cx.liferay.com/namespace-protection"
	controllerGroupName                  = "cx-liferay-controller-group"
	dxpMetadataConfigMapControllerName   = "dxp-metadata-configmap-controller"
	extensionNamespaceControllerName     = "extension-namespace-controller"
)

type RootReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// desiredNamespaceLabels returns the set of labels that should be on the namespace.
func desiredNamespaceLabels(source *metav1.ObjectMeta, managementNamespace, virtualInstanceID string) map[string]string {
	newLabels := make(map[string]string)
	if source.Labels != nil {
		for k, v := range source.Labels {
			newLabels[k] = v
		}
	}

	newLabels[liferayVirtualInstanceIdLabelKey] = virtualInstanceID
	newLabels[managedByLabelKey] = controllerGroupName
	newLabels[managedByResourceLabelKey] = managementNamespace

	return newLabels
}

func getManagedByResourceLabel(source *metav1.ObjectMeta) string {
	if source.Labels == nil {
		return ""
	}
	return source.Labels[managedByResourceLabelKey]
}

func getVirtualInstanceIdLabel(source *metav1.ObjectMeta) string {
	if source.Labels == nil {
		return ""
	}
	return source.Labels[liferayVirtualInstanceIdLabelKey]
}

// syncSourceConfigMapToNamespace ensures a copy of the sourceCM exists in the targetNamespace and is up-to-date.
func (r *RootReconciler) syncSourceConfigMapToNamespace(ctx context.Context, sourceCM *corev1.ConfigMap, targetNamespace *corev1.Namespace) error {
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
func (r *RootReconciler) newSyncedConfigMap(sourceCM *corev1.ConfigMap, targetNamespace *corev1.Namespace, syncedCMName string) *corev1.ConfigMap {
	// Copy relevant labels from source. Be careful not to copy labels that might cause issues
	// or conflict with the target namespace's own management.
	// For now, we'll start with minimal labels and the synced-from indicator.
	labelsToSync := make(map[string]string)
	labelsToSync[syncedFromConfigMapLabelKey] = sourceCM.Name
	labelsToSync[syncedFromConfigMapNamespaceLabelKey] = sourceCM.Namespace

	// Add other labels from sourceCM if they are safe and desired.
	// For example, the virtualInstanceIdLabelKey itself.
	if vid := getVirtualInstanceIdLabel(&sourceCM.ObjectMeta); vid != "" {
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
