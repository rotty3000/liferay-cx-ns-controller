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

	"github.com/liferay/liferay-portal/liferay-cx-ns-controller/internal/predicatelog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// ExtensionNamespaceReconciler reconciles a Namespace object
type ExtensionNamespaceReconciler struct {
	RootReconciler
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=namespaces/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *ExtensionNamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	sourceNS := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: req.Name}, sourceNS); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Namespace not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Namespace")
		return ctrl.Result{}, err
	}

	// Check if this Namespace has the necessary prerequisite labels.
	// This is a secondary check; the predicate should be the primary filter.
	if !hasNecessaryLabel(sourceNS) {
		log.Info("Namespace does not have all necessary prerequisite labels, skipping", "namespace", sourceNS.Name)
		return ctrl.Result{}, nil
	}

	if isAlreadyManaged(sourceNS) {
		log.Info("Namespace is already managed, skipping", "namespace", sourceNS.Name)
		return ctrl.Result{}, nil
	}

	// Extract necessary information from labels
	nsObjectMeta := &sourceNS.ObjectMeta
	virtualInstanceID := getVirtualInstanceIdLabel(nsObjectMeta)   // Already confirmed to exist by hasNecessaryLabel
	managementNamespace := getManagedByResourceLabel(nsObjectMeta) // Already confirmed to exist by hasNecessaryLabel

	if managementNamespace == "" {
		// This case should ideally be caught by hasNecessaryLabel if it checks for non-empty.
		// Adding a log for safety, though hasNecessaryLabel should prevent this.
		log.Error(fmt.Errorf("management namespace label is empty"), "Prerequisite label missing or empty", "label", managedByResourceLabelKey)
		return ctrl.Result{}, nil // Don't requeue if essential info is malformed.
	}

	// Ensure Namespace labels are correct, specifically the 'managed-by' label for this controller.
	// Other critical labels (virtualInstanceId, managed-by-resource, managed-by-resource-namespace)
	// are expected to be set by the DXPMetadataConfigMapReconciler.
	currentLabels := sourceNS.GetLabels()
	if currentLabels == nil { // Should not happen if hasNecessaryLabel passed and labels existed.
		currentLabels = make(map[string]string)
	}

	labelsNeedUpdate := false
	for k, v := range desiredNamespaceLabels(nsObjectMeta, managementNamespace, virtualInstanceID) {
		if val, ok := currentLabels[k]; !ok || val != v {
			currentLabels[k] = v
			labelsNeedUpdate = true
		}
	}

	// Ensure other essential labels are as expected (they should be, due to hasNecessaryLabel and extraction logic)
	// This also ensures that if desiredNamespaceLabels were to enforce more, they'd be set.
	// For this reconciler, we primarily care about managedByLabelKey.
	// The DXPMetadataConfigMapReconciler is responsible for the initial comprehensive set.

	if labelsNeedUpdate {
		log.Info("Updating Namespace labels to ensure controller management label.", "namespace", sourceNS.Name, "targetLabels", currentLabels)
		sourceNS.SetLabels(currentLabels)
		if err := r.Update(ctx, sourceNS); err != nil {
			log.Error(err, "Failed to update Namespace labels", "namespace", sourceNS.Name)
			return ctrl.Result{}, err
		}
		log.Info("Successfully updated Namespace labels. Requeueing.", "namespace", sourceNS.Name)
	}

	// Find a ConfigMaps in the management namespace that have the label "lxc.liferay.com/metadataType: dxp" and matching label "dxp.lxc.liferay.com/virtualInstanceId"
	authoritativeSourceCM := &corev1.ConfigMap{}
	authoritativeSourceCMList := &corev1.ConfigMapList{}
	listOpts := []client.ListOption{
		client.InNamespace(managementNamespace),
		client.MatchingLabels{
			liferayMetadataTypeLabelKey:      liferayMetadataTypeLabelValue,
			liferayVirtualInstanceIdLabelKey: virtualInstanceID,
		},
	}
	if err := r.List(ctx, authoritativeSourceCMList, listOpts...); err != nil {
		log.Error(err, "Failed to list dxp metadata configmaps for virtual instance ID", "managementNamespace", managementNamespace, "virtualInstanceID", virtualInstanceID)
		return ctrl.Result{Requeue: true}, nil
	}
	if len(authoritativeSourceCMList.Items) == 0 {
		err := fmt.Errorf("empty list")
		log.Error(err, "Failed to list dxp metadata configmaps for virtual instance ID", "managementNamespace", managementNamespace, "virtualInstanceID", virtualInstanceID)
		return ctrl.Result{Requeue: true}, nil
	}

	authoritativeSourceCM = &authoritativeSourceCMList.Items[0]

	// Sync the authoritative ConfigMap to the current namespace (sourceNS).
	// The syncSourceConfigMapToNamespace function will use authoritativeSourceCM.Name as the name for the synced CM.
	if err := r.syncSourceConfigMapToNamespace(ctx, authoritativeSourceCM, sourceNS); err != nil {
		log.Error(err, "Failed to sync source ConfigMap to namespace",
			"sourceConfigMapName", authoritativeSourceCM.Name,
			"sourceConfigMapNamespace", authoritativeSourceCM.Namespace,
			"targetNamespace", sourceNS.Name)
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled Namespace and synced ConfigMap.", "namespace", sourceNS.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ExtensionNamespaceReconciler) SetupWithManager(mgr ctrl.Manager, enablePredicateLogging bool) error {
	// Predicate to filter events.
	// It will allow events if the Namespace has the necessary labels.
	var eventFilterPredicate predicate.Predicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			return hasNecessaryLabel(ns) && !isAlreadyManaged(ns)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Process if either old or new object has the labels,
			// to handle cases where labels are added or removed.
			oldNs, oldOk := e.ObjectOld.(*corev1.Namespace)
			newNs, newOk := e.ObjectNew.(*corev1.Namespace)

			process := false
			if oldOk && hasNecessaryLabel(oldNs) && !isAlreadyManaged(oldNs) {
				process = true
			}
			if newOk && hasNecessaryLabel(newNs) && !isAlreadyManaged(newNs) {
				process = true
			}

			// Additionally, if it's an update from a relevant state to another relevant state,
			// check if relevant fields (like labels themselves) changed.
			// For simplicity, if it is or was a relevant namespace, reconcile.
			// More specific checks (e.g., on resourceVersion or generation) can be added if needed.
			return process
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			return hasNecessaryLabel(ns) && !isAlreadyManaged(ns)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			return hasNecessaryLabel(ns) && !isAlreadyManaged(ns)
		},
	}

	// Use the manager's logger for the predicate logging
	predicateLogger := mgr.GetLogger().WithValues(
		"controller", extensionNamespaceControllerName,
	)
	if enablePredicateLogging {
		eventFilterPredicate = &predicatelog.LoggingPredicate{
			OriginalPredicate: eventFilterPredicate,
			Logger:            predicateLogger,
		}
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Named(extensionNamespaceControllerName).
		WithEventFilter(eventFilterPredicate).
		Complete(r)
}

func isAlreadyManaged(ns *corev1.Namespace) bool {
	labels := ns.GetLabels()
	if labels == nil {
		return false
	}

	managedBy, hasManagedByLabel := labels[managedByLabelKey]
	if !hasManagedByLabel || managedBy == "" {
		return false
	}

	return true
}

// hasNecessaryLabel checks if the Namespace has the prerequisite labels
// (virtualInstanceId and managed-by-resource-namespace) and that they are not empty.
func hasNecessaryLabel(ns *corev1.Namespace) bool {
	labels := ns.GetLabels()
	if labels == nil {
		return false
	}

	virtualInstanceID, hasVirtualInstanceIdLabel := labels[liferayVirtualInstanceIdLabelKey]
	if !hasVirtualInstanceIdLabel || virtualInstanceID == "" {
		return false
	}

	managedByResource, hasManagedByResourceLabel := labels[managedByResourceLabelKey]
	if !hasManagedByResourceLabel || managedByResource == "" {
		return false
	}

	// managedByResourceLabelKey is also crucial but checked during reconcile as its absence is an error state.
	return true
}
