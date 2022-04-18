/*
Copyright 2022.

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

package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	modelsv1 "testCRD/api/v1"

	"k8s.io/client-go/tools/record"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CueServiceModelsReconciler reconciles a CueServiceModels object
type CueServiceModelsReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=models.test.crd.tatsuki,resources=cueservicemodels,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=models.test.crd.tatsuki,resources=cueservicemodels/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=models.test.crd.tatsuki,resources=cueservicemodels/finalizers,verbs=update

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the CueServiceModels object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *CueServiceModelsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// _ = log.FromContext(ctx)
	log := r.Log.WithValues("CueServiceModels", req.NamespacedName)
	// your logic here
	var cueServiceModels modelsv1.CueServiceModels

	// 対象のresourceがNamespace内にあるか確認, なければerrorを返す
	if err := r.Get(ctx, req.NamespacedName, &cueServiceModels); err != nil {
		log.Error(err, "unable to fetch CueServiceModels")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// deploymentNameが変更されていた場合, 前のdeploymentを削除
	if err := r.cleanupOwnedResources(ctx, log, &cueServiceModels); err != nil {
		log.Error(err, "failed to clean up old Deployment resources for this CueServiceModels")
		return ctrl.Result{}, err
	}

	deploymentName := cueServiceModels.Spec.DeploymentName
	// define deployment template using deploymentName
	// deploymentのmetadata生成
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: req.Namespace,
		},
	}
	if _, err := ctrl.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		// replicasを定義 (指定がある場合代入, ない場合1)
		replicas := int32(1)
		if cueServiceModels.Spec.Replicas != nil {
			replicas = *cueServiceModels.Spec.Replicas
		}
		deploy.Spec.Replicas = &replicas

		// deploymentのラベルを定義
		labels := map[string]string{
			"app":        "redis",
			"controller": req.Name,
		}

		// deploymentのラベルがまだ無ければselectorに追加
		if deploy.Spec.Selector == nil {
			deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		}

		// deploymentのコンテナを定義

		containers := []corev1.Container{
			{
				Name:  "redis-container",
				Image: "k8s.gcr.io/redis:e2e",
				Ports: []corev1.ContainerPort{{ContainerPort: 6379}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"cpu":    resource.MustParse("100m"),
						"memory": resource.MustParse("100Mi"),
					},
				},
			},
		}

		// containersがまだ無ければset
		if deploy.Spec.Template.Spec.Containers == nil {
			deploy.Spec.Template.Spec.Containers = containers
		}

		// CueServiceModelsの管理下であることを示すリファレンスを追加
		if err := ctrl.SetControllerReference(&cueServiceModels, deploy, r.Scheme); err != nil {
			log.Error(err, "unable to set ownerReference from CueServiceModels to Deployment")
			return err
		}

		// end of ctrl.CreateOrUpdate
		return nil

	}); err != nil {
		// error handling of ctrl.CreateOrUpdate
		log.Error(err, "unable to ensure deployment is correct")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

var (
	deploymentOwnerKey = ".metadata.controller"
	apiGVStr           = modelsv1.GroupVersion.String()
)

// SetupWithManager sets up the controller with the Manager.
func (r *CueServiceModelsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()

	// Deploymentについて, OwnerがCueServiceModelsの時のみタグを追加 {フィールド名: CRD名 (複数可)}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &appsv1.Deployment{}, deploymentOwnerKey, func(rawObj client.Object) []string {
		deployment := rawObj.(*appsv1.Deployment)
		owner := metav1.GetControllerOf(deployment)
		if owner == nil {
			return nil
		}
		if owner.APIVersion != apiGVStr || owner.Kind != "CueServiceModels" {
			return nil
		}
		return []string{owner.Name}
	}); err != nil {
		return nil
	}

	// 監視するリソースを記述
	return ctrl.NewControllerManagedBy(mgr).
		For(&modelsv1.CueServiceModels{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func (r *CueServiceModelsReconciler) cleanupOwnedResources(ctx context.Context, log logr.Logger, cueServiceModels *modelsv1.CueServiceModels) error {
	log.Info("finding existing Deployments for Foo resource")

	// List all deployment resources owned by this Foo
	var deployments appsv1.DeploymentList
	// namespaceとdeploymentOwnerKeyの値でdeploymentを絞り込む,
	if err := r.List(ctx, &deployments, client.InNamespace(cueServiceModels.Namespace), client.MatchingFields(map[string]string{deploymentOwnerKey: cueServiceModels.Name})); err != nil {
		return err
	}

	// Delete deployment if the deployment name doesn't match foo.spec.deploymentName
	for _, deployment := range deployments.Items {
		// deploymentの名前が等しければclearしない
		if deployment.Name == cueServiceModels.Spec.DeploymentName {
			// If this deployment's name matches the one on the Foo resource
			// then do not delete it.
			continue
		}

		// deploymentの名前が変更されていたら, deploymentをclearする
		// Delete old deployment object which doesn't match foo.spec.deploymentName
		if err := r.Delete(ctx, &deployment); err != nil {
			log.Error(err, "failed to delete Deployment resource")
			return err
		}

		log.Info("delete deployment resource: " + deployment.Name)
		r.Recorder.Eventf(cueServiceModels, corev1.EventTypeNormal, "Deleted", "Deleted deployment %q", deployment.Name)
	}

	return nil
}
