package manifestworkreplicasetcontroller

import (
	"context"
	"fmt"
	"strings"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	clusterinformerv1beta1 "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster/v1beta1"
	workclientset "open-cluster-management.io/api/client/work/clientset/versioned"
	workinformerv1 "open-cluster-management.io/api/client/work/informers/externalversions/work/v1"
	workinformerv1alpha1 "open-cluster-management.io/api/client/work/informers/externalversions/work/v1alpha1"
	worklisterv1 "open-cluster-management.io/api/client/work/listers/work/v1"
	worklisterv1alpha1 "open-cluster-management.io/api/client/work/listers/work/v1alpha1"
	"open-cluster-management.io/api/utils/work/v1/workapplier"
	workapiv1 "open-cluster-management.io/api/work/v1"
	workapiv1alpha1 "open-cluster-management.io/api/work/v1alpha1"

	"open-cluster-management.io/ocm/pkg/common/patcher"
)

const (
	// ManifestWorkReplicaSetControllerNameLabelKey is the label key on manifestwork to ref to the ManifestWorkReplicaSet
	// that owns this manifestwork
	// TODO move this to the api repo
	ManifestWorkReplicaSetControllerNameLabelKey = "work.open-cluster-management.io/manifestworkreplicaset"

	// ManifestWorkReplicaSetFinalizer is the name of the finalizer added to ManifestWorkReplicaSet. It is used to ensure
	// related manifestworks is deleted
	ManifestWorkReplicaSetFinalizer = "work.open-cluster-management.io/manifest-work-cleanup"
)

type ManifestWorkReplicaSetController struct {
	workClient                    workclientset.Interface
	manifestWorkReplicaSetLister  worklisterv1alpha1.ManifestWorkReplicaSetLister
	manifestWorkReplicaSetIndexer cache.Indexer

	reconcilers []ManifestWorkReplicaSetReconcile
}

// manifestWorkReplicaSetReconcile is a interface for reconcile logic. It returns an updated manifestWorkReplicaSet and whether further
// reconcile needs to proceed.
type ManifestWorkReplicaSetReconcile interface {
	reconcile(ctx context.Context, pw *workapiv1alpha1.ManifestWorkReplicaSet) (*workapiv1alpha1.ManifestWorkReplicaSet, reconcileState, error)
}

type reconcileState int64

const (
	reconcileStop reconcileState = iota
	reconcileContinue
)

func NewManifestWorkReplicaSetController(
	recorder events.Recorder,
	workClient workclientset.Interface,
	manifestWorkReplicaSetInformer workinformerv1alpha1.ManifestWorkReplicaSetInformer,
	manifestWorkInformer workinformerv1.ManifestWorkInformer,
	placementInformer clusterinformerv1beta1.PlacementInformer,
	placeDecisionInformer clusterinformerv1beta1.PlacementDecisionInformer) factory.Controller {

	controller := newController(
		workClient, manifestWorkReplicaSetInformer, manifestWorkInformer, placementInformer, placeDecisionInformer)

	err := manifestWorkReplicaSetInformer.Informer().AddIndexers(
		cache.Indexers{
			manifestWorkReplicaSetByPlacement: indexManifestWorkReplicaSetByPlacement,
		})
	if err != nil {
		utilruntime.HandleError(err)
	}

	return factory.New().
		WithInformersQueueKeyFunc(func(obj runtime.Object) string {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				utilruntime.HandleError(err)
				return ""
			}
			return key
		}, manifestWorkReplicaSetInformer.Informer()).
		WithFilteredEventsInformersQueueKeyFunc(func(obj runtime.Object) string {
			accessor, _ := meta.Accessor(obj)
			labelValue, ok := accessor.GetLabels()[ManifestWorkReplicaSetControllerNameLabelKey]
			if !ok {
				return ""
			}
			keys := strings.Split(labelValue, ".")
			if len(keys) != 2 {
				return ""
			}
			return fmt.Sprintf("%s/%s", keys[0], keys[1])
		}, func(obj interface{}) bool {
			accessor, err := meta.Accessor(obj)
			if err != nil {
				return false
			}
			if _, ok := accessor.GetLabels()[ManifestWorkReplicaSetControllerNameLabelKey]; ok {
				return true
			}
			return false
		}, manifestWorkInformer.Informer()).
		WithInformersQueueKeysFunc(controller.placementDecisionQueueKeysFunc, placeDecisionInformer.Informer()).
		WithInformersQueueKeysFunc(controller.placementQueueKeysFunc, placementInformer.Informer()).
		WithSync(controller.sync).ToController("ManifestWorkReplicaSetController", recorder)
}

func newController(workClient workclientset.Interface,
	manifestWorkReplicaSetInformer workinformerv1alpha1.ManifestWorkReplicaSetInformer,
	manifestWorkInformer workinformerv1.ManifestWorkInformer,
	placementInformer clusterinformerv1beta1.PlacementInformer,
	placeDecisionInformer clusterinformerv1beta1.PlacementDecisionInformer) *ManifestWorkReplicaSetController {
	return &ManifestWorkReplicaSetController{
		workClient:                    workClient,
		manifestWorkReplicaSetLister:  manifestWorkReplicaSetInformer.Lister(),
		manifestWorkReplicaSetIndexer: manifestWorkReplicaSetInformer.Informer().GetIndexer(),

		reconcilers: []ManifestWorkReplicaSetReconcile{
			&finalizeReconciler{workApplier: workapplier.NewWorkApplierWithTypedClient(workClient, manifestWorkInformer.Lister()),
				workClient: workClient, manifestWorkLister: manifestWorkInformer.Lister()},
			&addFinalizerReconciler{workClient: workClient},
			&deployReconciler{workApplier: workapplier.NewWorkApplierWithTypedClient(workClient, manifestWorkInformer.Lister()),
				manifestWorkLister: manifestWorkInformer.Lister(), placementLister: placementInformer.Lister(), placeDecisionLister: placeDecisionInformer.Lister()},
			&statusReconciler{manifestWorkLister: manifestWorkInformer.Lister()},
		},
	}
}

// sync is the main reconcile loop for placeManifest work. It is triggered every 15sec
func (m *ManifestWorkReplicaSetController) sync(ctx context.Context, controllerContext factory.SyncContext) error {
	key := controllerContext.QueueKey()
	klog.V(4).Infof("Reconciling ManifestWorkReplicaSet %q", key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		// ignore placement whose key is not in format: namespace/name
		utilruntime.HandleError(err)
		return nil
	}

	oldManifestWorkReplicaSet, err := m.manifestWorkReplicaSetLister.ManifestWorkReplicaSets(namespace).Get(name)
	switch {
	case errors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	}

	manifestWorkReplicaSet := oldManifestWorkReplicaSet.DeepCopy()

	var state reconcileState
	var errs []error
	for _, reconciler := range m.reconcilers {
		manifestWorkReplicaSet, state, err = reconciler.reconcile(ctx, manifestWorkReplicaSet)
		if err != nil {
			errs = append(errs, err)
		}
		if state == reconcileStop {
			break
		}
	}

	workSetPatcher := patcher.NewPatcher[
		*workapiv1alpha1.ManifestWorkReplicaSet, workapiv1alpha1.ManifestWorkReplicaSetSpec, workapiv1alpha1.ManifestWorkReplicaSetStatus](
		m.workClient.WorkV1alpha1().ManifestWorkReplicaSets(namespace))

	// Patch status
	if _, err := workSetPatcher.PatchStatus(ctx, manifestWorkReplicaSet, manifestWorkReplicaSet.Status, oldManifestWorkReplicaSet.Status); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

func listManifestWorksByManifestWorkReplicaSet(mwrs *workapiv1alpha1.ManifestWorkReplicaSet,
	manifestWorkLister worklisterv1.ManifestWorkLister) ([]*workapiv1.ManifestWork, error) {
	req, err := labels.NewRequirement(ManifestWorkReplicaSetControllerNameLabelKey, selection.Equals, []string{manifestWorkReplicaSetKey(mwrs)})
	if err != nil {
		return nil, err
	}

	selector := labels.NewSelector().Add(*req)
	return manifestWorkLister.List(selector)
}
