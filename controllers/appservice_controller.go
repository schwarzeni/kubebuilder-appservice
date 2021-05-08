/*
Copyright 2021.

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
	"reflect"

	"github.com/go-logr/logr"
	batchv1alpha1 "github.com/schwarzeni/kubebuilder-appservice/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// AppServiceReconciler reconciles a AppService object
type AppServiceReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=batch.schwarzeni.github.com,resources=appservices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch.schwarzeni.github.com,resources=appservices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=batch.schwarzeni.github.com,resources=appservices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods/status,verbs=get
//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments/status,verbs=get
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AppService object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *AppServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("appservice", req.NamespacedName)
	logger.Info("Reconciling AppService")

	instance := &batchv1alpha1.AppService{}
	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	// TODO: print kind to check NewDeployment() app.Kind
	logger.Info("app kind: " + instance.Kind + ", app name: " + instance.Name)

	if instance.DeletionTimestamp != nil {
		return reconcile.Result{}, nil
	}

	deployment := &appsv1.Deployment{}
	if err := r.Client.Get(ctx, req.NamespacedName, deployment); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// 1. 不存在，则创建
		// 1-1. 创建 Deployment
		deployment = NewDeployment(instance)
		if err := r.Client.Create(ctx, deployment); err != nil {
			return ctrl.Result{}, err
		}

		// 1-2. 创建 Service
		svc := NewService(instance)
		if err := r.Client.Create(ctx, svc); err != nil {
			return ctrl.Result{}, err
		}

	} else {
		oldSpec := &batchv1alpha1.AppServiceSpec{}
		if err := json.Unmarshal([]byte(instance.Annotations["spec"]), oldSpec); err != nil {
			return ctrl.Result{}, err
		}
		// 2. 对比更新
		if !reflect.DeepEqual(instance.Spec, *oldSpec) {
			// 2-1. 更新 Deployment 资源
			newDeployment := NewDeployment(instance)
			currDeployment := &appsv1.Deployment{}
			if err := r.Client.Get(ctx, req.NamespacedName, currDeployment); err != nil {
				return ctrl.Result{}, err
			}
			currDeployment.Spec = newDeployment.Spec
			if err := r.Client.Update(ctx, currDeployment); err != nil {
				return ctrl.Result{}, err
			}

			// 2-2. 更新 Service 资源
			newService := NewService(instance)
			currService := &corev1.Service{}
			if err := r.Client.Get(ctx, req.NamespacedName, currService); err != nil {
				return ctrl.Result{}, err
			}
			currIP := currService.Spec.ClusterIP
			currService.Spec = newService.Spec
			currService.Spec.ClusterIP = currIP
			if err := r.Client.Update(ctx, currService); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 3. 关联 Annotations
	data, _ := json.Marshal(instance.Spec)
	if instance.Annotations != nil {
		instance.Annotations["spec"] = string(data)
	} else {
		instance.Annotations = map[string]string{"spec": string(data)}
	}
	if err := r.Client.Update(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AppServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1alpha1.AppService{}).
		Complete(r)
}

func NewDeployment(app *batchv1alpha1.AppService) *appsv1.Deployment {
	labels := map[string]string{"app": app.Name}
	selector := &metav1.LabelSelector{MatchLabels: labels}
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: app.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(app, schema.GroupVersionKind{
					Group:   batchv1alpha1.GroupVersion.Group,
					Version: batchv1alpha1.GroupVersion.Version,
					Kind:    app.Kind,
				}),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: app.Spec.Replicas,
			Selector: selector,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: newContainer(app)},
			},
		},
	}
}

func newContainer(app *batchv1alpha1.AppService) []corev1.Container {
	containerPorts := []corev1.ContainerPort{}
	for _, svcPort := range app.Spec.Ports {
		cport := corev1.ContainerPort{}
		cport.ContainerPort = svcPort.TargetPort.IntVal
		containerPorts = append(containerPorts, cport)
	}
	return []corev1.Container{
		{
			Name:            app.Name,
			Image:           app.Spec.Image,
			Resources:       app.Spec.Resources,
			Ports:           containerPorts,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             app.Spec.Envs,
		},
	}
}

func NewService(app *batchv1alpha1.AppService) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: app.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(app, schema.GroupVersionKind{
					Group:   batchv1alpha1.GroupVersion.Group,
					Version: batchv1alpha1.GroupVersion.Version,
					Kind:    app.Kind,
				}),
			},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeNodePort,
			Ports: app.Spec.Ports,
			Selector: map[string]string{
				"app": app.Name,
			},
		},
	}
}
