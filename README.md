# Kubebuilder Demo

参(抄)考(袭) [https://github.com/cnych/opdemo]

目前我对于 CRD 以及自定义 Operator 的理解是，利用 Kubernetes 已有的资源，例如 Deployment、Service、Pods 通过组合构建出更加复杂的应用。就像是这个 appservice-operator 一样，利用 Deployment + Service 构建出了一个 AppService 资源，这样在部署的时候不用分别部署 Deployment 和 Service，而是直接部署 AppService 即可。下面简述一下 AppService Operator 构建的过程。

---

## 环境

- Kubebuilder： v3.0.0
- Kubernetes api： v1.19.2
- 容器运行时： Docker v20.10.5
- Go： v1.15.6
- 本地 Registry 地址 10.211.55.2:10000，需要将该地址加入至 docker insecure registry 中，具体操作详见 [https://docs.docker.com/registry/insecure/](https://docs.docker.com/registry/insecure/) 

---

## 初始化项目

```bash
go mod init github.com/schwarzeni/kubebuilder-appservice
kubebuilder init --domain schwarzeni.github.com
kubebuilder create api --group batch --version v1alpha1 --kind AppService
```

---

## 设计 CRD

部署 AppService 时的样例 yaml 文件如下

```yaml
apiVersion: batch.schwarzeni.github.com/v1alpha1
kind: AppService
metadata:
  name: nginx-app
spec:
  replicas: 3
  image: nginx:alpine
  ports:
    - port: 80
      targetPort: 80
      nodePort: 30002
```

修改文件 `api/v1alpha1/appservice_types.go` 中的 `AppServiceSpec` 和 `AppServiceStatus`

```go
// api/v1alpha1/appservice_types.go

import (
  appsv1 "k8s.io/api/apps/v1"
  corev1 "k8s.io/api/core/v1"
  metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AppServiceSpec struct {
  Replicas  *int32                      `json:"replicas"`
  Image     string                      `json:"image"`
  Resources corev1.ResourceRequirements `json:"resources,omitempty"`
  Envs      []corev1.EnvVar             `json:"envs,omitempty"`
  Ports     []corev1.ServicePort        `json:"ports,omitempty"`
}

type AppServiceStatus struct {
  appsv1.DeploymentStatus `json:",inline"`
}
```

执行命令生成 CRD 定义 yaml 文件 `config/crd/bases/batch.schwarzeni.github.com_appservices.yaml` ，同时将其部署至 K8S 中

```bash
make install
```

---

## 设计 Controller

Controller 的 Reconcile 如下：

- 如果 AppService 实例不存在，则根据 AppServiceSpec 创建
  - 创建 Deployment 资源
  - 创建 Service 资源
- 如果 AppService 实例存在，则将 Annotations 中记录的 Spec 值与当前的 Spec 比较
  - 如果前后的 Spec 不同
    - 更新 Deployment 资源
    - 更新 Service 资源
  - 如果前后的 Spec 相同，则无需额外的操作
- 使用 Annotations 记录当前 Spec 的值

修改 `controllers/appservice_controller.go` 中的 AppServiceReconciler.Reconcile 函数如下，这里省略了部分日志输出以及创建 Deployment 和 Service 实例的函数

```go
func (r *AppServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
  instance := &batchv1alpha1.AppService{}
  if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
    if errors.IsNotFound(err) {
      return reconcile.Result{}, nil
    }
    return reconcile.Result{}, err
  }

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
```

---

## 创建 Deployment 和 Service 实例

一般来说，Deployment 和 Service 都是用 yaml 配置文件通过 kubectl apply 创建的，但是最终都会被转变为 Deployment 和 Service 对象。这里是直接采用 Go 代码的方式根据 AppService 的相关配置创建 Deployment 和 Service 对象。

创建 Deployment 对象：


```go
// controllers/appservice_controller.go
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
```

创建 Container 对象：

```go
// controllers/appservice_controller.go
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
```

创建 Service 对象：

```go
// controllers/appservice_controller.go
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
```

---

## RBAC 资源访问权限

kubebuilder 通过 kustomize 来配置 Operator 的 RBAC ，这里需要给 AppService-Controller Deployment 和 Service 资源的操作权限

```go
// controllers/appservice_controller.go

//+kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="apps",resources=deployments/status,verbs=get
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services/status,verbs=get
```

---

## 部署与运行

本地运行

```bash
make run
```

部署至集群中

```bash
make docker-build docker-push IMG=10.211.55.2:10000/appservice-operator:v1
make deploy IMG=10.211.55.2:10000/appservice-operator:v1
```

部署这样一个 cr 

```yaml
apiVersion: batch.schwarzeni.github.com/v1alpha1
kind: AppService
metadata:
  name: nginx-app
spec:
  replicas: 2
  image: nginx:alpine
  ports:
    - port: 80
      targetPort: 80
      nodePort: 30002

# kubectl apply -f config/samples/batch_v1alpha1_appservice.yaml -n kubebuilder-appservice-system
```

查看相关的 pod 是否启动

```txt
> kubectl get pods -n kubebuilder-appservice-system

NAME                                                         READY   STATUS    RESTARTS   AGE
kubebuilder-appservice-controller-manager-84c79db9f4-hkvkj   2/2     Running   0          34m
nginx-app-6cdc9c97d4-pjqgz                                   1/1     Running   0          28s
nginx-app-6cdc9c97d4-t2pdk                                   1/1     Running   0          28s
```

修改 cr 中的 replicas 为 5，再 kubectl apply 一下

```txt
> kubectl get pods -n kubebuilder-appservice-system
NAME                                                         READY   STATUS    RESTARTS   AGE
kubebuilder-appservice-controller-manager-84c79db9f4-hkvkj   2/2     Running   0          35m
nginx-app-6cdc9c97d4-j2hs2                                   1/1     Running   0          4s
nginx-app-6cdc9c97d4-pjqgz                                   1/1     Running   0          80s
nginx-app-6cdc9c97d4-rt5tk                                   1/1     Running   0          4s
nginx-app-6cdc9c97d4-t2pdk                                   1/1     Running   0          80s
nginx-app-6cdc9c97d4-w5xfj                                   1/1     Running   0          4s
```
