package predicatelog

import (
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// LoggingPredicate wraps an existing predicate and logs when it filters out an event.
type LoggingPredicate struct {
	// OriginalPredicate is the predicate whose behavior we are wrapping.
	OriginalPredicate predicate.Predicate
	// Logger is used to log information.
	// It's good practice to use the controller-runtime logger.
	Logger logr.Logger
}

// Ensure LoggingPredicate implements predicate.Predicate
var _ predicate.Predicate = &LoggingPredicate{}

// Create implements predicate.Predicate
func (lp *LoggingPredicate) Create(e event.CreateEvent) bool {
	// Call the original predicate
	res := lp.OriginalPredicate.Create(e)
	if !res {
		// Log if the original predicate returned false
		lp.logIgnoredEvent("CreateEvent", e.Object)
	}
	return res
}

// Delete implements predicate.Predicate
func (lp *LoggingPredicate) Delete(e event.DeleteEvent) bool {
	// Call the original predicate
	res := lp.OriginalPredicate.Delete(e)
	if !res {
		// Log if the original predicate returned false
		lp.logIgnoredEvent("DeleteEvent", e.Object)
	}
	return res
}

// Update implements predicate.Predicate
func (lp *LoggingPredicate) Update(e event.UpdateEvent) bool {
	// Call the original predicate
	res := lp.OriginalPredicate.Update(e)
	if !res {
		// Log if the original predicate returned false
		// We log both OldObject and NewObject for context, though typically the filter might be on NewObject.
		lp.Logger.Info(
			"Predicate returned false, ignoring event",
			"event_type", "UpdateEvent",
			"gvk_old", getGVK(e.ObjectOld),
			"namespace_old", e.ObjectOld.GetNamespace(),
			"name_old", e.ObjectOld.GetName(),
			"gvk_new", getGVK(e.ObjectNew),
			"namespace_new", e.ObjectNew.GetNamespace(),
			"name_new", e.ObjectNew.GetName(),
		)
	}
	return res
}

// Generic implements predicate.Predicate
func (lp *LoggingPredicate) Generic(e event.GenericEvent) bool {
	// Call the original predicate
	res := lp.OriginalPredicate.Generic(e)
	if !res {
		// Log if the original predicate returned false
		lp.logIgnoredEvent("GenericEvent", e.Object)
	}
	return res
}

// logIgnoredEvent is a helper function to log common details.
func (lp *LoggingPredicate) logIgnoredEvent(eventType string, obj client.Object) {
	if obj == nil {
		lp.Logger.Info(
			"Predicate returned false, ignoring event for nil object",
			"event_type", eventType,
		)
		return
	}
	lp.Logger.Info(
		"Predicate returned false, ignoring event",
		"event_type", eventType,
		"gvk", getGVK(obj),
		"namespace", obj.GetNamespace(),
		"name", obj.GetName(),
	)
}

// getGVK extracts the GroupVersionKind from a runtime.Object.
// This is a helper; in a real controller, you might have access to a Scheme
// to get a more accurate GVK.
func getGVK(obj runtime.Object) schema.GroupVersionKind {
	if obj == nil {
		return schema.GroupVersionKind{}
	}
	// ObjectKind().GroupVersionKind() is the primary way to get GVK
	gvk := obj.GetObjectKind().GroupVersionKind()
	if gvk.Kind == "" {
		// Fallback for objects that might not have GVK set directly,
		// though this is less common for objects passed to predicates.
		// In a real scenario, ensure your objects have GVK populated.
		// For example, by getting them from a scheme.
		// This is a simplified example.
		return schema.GroupVersionKind{Kind: fmt.Sprintf("%T", obj)}
	}
	return gvk
}
