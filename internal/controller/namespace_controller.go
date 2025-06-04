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

// NamespaceReconciler reconciles a Namespace object
type NamespaceReconciler struct {
	RootReconciler
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=namespaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=namespaces/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Namespace object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("namespace", req.Name)

	sourceNS := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: req.Name}, sourceNS); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Namespace not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Namespace")
		return ctrl.Result{}, err
	}

	// Check if this Namespace has the virtualInstanceId label. If not, ignore it.
	if !hasNecessaryLabel(sourceNS) {
		log.Info("Namespace does not have the necessary labels to be managed by this controller, skipping", "namespace", sourceNS.Name)
		return ctrl.Result{}, nil
	}

	virtualInstanceID := getVirtualInstanceIdLabel(&sourceNS.ObjectMeta)
	if virtualInstanceID == "" {
		log.Info("Namespace is missing or has empty virtualInstanceId in labels, skipping", "dataKey", liferayVirtualInstanceIdLabelKey)
		return ctrl.Result{}, nil // Do not requeue if data is malformed permanently.
	}

	managementNamespace := getManagedByResourceNamespaceLabel(&sourceNS.ObjectMeta)
	if managementNamespace == "" {
		log.Info("Namespace is missing or has empty managed-by-resource-namespace in labels, skipping", "dataKey", managedByResourceNamespaceLabelKey)
		return ctrl.Result{}, nil // Do not requeue if data is malformed permanently.
	}

	// add management labels to the namespace
	if sourceNS.Labels == nil {
		sourceNS.Labels = make(map[string]string)
	}

	for k, v := range desiredNamespaceLabels(&sourceNS.ObjectMeta, managementNamespace, virtualInstanceID) {
		sourceNS.Labels[k] = v
	}

	// update the Namespace
	if err := r.Update(ctx, sourceNS); err != nil {
		log.Error(err, "Failed to update the Namespace")
		return ctrl.Result{}, err
	}

	dxpMetadataCM := &corev1.ConfigMap{}
	dxpMetadataCMName := fmt.Sprintf(`%s-lxc-dxp-metadata`, virtualInstanceID)
	if err := r.Get(ctx, client.ObjectKey{Name: dxpMetadataCMName, Namespace: sourceNS.Name}, dxpMetadataCM); err != nil {
		if apierrors.IsNotFound(err) {
			// ConfigMap not found, we need to sync it from the management namespace
			if err := r.Get(ctx, client.ObjectKey{Name: dxpMetadataCMName, Namespace: managementNamespace}, dxpMetadataCM); err != nil {
				if apierrors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
			}
		}
	}

	if err := r.syncSourceConfigMapToNamespace(ctx, dxpMetadataCM, sourceNS); err != nil {
		// log.Error(err, "Failed to sync source ConfigMap to newly created default namespace", "sourceCM", sourceCM.Name, "namespace", createdNs.Name)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager, enablePredicateLogging bool) error {

	// Predicate to filter for ConfigMaps that are Liferay Virtual Instances.
	var eventFilterPredicate predicate.Predicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			return hasNecessaryLabel(ns)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			ns, ok := e.ObjectNew.(*corev1.Namespace)
			if !ok {
				return false
			}
			return hasNecessaryLabel(ns)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			return hasNecessaryLabel(ns)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			ns, ok := e.Object.(*corev1.Namespace)
			if !ok {
				return false
			}
			return hasNecessaryLabel(ns)
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
		For(&corev1.Namespace{}).
		Named(controllerName).
		Complete(r)
}

// hasNecessaryLabel checks if the object is a Namespace that should be managed by the controller.
func hasNecessaryLabel(ns *corev1.Namespace) bool {
	labels := ns.GetLabels()
	if labels == nil {
		return false
	}

	_, hasVirtualInstanceIdLabel := labels[liferayVirtualInstanceIdLabelKey]
	_, hasManagedByResourceNamespaceLabel := labels[managedByResourceNamespaceLabelKey]

	return hasVirtualInstanceIdLabel && hasManagedByResourceNamespaceLabel
}
