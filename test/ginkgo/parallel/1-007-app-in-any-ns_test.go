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

package parallel

import (
	"context"
	"time"

	applicationFixture "github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture/application"
	appv1alpha1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/gitops-engine/pkg/health"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	imageUpdaterApi "github.com/argoproj-labs/argocd-image-updater/api/v1alpha1"

	"github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture"
	argocdFixture "github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture/argocd"
	deplFixture "github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture/deployment"
	iuFixture "github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture/imageupdater"
	k8sFixture "github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture/k8s"
	ssFixture "github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture/statefulset"
	fixtureUtils "github.com/argoproj-labs/argocd-image-updater/test/ginkgo/fixture/utils"
	argov1beta1api "github.com/argoproj-labs/argocd-operator/api/v1beta1"
)

var _ = Describe("ArgoCD Image Updater Parallel E2E Tests", func() {

	Context("1-007-app-in-any-ns-test", func() {

		var (
			k8sClient                       client.Client
			ctx                             context.Context
			ns                              *corev1.Namespace
			cleanupFunc                     func()
			imageUpdater                    *imageUpdaterApi.ImageUpdater
			argoCD                          *argov1beta1api.ArgoCD
			notificationClusterRole         *rbacv1.ClusterRole
			notificationClusterRoleBinding  *rbacv1.ClusterRoleBinding
			serverClusterRole               *rbacv1.ClusterRole
			serverClusterRoleBinding        *rbacv1.ClusterRoleBinding
			appControllerClusterRole        *rbacv1.ClusterRole
			appControllerClusterRoleBinding *rbacv1.ClusterRoleBinding
		)

		BeforeEach(func() {
			fixture.EnsureParallelCleanSlate()

			k8sClient, _ = fixtureUtils.GetE2ETestKubeClient()
			ctx = context.Background()
		})

		AfterEach(func() {
			// Cleanup is best-effort. Issue deletes and give some time for controllers
			// to process, but don't fail the test if cleanup takes too long.

			if imageUpdater != nil {
				By("deleting ImageUpdater CR")
				_ = k8sClient.Delete(ctx, imageUpdater)
			}

			if argoCD != nil {
				By("deleting ArgoCD CR")
				_ = k8sClient.Delete(ctx, argoCD)
			}

			if notificationClusterRole != nil {
				By("deleting ClusterRoles")
				_ = k8sClient.Delete(ctx, notificationClusterRole)
				_ = k8sClient.Delete(ctx, notificationClusterRoleBinding)
				_ = k8sClient.Delete(ctx, serverClusterRole)
				_ = k8sClient.Delete(ctx, serverClusterRoleBinding)
				if appControllerClusterRole != nil {
					_ = k8sClient.Delete(ctx, appControllerClusterRole)
					_ = k8sClient.Delete(ctx, appControllerClusterRoleBinding)
				}
			}

			if cleanupFunc != nil {
				cleanupFunc()
			}

			fixture.OutputDebugOnFail(ns)

		})

		It("ensures that Image Updater will update Argo CD Application using argocd (default) policy using legacy annotations", func() {

			By("creating namespaces")
			ns, cleanupFunc = fixture.CreateRandomE2ETestNamespaceWithCleanupFunc()
			nsDev := fixture.CreateNamespace("dev")

			// RBAC must be created BEFORE the ArgoCD CR so that when the application-controller
			// starts up with application.namespaces pointing to dev, it already has the
			// permissions to set up informers/watches for that namespace. Without this,
			// the controller's informers fail at startup and don't retry, leaving
			// Applications in external namespaces undiscovered (empty health/sync status).

			By("creating RBAC to allow notifications-controller to access Applications in all namespaces")
			notificationClusterRole = &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "argocd-notifications-controller-cluster-apps",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "argocd-notifications-controller-cluster-apps",
						"app.kubernetes.io/part-of":   ns.Name,
						"app.kubernetes.io/component": "notifications-controller",
					},
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"secrets", "configmaps"},
						Verbs:     []string{"get", "list", "watch"},
					},
					{
						APIGroups: []string{"argoproj.io"},
						Resources: []string{"applications"},
						Verbs:     []string{"get", "list", "watch", "update", "patch"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, notificationClusterRole)).To(Succeed())

			notificationClusterRoleBinding = &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "argocd-notifications-controller-cluster-apps",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "argocd-notifications-controller-cluster-apps",
						"app.kubernetes.io/part-of":   ns.Name,
						"app.kubernetes.io/component": "notifications-controller",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "argocd-notifications-controller-cluster-apps",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "argocd-notifications-controller",
						Namespace: ns.Name,
					},
				},
			}
			Expect(k8sClient.Create(ctx, notificationClusterRoleBinding)).To(Succeed())

			By("creating RBAC to allow argocd-server to perform CRUD operations on Applications in all namespaces")
			serverClusterRole = &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "argocd-server-cluster-apps",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "argocd-server-cluster-apps",
						"app.kubernetes.io/part-of":   ns.Name,
						"app.kubernetes.io/component": "server",
					},
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"events"},
						Verbs:     []string{"create"},
					},
					{
						APIGroups: []string{"argoproj.io"},
						Resources: []string{"applications"},
						Verbs:     []string{"create", "delete", "update", "patch"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, serverClusterRole)).To(Succeed())

			serverClusterRoleBinding = &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "argocd-server-cluster-apps",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "argocd-server-cluster-apps",
						"app.kubernetes.io/part-of":   ns.Name,
						"app.kubernetes.io/component": "server",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "argocd-server-cluster-apps",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "argocd-server",
						Namespace: ns.Name,
					},
				},
			}
			Expect(k8sClient.Create(ctx, serverClusterRoleBinding)).To(Succeed())

			By("creating ClusterRole for argocd-application-controller to discover and manage Applications in other namespaces")
			appControllerClusterRole = &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "argocd-application-controller-cluster-apps",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "argocd-application-controller-cluster-apps",
						"app.kubernetes.io/part-of":   ns.Name,
						"app.kubernetes.io/component": "application-controller",
					},
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{"argoproj.io"},
						Resources: []string{"applications"},
						Verbs:     []string{"get", "list", "watch", "update", "patch"},
					},
					{
						APIGroups: []string{"argoproj.io"},
						Resources: []string{"applications/status"},
						Verbs:     []string{"update", "patch"},
					},
					{
						APIGroups: []string{"argoproj.io"},
						Resources: []string{"appprojects"},
						Verbs:     []string{"get", "list", "watch"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, appControllerClusterRole)).To(Succeed())

			appControllerClusterRoleBinding = &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "argocd-application-controller-cluster-apps",
					Labels: map[string]string{
						"app.kubernetes.io/name":      "argocd-application-controller-cluster-apps",
						"app.kubernetes.io/part-of":   ns.Name,
						"app.kubernetes.io/component": "application-controller",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "argocd-application-controller-cluster-apps",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "argocd-argocd-application-controller",
						Namespace: ns.Name,
					},
				},
			}
			Expect(k8sClient.Create(ctx, appControllerClusterRoleBinding)).To(Succeed())

			By("creating RBAC for argocd-application-controller in dev namespace")
			appControllerRole := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "argocd-application-controller",
					Namespace: nsDev.Name,
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"pods", "services", "replicationcontrollers", "configmaps", "secrets", "endpoints", "persistentvolumeclaims", "events"},
						Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
					},
					{
						APIGroups: []string{"apps"},
						Resources: []string{"deployments", "daemonsets", "replicasets", "statefulsets"},
						Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
					},
					{
						APIGroups: []string{"extensions", "networking.k8s.io"},
						Resources: []string{"ingresses"},
						Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
					},
					{
						APIGroups: []string{"batch"},
						Resources: []string{"jobs", "cronjobs"},
						Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
					},
					{
						APIGroups: []string{"argoproj.io"},
						Resources: []string{"applications", "appprojects"},
						Verbs:     []string{"get", "list", "watch"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, appControllerRole)).To(Succeed())

			appControllerRoleBinding := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "argocd-application-controller-binding",
					Namespace: nsDev.Name,
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "Role",
					Name:     "argocd-application-controller",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "argocd-argocd-application-controller",
						Namespace: ns.Name,
					},
				},
			}
			Expect(k8sClient.Create(ctx, appControllerRoleBinding)).To(Succeed())

			By("creating simple namespace-scoped Argo CD instance with image updater enabled")
			argoCD = &argov1beta1api.ArgoCD{
				ObjectMeta: metav1.ObjectMeta{Name: "argocd", Namespace: ns.Name},
				Spec: argov1beta1api.ArgoCDSpec{
					CmdParams: map[string]string{
						"application.namespaces": nsDev.Name,
					},
					ImageUpdater: argov1beta1api.ArgoCDImageUpdaterSpec{
						Env: []corev1.EnvVar{
							{
								Name:  "IMAGE_UPDATER_LOGLEVEL",
								Value: "trace",
							},
							{
								Name:  "IMAGE_UPDATER_INTERVAL",
								Value: "0",
							},
						},
						Enabled: true},
				},
			}
			Expect(k8sClient.Create(ctx, argoCD)).To(Succeed())

			By("waiting for ArgoCD CR to be reconciled and the instance to be ready")
			Eventually(argoCD, "5m", "3s").Should(argocdFixture.BeAvailable())

			By("verifying all workloads are started")
			deploymentsShouldExist := []string{"argocd-redis", "argocd-server", "argocd-repo-server", "argocd-argocd-image-updater-controller"}
			for _, depl := range deploymentsShouldExist {
				depl := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: depl, Namespace: ns.Name}}
				Eventually(depl).Should(k8sFixture.ExistByName())
				Eventually(depl).Should(deplFixture.HaveReplicas(1))
				Eventually(depl, "3m", "3s").Should(deplFixture.HaveReadyReplicas(1), depl.Name+" was not ready")
			}

			statefulSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "argocd-application-controller", Namespace: ns.Name}}
			Eventually(statefulSet).Should(k8sFixture.ExistByName())
			Eventually(statefulSet).Should(ssFixture.HaveReplicas(1))
			Eventually(statefulSet, "3m", "3s").Should(ssFixture.HaveReadyReplicas(1))

			By("verifying argocd-cmd-params-cm ConfigMap exists with application.namespaces")
			cmdParamsCM := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "argocd-cmd-params-cm", Namespace: ns.Name}}
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cmdParamsCM), cmdParamsCM); err != nil {
					return false
				}
				return cmdParamsCM.Data["application.namespaces"] == nsDev.Name
			}, "2m", "3s").Should(BeTrue())

			// The application-controller pod may have started before the argocd-cmd-params-cm
			// ConfigMap was created (operator race condition). Since application.namespaces is
			// read at startup via env var (configMapKeyRef optional:true), the controller won't
			// know about the dev namespace. A rollout restart ensures the controller picks up
			// the ConfigMap and all RBAC that's now in place.
			By("restarting argocd-server and argocd-application-controller to pick up application.namespaces config")
			restartTimestamp := time.Now().Format(time.RFC3339)

			serverDepl := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: ns.Name}}
			deplFixture.Update(serverDepl, func(d *appsv1.Deployment) {
				if d.Spec.Template.ObjectMeta.Annotations == nil {
					d.Spec.Template.ObjectMeta.Annotations = map[string]string{}
				}
				d.Spec.Template.ObjectMeta.Annotations["kubectl.kubernetes.io/restartedAt"] = restartTimestamp
			})

			ssFixture.Update(statefulSet, func(ss *appsv1.StatefulSet) {
				if ss.Spec.Template.ObjectMeta.Annotations == nil {
					ss.Spec.Template.ObjectMeta.Annotations = map[string]string{}
				}
				ss.Spec.Template.ObjectMeta.Annotations["kubectl.kubernetes.io/restartedAt"] = restartTimestamp
			})

			By("waiting for argocd-server and argocd-application-controller to be ready after restart")
			Eventually(serverDepl, "3m", "3s").Should(deplFixture.HaveReadyReplicas(1))
			Eventually(statefulSet, "3m", "3s").Should(ssFixture.HaveReadyReplicas(1))

			By("creating AppProject")
			appProject := &appv1alpha1.AppProject{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nsDev.Name,
					Namespace: ns.Name,
				},
				Spec: appv1alpha1.AppProjectSpec{
					SourceRepos:      []string{"*"},
					SourceNamespaces: []string{nsDev.Name},
					Destinations: []appv1alpha1.ApplicationDestination{{
						Server:    "*",
						Namespace: "*",
					}},
					ClusterResourceWhitelist: []appv1alpha1.ClusterResourceRestrictionItem{{
						Group: "*",
						Kind:  "*",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, appProject)).To(Succeed())

			By("creating Application")
			app := &appv1alpha1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-01",
					Namespace: nsDev.Name,
				},
				Spec: appv1alpha1.ApplicationSpec{
					Project: nsDev.Name,
					Source: &appv1alpha1.ApplicationSource{
						RepoURL:        "https://github.com/argoproj-labs/argocd-image-updater/",
						Path:           "test/e2e/testdata/005-public-guestbook",
						TargetRevision: "HEAD",
					},
					Destination: appv1alpha1.ApplicationDestination{
						Server:    "https://kubernetes.default.svc",
						Namespace: nsDev.Name,
					},
					SyncPolicy: &appv1alpha1.SyncPolicy{Automated: &appv1alpha1.SyncPolicyAutomated{}},
				},
			}
			Expect(k8sClient.Create(ctx, app)).To(Succeed())

			By("verifying deploying the Application succeeded")
			Eventually(app, "4m", "3s").Should(applicationFixture.HaveHealthStatusCode(health.HealthStatusHealthy))
			Eventually(app, "4m", "3s").Should(applicationFixture.HaveSyncStatusCode(appv1alpha1.SyncStatusCodeSynced))

			By("creating ImageUpdater CR")
			updateStrategy := "semver"
			imageUpdater = &imageUpdaterApi.ImageUpdater{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "image-updater",
					Namespace: nsDev.Name,
				},
				Spec: imageUpdaterApi.ImageUpdaterSpec{
					Namespace: nsDev.Name,
					ApplicationRefs: []imageUpdaterApi.ApplicationRef{
						{
							NamePattern: "app*",
							Images: []imageUpdaterApi.ImageConfig{
								{
									Alias:     "guestbook",
									ImageName: "quay.io/dkarpele/my-guestbook:~29437546.0",
									CommonUpdateSettings: &imageUpdaterApi.CommonUpdateSettings{
										UpdateStrategy: &updateStrategy,
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, imageUpdater)).To(Succeed())

			By("ensuring that the Application image has `29437546.0` version after update")
			triggerRefresh := iuFixture.TriggerArgoCDRefresh(ctx, k8sClient, app)
			Eventually(func() string {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(app), app)

				if err != nil {
					return "" // Let Eventually retry on error
				}

				// Trigger ArgoCD refresh periodically to force immediate git check
				triggerRefresh()

				// Nil-safe check: The Kustomize block is only added by the Image Updater after its first run.
				// We must check that it and its Images field exist before trying to access them.
				if app.Spec.Source.Kustomize != nil && len(app.Spec.Source.Kustomize.Images) > 0 {
					return string(app.Spec.Source.Kustomize.Images[0])
				}

				// Return an empty string to signify the condition is not yet met.
				return ""
			}, "5m", "3s").Should(Equal("quay.io/dkarpele/my-guestbook:29437546.0"))
		})
	})
})
