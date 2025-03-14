package argocd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	yaml "sigs.k8s.io/yaml/goyaml.v3"

	"github.com/argoproj-labs/argocd-image-updater/ext/git"
	gitmock "github.com/argoproj-labs/argocd-image-updater/ext/git/mocks"
	argomock "github.com/argoproj-labs/argocd-image-updater/pkg/argocd/mocks"
	"github.com/argoproj-labs/argocd-image-updater/pkg/common"
	"github.com/argoproj-labs/argocd-image-updater/pkg/kube"
	registryCommon "github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/common"
	"github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/image"
	registryKube "github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/kube"
	"github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/registry"
	regmock "github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/registry/mocks"
	"github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/tag"
	"github.com/argoproj-labs/argocd-image-updater/test/fake"
	"github.com/argoproj-labs/argocd-image-updater/test/fixture"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"github.com/distribution/distribution/v3/manifest/schema1" //nolint:staticcheck
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_UpdateApplication(t *testing.T) {
	t.Run("Test kustomize w/ multiple images w/ different registry w/ different tags", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.2", "1.0.3"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		annotations := map[string]string{
			common.ImageUpdaterAnnotation: "foobar=gcr.io/jannfis/foobar:>=1.0.1,foobar=gcr.io/jannfis/barbar:>=1.0.1",
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:        "guestbook",
					Namespace:   "guestbook",
					Annotations: annotations,
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.1",
								"jannfis/barbar:1.0.1",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"gcr.io/jannfis/foobar:1.0.1",
							"gcr.io/jannfis/barbar:1.0.1",
						},
					},
				},
			},
			Images: *parseImageList(annotations),
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, v1alpha1.KustomizeImage("gcr.io/jannfis/foobar:1.0.3"), appImages.Application.Spec.Source.Kustomize.Images[0])
		assert.Equal(t, v1alpha1.KustomizeImage("gcr.io/jannfis/barbar:1.0.3"), appImages.Application.Spec.Source.Kustomize.Images[1])
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 2, res.NumImagesConsidered)
		assert.Equal(t, 2, res.NumImagesUpdated)
	})

	t.Run("Update app w/ GitHub App creds", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.2", "1.0.3"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"githubAppID":             []byte("12345678"),
			"githubAppInstallationID": []byte("87654321"),
			"githubAppPrivateKey":     []byte("foo"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}

		annotations := map[string]string{
			common.ImageUpdaterAnnotation:    "foo=gcr.io/jannfis/foobar:>=1.0.1",
			common.WriteBackMethodAnnotation: "git:secret:argocd-image-updater/git-creds",
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:        "guestbook",
					Namespace:   "guestbook",
					Annotations: annotations,
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						RepoURL:        "https://example.com/example",
						TargetRevision: "main",
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.1",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"gcr.io/jannfis/foobar:1.0.1",
						},
					},
				},
			},
			Images: *parseImageList(annotations),
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, v1alpha1.KustomizeImage("gcr.io/jannfis/foobar:1.0.3"), appImages.Application.Spec.Source.Kustomize.Images[0])
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		// configured githubApp creds will take effect and git client will catch the invalid GithubAppPrivateKey "foo":
		// "Could not update application spec: could not parse private key: invalid key: Key must be a PEM encoded PKCS1 or PKCS8 key"
		assert.Equal(t, 1, res.NumErrors)
	})

	t.Run("Test successful update", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.MatchedBy(func(s string) bool {
				return s == "jannfis/foobar"
			})).Return(nil)
			regMock.On("Tags").Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/foobar:~1.0.0"),
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, &application.ApplicationUpdateSpecRequest{
			Name:         &appImages.Application.Name,
			AppNamespace: &appImages.Application.Namespace,
			Spec:         &appImages.Application.Spec,
		}).Return(nil, nil)

		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 1, res.NumImagesUpdated)
	})

	t.Run("Test successful update two images", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.MatchedBy(func(s string) bool {
				return s == "jannfis/foobar" || s == "jannfis/barbar"
			})).Return(nil)
			regMock.On("Tags").Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
								"jannfis/barbar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
							"jannfis/barbar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/foobar:~1.0.0"),
				image.NewFromIdentifier("jannfis/barbar:~1.0.0"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 2, res.NumImagesConsidered)
		assert.Equal(t, 2, res.NumImagesUpdated)
	})

	t.Run("Test kustomize w/ different registry", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			assert.Equal(t, endpoint.RegistryPrefix, "quay.io")
			regMock.On("NewRepository", mock.MatchedBy(func(s string) bool {
				return s == "jannfis/foobar"
			})).Return(nil)
			regMock.On("Tags").Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
					Annotations: map[string]string{
						"argocd-image-updater.argoproj.io/image-list":                  "foobar=quay.io/jannfis/foobar:~1.0.0",
						"argocd-image-updater.argoproj.io/foobar.kustomize.image-name": "jannfis/foobar",
						"argocd-image-updater.argoproj.io/foobar.force-update":         "true",
					},
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("quay.io/jannfis/foobar"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 1, res.NumImagesUpdated)
	})

	t.Run("Test kustomize w/ different registry and org", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			assert.Equal(t, endpoint.RegistryPrefix, "quay.io")
			regMock.On("NewRepository", mock.MatchedBy(func(s string) bool {
				return s == "someorg/foobar"
			})).Return(nil)
			regMock.On("Tags").Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
					Annotations: map[string]string{
						"argocd-image-updater.argoproj.io/image-list":                  "foobar=quay.io/someorg/foobar:~1.0.0",
						"argocd-image-updater.argoproj.io/foobar.kustomize.image-name": "jannfis/foobar",
						"argocd-image-updater.argoproj.io/foobar.force-update":         "true",
					},
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("quay.io/someorg/foobar"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 1, res.NumImagesUpdated)
	})

	t.Run("Test successful update when no tag is set in running workload", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/foobar:1.0.x"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 1, res.NumImagesUpdated)
	})

	t.Run("Test successful update with credentials", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			assert.Equal(t, "myuser", username)
			assert.Equal(t, "mypass", password)
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(fixture.NewSecret("foo", "bar", map[string][]byte{"creds": []byte("myuser:mypass")})),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
					Annotations: map[string]string{
						fmt.Sprintf(registryCommon.Prefixed(common.ImageUpdaterAnnotationPrefix, registryCommon.PullSecretAnnotationSuffix), "dummy"): "secret:foo/bar#creds",
					},
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("dummy=jannfis/foobar:1.0.1"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 1, res.NumImagesUpdated)
	})

	t.Run("Test skip because of image not in list", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/barbar:1.0.1"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 1, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 0, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

	t.Run("Test skip because of image up-to-date", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.1",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.1",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/foobar:1.0.1"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

	t.Run("Test update because of image registry changed", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		annotations := map[string]string{
			common.ImageUpdaterAnnotation: "foobar=gcr.io/jannfis/foobar:>=1.0.1",
			fmt.Sprintf(registryCommon.Prefixed(common.ImageUpdaterAnnotationPrefix, registryCommon.KustomizeApplicationNameAnnotationSuffix), "foobar"): "jannfis/foobar",
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:        "guestbook",
					Namespace:   "guestbook",
					Annotations: annotations,
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.1",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.1",
						},
					},
				},
			},
			Images: *parseImageList(annotations),
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 1, res.NumImagesUpdated)
	})

	t.Run("Test not updated because kustomize image is the same", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		annotations := map[string]string{
			common.ImageUpdaterAnnotation: "foobar=gcr.io/jannfis/foobar:>=1.0.1",
			fmt.Sprintf(registryCommon.Prefixed(common.ImageUpdaterAnnotationPrefix, registryCommon.KustomizeApplicationNameAnnotationSuffix), "foobar"): "jannfis/foobar",
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:        "guestbook",
					Namespace:   "guestbook",
					Annotations: annotations,
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.1",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"gcr.io/jannfis/foobar:1.0.1",
						},
					},
				},
			},
			Images: *parseImageList(annotations),
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

	t.Run("Test skip because of match-tag pattern doesn't match", func(t *testing.T) {
		meta := make([]*schema1.SignedManifest, 4) //nolint:staticcheck
		for i := 0; i < 4; i++ {
			ts := fmt.Sprintf("2006-01-02T15:%.02d:05.999999999Z", i)
			meta[i] = &schema1.SignedManifest{ //nolint:staticcheck
				Manifest: schema1.Manifest{ //nolint:staticcheck
					History: []schema1.History{ //nolint:staticcheck
						{
							V1Compatibility: `{"created":"` + ts + `"}`,
						},
					},
				},
			}
		}
		called := 0
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"one", "two", "three", "four"}, nil)
			regMock.On("Manifest", mock.Anything).Return(meta[called], nil)
			called += 1
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
					Annotations: map[string]string{
						fmt.Sprintf(registryCommon.Prefixed(common.ImageUpdaterAnnotationPrefix, registryCommon.AllowTagsOptionAnnotationSuffix), "dummy"): "regexp:^foobar$",
						fmt.Sprintf(registryCommon.Prefixed(common.ImageUpdaterAnnotationPrefix, registryCommon.UpdateStrategyAnnotationSuffix), "dummy"):  "name",
					},
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:one",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:one",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("dummy=jannfis/foobar"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

	t.Run("Test skip because of ignored", func(t *testing.T) {
		meta := make([]*schema1.SignedManifest, 4) //nolint:staticcheck
		for i := 0; i < 4; i++ {
			ts := fmt.Sprintf("2006-01-02T15:%.02d:05.999999999Z", i)
			meta[i] = &schema1.SignedManifest{ //nolint:staticcheck
				Manifest: schema1.Manifest{ //nolint:staticcheck
					History: []schema1.History{ //nolint:staticcheck
						{
							V1Compatibility: `{"created":"` + ts + `"}`,
						},
					},
				},
			}
		}
		called := 0
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"one", "two", "three", "four"}, nil)
			regMock.On("Manifest", mock.Anything).Return(meta[called], nil)
			called += 1
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
					Annotations: map[string]string{
						fmt.Sprintf(registryCommon.Prefixed(common.ImageUpdaterAnnotationPrefix, registryCommon.IgnoreTagsOptionAnnotationSuffix), "dummy"): "*",
						fmt.Sprintf(registryCommon.UpdateStrategyAnnotationSuffix, "dummy"):                                                                 "name",
					},
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:one",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:one",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("dummy=jannfis/foobar"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

	t.Run("Update from inferred registry", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.1", "1.0.2"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"example.io/jannfis/example:1.0.1",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"example.io/jannfis/example:1.0.1",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("example.io/jannfis/example:1.0.x"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 0, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 1, res.NumImagesUpdated)
	})

	t.Run("Test error on generic registry client failure", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			return nil, errors.New("some error")
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/foobar:1.0.1"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 1, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

	t.Run("Test error on failure to list tags", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return(nil, errors.New("some error"))
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:1.0.0",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:1.0.0",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/foobar:1.0.1"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 1, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

	t.Run("Test error on improper semver in tag", func(t *testing.T) {
		mockClientFn := func(endpoint *registry.RegistryEndpoint, username, password string) (registry.RegistryClient, error) {
			regMock := regmock.RegistryClient{}
			regMock.On("NewRepository", mock.Anything).Return(nil)
			regMock.On("Tags", mock.Anything).Return([]string{"1.0.0", "1.0.1"}, nil)
			return &regMock, nil
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}
		appImages := &ApplicationImages{
			Application: v1alpha1.Application{
				ObjectMeta: v1.ObjectMeta{
					Name:      "guestbook",
					Namespace: "guestbook",
				},
				Spec: v1alpha1.ApplicationSpec{
					Source: &v1alpha1.ApplicationSource{
						Kustomize: &v1alpha1.ApplicationSourceKustomize{
							Images: v1alpha1.KustomizeImages{
								"jannfis/foobar:stable",
							},
						},
					},
				},
				Status: v1alpha1.ApplicationStatus{
					SourceType: v1alpha1.ApplicationSourceTypeKustomize,
					Summary: v1alpha1.ApplicationSummary{
						Images: []string{
							"jannfis/foobar:stable",
						},
					},
				},
			},
			Images: image.ContainerImageList{
				image.NewFromIdentifier("jannfis/foobar:stable"),
			},
		}
		res := UpdateApplication(&UpdateConfiguration{
			NewRegFN:   mockClientFn,
			ArgoClient: &argoClient,
			KubeClient: &kubeClient,
			UpdateApp:  appImages,
			DryRun:     false,
		}, NewSyncIterationState())
		assert.Equal(t, 1, res.NumErrors)
		assert.Equal(t, 0, res.NumSkipped)
		assert.Equal(t, 1, res.NumApplicationsProcessed)
		assert.Equal(t, 1, res.NumImagesConsidered)
		assert.Equal(t, 0, res.NumImagesUpdated)
	})

}

func Test_MarshalParamsOverride(t *testing.T) {
	t.Run("Valid Kustomize source", func(t *testing.T) {
		expected := `
kustomize:
  images:
  - baz
  - foo
  - bar
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Kustomize: &v1alpha1.ApplicationSourceKustomize{
						Images: v1alpha1.KustomizeImages{
							"foo",
							"bar",
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		originalData := []byte(`
kustomize:
  images:
  - baz
`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(yaml)))
	})

	t.Run("Merge images param", func(t *testing.T) {
		expected := `
kustomize:
  images:
  - existing:latest
  - updated:latest
  - new
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list": "nginx",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Kustomize: &v1alpha1.ApplicationSourceKustomize{
						Images: v1alpha1.KustomizeImages{
							"new",
							"updated:latest",
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		originalData := []byte(`
kustomize:
  images:
  - existing:latest
  - updated:old
`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(yaml)))
	})

	t.Run("Empty Kustomize source", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}

		yaml, err := marshalParamsOverride(&app, nil)
		require.NoError(t, err)
		assert.Empty(t, yaml)
		assert.Equal(t, "", strings.TrimSpace(string(yaml)))
	})

	t.Run("Valid Helm source", func(t *testing.T) {
		expected := `
helm:
  parameters:
  - name: baz
    value: baz
    forcestring: false
  - name: foo
    value: bar
    forcestring: true
  - name: bar
    value: foo
    forcestring: true
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "foo",
								Value:       "bar",
								ForceString: true,
							},
							{
								Name:        "bar",
								Value:       "foo",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		originalData := []byte(`
helm:
  parameters:
  - name: baz
    value: baz
    forcestring: false
`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Empty originalData error with valid Helm source", func(t *testing.T) {
		expected := `
helm:
  parameters:
  - name: foo
    value: bar
    forcestring: true
  - name: bar
    value: foo
    forcestring: true
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "foo",
								Value:       "bar",
								ForceString: true,
							},
							{
								Name:        "bar",
								Value:       "foo",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		originalData := []byte(``)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Invalid unmarshal originalData error with valid Helm source", func(t *testing.T) {
		expected := `
helm:
  parameters:
  - name: foo
    value: bar
    forcestring: true
  - name: bar
    value: foo
    forcestring: true
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "foo",
								Value:       "bar",
								ForceString: true,
							},
							{
								Name:        "bar",
								Value:       "foo",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		originalData := []byte(`random content`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Empty Helm source", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		yaml, err := marshalParamsOverride(&app, nil)
		require.NoError(t, err)
		assert.Empty(t, yaml)
	})

	t.Run("Valid Helm source with Helm values file", func(t *testing.T) {
		expected := `
image.name: nginx
image.tag: v1.0.0
replicas: 1
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "image.name",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag":  "image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "image.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`
image.name: nginx
image.tag: v0.0.0
replicas: 1
`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Valid Helm source with Helm values file and image-spec", func(t *testing.T) {
		expected := `
image.spec.foo: nginx:v1.0.0
replicas: 1
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-spec": "image.spec.foo",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.spec.foo",
								Value:       "nginx:v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`
image.spec.foo: nginx:v0.0.0
replicas: 1
`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))

		// when image.spec.foo fields are missing in the target helm value file,
		// they should be auto created without corrupting any other pre-existing elements.
		originalData = []byte("test-value1: one")
		expected = `
test-value1: one
image:
  spec:
    foo: nginx:v1.0.0
`
		yaml, err = marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Valid Helm source with Helm values file with multiple images", func(t *testing.T) {
		expected := `
nginx.image.name: nginx
nginx.image.tag: v1.0.0
redis.image.name: redis
redis.image.tag: v1.0.0
replicas: 1
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx=nginx, redis=redis",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "nginx.image.name",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag":  "nginx.image.tag",
					"argocd-image-updater.argoproj.io/redis.helm.image-name": "redis.image.name",
					"argocd-image-updater.argoproj.io/redis.helm.image-tag":  "redis.image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Sources: []v1alpha1.ApplicationSource{
					{
						Chart: "my-app",
						Helm: &v1alpha1.ApplicationSourceHelm{
							ReleaseName: "my-app",
							ValueFiles:  []string{"$values/some/dir/values.yaml"},
							Parameters: []v1alpha1.HelmParameter{
								{
									Name:        "nginx.image.name",
									Value:       "nginx",
									ForceString: true,
								},
								{
									Name:        "nginx.image.tag",
									Value:       "v1.0.0",
									ForceString: true,
								},
								{
									Name:        "redis.image.name",
									Value:       "redis",
									ForceString: true,
								},
								{
									Name:        "redis.image.tag",
									Value:       "v1.0.0",
									ForceString: true,
								},
							},
						},
						RepoURL:        "https://example.com/example",
						TargetRevision: "main",
					},
					{
						Ref:            "values",
						RepoURL:        "https://example.com/example2",
						TargetRevision: "main",
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceTypes: []v1alpha1.ApplicationSourceType{
					v1alpha1.ApplicationSourceTypeHelm,
					"",
				},
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
						"redis:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`
nginx.image.name: nginx
nginx.image.tag: v0.0.0
redis.image.name: redis
redis.image.tag: v0.0.0
replicas: 1
`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))

		// when nginx.* and redis.* fields are missing in the target helm value file,
		// they should be auto created without corrupting any other pre-existing elements.
		originalData = []byte("test-value1: one")
		expected = `
test-value1: one
nginx:
  image:
    tag: v1.0.0
    name: nginx
redis:
  image:
    tag: v1.0.0
    name: redis
`
		yaml, err = marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Valid Helm source with Helm values file with multiple aliases", func(t *testing.T) {
		expected := `
foo.image.name: nginx
foo.image.tag: v1.0.0
bar.image.name: nginx
bar.image.tag: v1.0.0
bbb.image.name: nginx
bbb.image.tag: v1.0.0
replicas: 1
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":          "foo=nginx, bar=nginx, bbb=nginx",
					"argocd-image-updater.argoproj.io/write-back-method":   "git",
					"argocd-image-updater.argoproj.io/write-back-target":   "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/foo.helm.image-name": "foo.image.name",
					"argocd-image-updater.argoproj.io/foo.helm.image-tag":  "foo.image.tag",
					"argocd-image-updater.argoproj.io/bar.helm.image-name": "bar.image.name",
					"argocd-image-updater.argoproj.io/bar.helm.image-tag":  "bar.image.tag",
					"argocd-image-updater.argoproj.io/bbb.helm.image-name": "bbb.image.name",
					"argocd-image-updater.argoproj.io/bbb.helm.image-tag":  "bbb.image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Sources: []v1alpha1.ApplicationSource{
					{
						Chart: "my-app",
						Helm: &v1alpha1.ApplicationSourceHelm{
							ReleaseName: "my-app",
							ValueFiles:  []string{"$values/some/dir/values.yaml"},
							Parameters: []v1alpha1.HelmParameter{
								{
									Name:        "foo.image.name",
									Value:       "nginx",
									ForceString: true,
								},
								{
									Name:        "foo.image.tag",
									Value:       "v1.0.0",
									ForceString: true,
								},
								{
									Name:        "bar.image.name",
									Value:       "nginx",
									ForceString: true,
								},
								{
									Name:        "bar.image.tag",
									Value:       "v1.0.0",
									ForceString: true,
								},
								{
									Name:        "bbb.image.name",
									Value:       "nginx",
									ForceString: true,
								},
								{
									Name:        "bbb.image.tag",
									Value:       "v1.0.0",
									ForceString: true,
								},
							},
						},
						RepoURL:        "https://example.com/example",
						TargetRevision: "main",
					},
					{
						Ref:            "values",
						RepoURL:        "https://example.com/example2",
						TargetRevision: "main",
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceTypes: []v1alpha1.ApplicationSourceType{
					v1alpha1.ApplicationSourceTypeHelm,
					"",
				},
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`
foo.image.name: nginx
foo.image.tag: v0.0.0
bar.image.name: nginx
bar.image.tag: v0.0.0
bbb.image.name: nginx
bbb.image.tag: v0.0.0
replicas: 1
`)
		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Failed to setValue image parameter name", func(t *testing.T) {
		expected := `
test-value1: one
image:
  name: nginx
  tag: v1.0.0
replicas: 1
`

		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "image.name",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag":  "image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "image.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`
test-value1: one
image:
  name: nginx
replicas: 1
`)

		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Failed to setValue image parameter version", func(t *testing.T) {
		expected := `
image:
  tag: v1.0.0
  name: nginx
replicas: 1
`
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "image.name",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag":  "image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "image.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`
image:
  tag: v0.0.0
replicas: 1
`)

		yaml, err := marshalParamsOverride(&app, originalData)
		require.NoError(t, err)
		assert.NotEmpty(t, yaml)
		assert.Equal(t, strings.TrimSpace(strings.ReplaceAll(expected, "\t", "  ")), strings.TrimSpace(string(yaml)))
	})

	t.Run("Missing annotation image-tag for helmvalues write-back-target", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "image.name",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "dockerimage.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "dockerimage.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`random: yaml`)
		_, err := marshalParamsOverride(&app, originalData)
		assert.Error(t, err)
		assert.Equal(t, "could not find an image-tag annotation for image nginx", err.Error())
	})

	t.Run("Missing annotation image-name for helmvalues write-back-target", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":           "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":    "git",
					"argocd-image-updater.argoproj.io/write-back-target":    "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag": "image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "image.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`random: yaml`)
		_, err := marshalParamsOverride(&app, originalData)
		assert.Error(t, err)
		assert.Equal(t, "could not find an image-name annotation for image nginx", err.Error())
	})

	t.Run("Image-name annotation value not found in Helm source parameters list", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "wrongimage.name",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag":  "image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "image.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`random: yaml`)
		_, err := marshalParamsOverride(&app, originalData)
		assert.Error(t, err)
	})

	t.Run("Image-tag annotation value not found in Helm source parameters list", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "image.name",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag":  "wrongimage.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "image.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`random: yaml`)
		_, err := marshalParamsOverride(&app, originalData)
		assert.Error(t, err)
		assert.Equal(t, "wrongimage.tag parameter not found", err.Error())
	})

	t.Run("Invalid parameters merge for Helm source with Helm values file", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":            "nginx",
					"argocd-image-updater.argoproj.io/write-back-method":     "git",
					"argocd-image-updater.argoproj.io/write-back-target":     "helmvalues:./test-values.yaml",
					"argocd-image-updater.argoproj.io/nginx.helm.image-name": "image.name",
					"argocd-image-updater.argoproj.io/nginx.helm.image-tag":  "image.tag",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Helm: &v1alpha1.ApplicationSourceHelm{
						Parameters: []v1alpha1.HelmParameter{
							{
								Name:        "image.name",
								Value:       "nginx",
								ForceString: true,
							},
							{
								Name:        "image.tag",
								Value:       "v1.0.0",
								ForceString: true,
							},
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
				Summary: v1alpha1.ApplicationSummary{
					Images: []string{
						"nginx:v0.0.0",
					},
				},
			},
		}

		originalData := []byte(`random content`)
		_, err := marshalParamsOverride(&app, originalData)
		assert.Error(t, err)
	})

	t.Run("Unknown source", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Kustomize: &v1alpha1.ApplicationSourceKustomize{
						Images: v1alpha1.KustomizeImages{
							"foo",
							"bar",
						},
					},
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeDirectory,
			},
		}

		_, err := marshalParamsOverride(&app, nil)
		assert.Error(t, err)
	})
}

func Test_SetHelmValue(t *testing.T) {
	t.Run("Update existing Key", func(t *testing.T) {
		expected := `
image:
  attributes:
    name: repo-name
    tag: v2.0.0
`

		inputData := []byte(`
image:
  attributes:
    name: repo-name
    tag: v1.0.0
`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})

	t.Run("Update Key with dots", func(t *testing.T) {
		expected := `image.attributes.tag: v2.0.0`

		inputData := []byte(`image.attributes.tag: v1.0.0`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})

	t.Run("Key not found", func(t *testing.T) {
		expected := `
image:
  attributes:
    name: repo-name
    tag: v2.0.0
`

		inputData := []byte(`
image:
  attributes:
    name: repo-name
`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})

	t.Run("Root key not found", func(t *testing.T) {
		expected := `
name: repo-name
tag: v2.0.0
`

		inputData := []byte(`name: repo-name`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})

	t.Run("Empty values with deep key", func(t *testing.T) {
		// this uses inline syntax because the input data
		// needed is an empty map, which can only be expressed as {}.
		expected := `{image: {attributes: {tag: v2.0.0}}}`

		inputData := []byte(`{}`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})

	t.Run("Unexpected type for key", func(t *testing.T) {
		inputData := []byte(`
image:
  attributes: v1.0.0
`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		assert.Error(t, err)
		assert.Equal(t, "unexpected type ScalarNode for key attributes", err.Error())
	})

	t.Run("Aliases, comments, and multiline strings are preserved", func(t *testing.T) {
		expected := `
image:
  attributes:
    name: &repo repo-name
    tag: v2.0.0
    # this is a comment
    multiline: |
      one
      two
      three
    alias: *repo
`

		inputData := []byte(`
image:
  attributes:
    name: &repo repo-name
    tag: v1.0.0
    # this is a comment
    multiline: |
      one
      two
      three
    alias: *repo
`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})

	t.Run("Aliases to mappings are followed", func(t *testing.T) {
		expected := `
global:
  attributes: &attrs
    name: &repo repo-name
    tag: v2.0.0
image:
  attributes: *attrs
`

		inputData := []byte(`
global:
  attributes: &attrs
    name: &repo repo-name
    tag: v1.0.0
image:
  attributes: *attrs
`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})

	t.Run("Aliases to scalars are followed", func(t *testing.T) {
		expected := `
image:
  attributes:
    name: repo-name
    version: &ver v2.0.0
    tag: *ver
`

		inputData := []byte(`
image:
  attributes:
    name: repo-name
    version: &ver v1.0.0
    tag: *ver
`)
		input := yaml.Node{}
		err := yaml.Unmarshal(inputData, &input)
		require.NoError(t, err)

		key := "image.attributes.tag"
		value := "v2.0.0"

		err = setHelmValue(&input, key, value)
		require.NoError(t, err)

		output, err := marshalWithIndent(&input, defaultIndent)
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSpace(expected), strings.TrimSpace(string(output)))
	})
}

func Test_GetWriteBackConfig(t *testing.T) {
	t.Run("Valid write-back config - git", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/git-branch":        "mybranch:mytargetbranch",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackGit)
		assert.Equal(t, "mybranch", wbc.GitBranch)
		assert.Equal(t, "mytargetbranch", wbc.GitWriteBranch)
	})

	t.Run("Valid git branch name determiniation - write branch only", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/git-branch":        ":mytargetbranch",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, "", wbc.GitBranch)
		assert.Equal(t, "mytargetbranch", wbc.GitWriteBranch)
	})

	t.Run("Valid git branch name determiniation - base branch only", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/git-branch":        "mybranch",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, "mybranch", wbc.GitBranch)
		assert.Equal(t, "", wbc.GitWriteBranch)
	})

	t.Run("Valid write-back config - argocd", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "argocd",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackApplication)
	})

	t.Run("kustomization write-back config", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/write-back-target": "kustomization:../bar",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Path:           "config/foo",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackGit)
		assert.Equal(t, wbc.KustomizeBase, "config/bar")
	})

	t.Run("helmvalues write-back config with relative path", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/write-back-target": "helmvalues:../bar/values.yaml",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Path:           "config/foo",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackGit)
		assert.Equal(t, wbc.Target, "config/bar/values.yaml")
	})

	t.Run("helmvalues write-back config without path", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/write-back-target": "helmvalues",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Path:           "config/foo",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackGit)
		assert.Equal(t, wbc.Target, "config/foo/values.yaml")
	})

	t.Run("helmvalues write-back config with absolute path", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/write-back-target": "helmvalues:/helm/app/values.yaml",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Path:           "config/foo",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackGit)
		assert.Equal(t, wbc.Target, "helm/app/values.yaml")
	})

	t.Run("Plain write back target without kustomize or helm types", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/write-back-target": "target/folder/app-parameters.yaml",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Path:           "config/foo",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackGit)
		assert.Equal(t, wbc.Target, "target/folder/app-parameters.yaml")
	})

	t.Run("Unknown credentials", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:error:argocd-image-updater/git-creds",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
					Path:           "config/foo",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeHelm,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		_, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		assert.Error(t, err)
	})

	t.Run("Default write-back config - argocd", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list": "nginx",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.NotNil(t, wbc)
		assert.Equal(t, wbc.Method, WriteBackApplication)
	})

	t.Run("Invalid write-back config - unknown", func(t *testing.T) {
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "unknown",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}

		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeKubeClient(),
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.Error(t, err)
		require.Nil(t, wbc)
	})

}

func Test_GetGitCreds(t *testing.T) {
	t.Run("HTTP user creds from a secret", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"username": []byte("foo"),
			"password": []byte("bar"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:secret:argocd-image-updater/git-creds",
					"argocd-image-updater.argoproj.io/git-credentials":   "argocd-image-updater/git-creds",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)

		creds, err := wbc.GetCreds(&app)
		require.NoError(t, err)
		require.NotNil(t, creds)
		// Must have HTTPS user creds
		_, ok := creds.(git.HTTPSCreds)
		require.True(t, ok)
	})

	t.Run("HTTP GitHub App creds from a secret", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"githubAppID":             []byte("12345678"),
			"githubAppInstallationID": []byte("87654321"),
			"githubAppPrivateKey":     []byte("foo"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:secret:argocd-image-updater/git-creds",
					"argocd-image-updater.argoproj.io/git-credentials":   "argocd-image-updater/git-creds",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)

		creds, err := wbc.GetCreds(&app)
		require.NoError(t, err)
		require.NotNil(t, creds)
		// Must have HTTPS GitHub App creds
		_, ok := creds.(git.GitHubAppCreds)
		require.True(t, ok)

		// invalid secrete data in GitHub App creds
		invalidSecretEntries := []map[string][]byte{
			{ // missing githubAppPrivateKey
				"githubAppID":             []byte("12345678"),
				"githubAppInstallationID": []byte("87654321"),
			}, { // missing githubAppInstallationID
				"githubAppID":         []byte("12345678"),
				"githubAppPrivateKey": []byte("foo"),
			}, { // missing githubAppID
				"githubAppInstallationID": []byte("87654321"),
				"githubAppPrivateKey":     []byte("foo"),
			}, { // ID should be a number
				"githubAppID":             []byte("NaN"),
				"githubAppInstallationID": []byte("87654321"),
				"githubAppPrivateKey":     []byte("foo"),
			}, {
				"githubAppID":             []byte("12345678"),
				"githubAppInstallationID": []byte("NaN"),
				"githubAppPrivateKey":     []byte("foo"),
			},
		}
		for _, secretEntry := range invalidSecretEntries {
			secret = fixture.NewSecret("argocd-image-updater", "git-creds", secretEntry)
			kubeClient = kube.ImageUpdaterKubernetesClient{
				KubeClient: &registryKube.KubernetesClient{
					Clientset: fake.NewFakeClientsetWithResources(secret),
				},
			}
			wbc, err = getWriteBackConfig(&app, &kubeClient, &argoClient)
			require.NoError(t, err)
			_, err = wbc.GetCreds(&app)
			require.Error(t, err)
		}
	})

	t.Run("SSH creds from a secret", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"sshPrivateKey": []byte("foo"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:secret:argocd-image-updater/git-creds",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "git@example.com:example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)

		creds, err := wbc.GetCreds(&app)
		require.NoError(t, err)
		require.NotNil(t, creds)
		// Must have SSH creds
		_, ok := creds.(git.SSHCreds)
		require.True(t, ok)
	})

	t.Run("HTTP creds from Argo CD settings", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd", "git-creds", map[string][]byte{
			"username": []byte("foo"),
			"password": []byte("bar"),
		})
		configMap := fixture.NewConfigMap("argocd", "argocd-cm", map[string]string{
			"repositories": `
- url: https://example.com/example
  passwordSecret:
    name: git-creds
    key: password
  usernameSecret:
    name: git-creds
    key: username`,
		})
		fixture.AddPartOfArgoCDLabel(secret, configMap)

		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret, configMap),
				Namespace: "argocd",
			},
		}
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:repocreds",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)

		creds, err := wbc.GetCreds(&app)
		require.NoError(t, err)
		require.NotNil(t, creds)
		// Must have HTTPS creds
		_, ok := creds.(git.HTTPSCreds)
		require.True(t, ok)
	})

	t.Run("Invalid fields in secret", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"sshPrivateKex": []byte("foo"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:secret:argocd-image-updater/git-creds",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "git@example.com:example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)

		creds, err := wbc.GetCreds(&app)
		require.Error(t, err)
		require.Nil(t, creds)
	})

	t.Run("Invalid secret reference", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"sshPrivateKey": []byte("foo"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:secret:nonexist",
					"argocd-image-updater.argoproj.io/git-credentials":   "",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "git@example.com:example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)

		creds, err := wbc.GetCreds(&app)
		require.Error(t, err)
		require.Nil(t, creds)
	})

	t.Run("Secret does not exist", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"sshPrivateKey": []byte("foo"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}
		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git",
					"argocd-image-updater.argoproj.io/git-credentials":   "argocd-image-updater/nonexist",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "git@example.com:example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)

		creds, err := wbc.GetCreds(&app)
		require.Error(t, err)
		require.Nil(t, creds)
	})

	t.Run("SSH creds from Argo CD settings with Helm Chart repoURL", func(t *testing.T) {
		argoClient := argomock.ArgoCD{}
		argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
		secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
			"sshPrivateKey": []byte("foo"),
		})
		kubeClient := kube.ImageUpdaterKubernetesClient{
			KubeClient: &registryKube.KubernetesClient{
				Clientset: fake.NewFakeClientsetWithResources(secret),
			},
		}

		app := v1alpha1.Application{
			ObjectMeta: v1.ObjectMeta{
				Name: "testapp",
				Annotations: map[string]string{
					"argocd-image-updater.argoproj.io/image-list":        "nginx",
					"argocd-image-updater.argoproj.io/write-back-method": "git:secret:argocd-image-updater/git-creds",
					"argocd-image-updater.argoproj.io/git-repository":    "git@github.com:example/example.git",
				},
			},
			Spec: v1alpha1.ApplicationSpec{
				Source: &v1alpha1.ApplicationSource{
					RepoURL:        "https://example-helm-repo.com/example",
					TargetRevision: "main",
				},
			},
			Status: v1alpha1.ApplicationStatus{
				SourceType: v1alpha1.ApplicationSourceTypeKustomize,
			},
		}

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		require.Equal(t, wbc.GitRepo, "git@github.com:example/example.git")

		creds, err := wbc.GetCreds(&app)
		require.NoError(t, err)
		require.NotNil(t, creds)
		// Must have SSH creds
		_, ok := creds.(git.SSHCreds)
		require.True(t, ok)
	})
}

func Test_CommitUpdates(t *testing.T) {
	argoClient := argomock.ArgoCD{}
	argoClient.On("UpdateSpec", mock.Anything, mock.Anything).Return(nil, nil)
	secret := fixture.NewSecret("argocd-image-updater", "git-creds", map[string][]byte{
		"sshPrivateKey": []byte("foo"),
	})
	kubeClient := kube.ImageUpdaterKubernetesClient{
		KubeClient: &registryKube.KubernetesClient{
			Clientset: fake.NewFakeClientsetWithResources(secret),
		},
	}
	app := v1alpha1.Application{
		ObjectMeta: v1.ObjectMeta{
			Name: "testapp",
			Annotations: map[string]string{
				"argocd-image-updater.argoproj.io/image-list":        "nginx",
				"argocd-image-updater.argoproj.io/write-back-method": "git:secret:argocd-image-updater/git-creds",
			},
		},
		Spec: v1alpha1.ApplicationSpec{
			Source: &v1alpha1.ApplicationSource{
				RepoURL:        "git@example.com:example",
				TargetRevision: "main",
			},
		},
		Status: v1alpha1.ApplicationStatus{
			SourceType: v1alpha1.ApplicationSourceTypeKustomize,
		},
	}

	t.Run("Good commit to target revision", func(t *testing.T) {
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "main", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock

		err = commitChanges(&app, wbc, nil)
		assert.NoError(t, err)
	})

	t.Run("Good commit to configured branch", func(t *testing.T) {
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mybranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		wbc.GitBranch = "mybranch"

		err = commitChanges(&app, wbc, nil)
		assert.NoError(t, err)
	})

	t.Run("Good commit to default branch", func(t *testing.T) {
		app := app.DeepCopy()
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mydefaultbranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""

		err = commitChanges(app, wbc, nil)
		assert.NoError(t, err)
	})

	t.Run("Good commit to different than base branch", func(t *testing.T) {
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Branch", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)

		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		wbc.GitBranch = "mydefaultbranch"
		wbc.GitWriteBranch = "image-updater{{range .Images}}-{{.Name}}-{{.NewTag}}{{end}}"

		cl := []ChangeEntry{
			{
				Image:  image.NewFromIdentifier("foo/bar"),
				OldTag: tag.NewImageTag("1.0", time.Now(), ""),
				NewTag: tag.NewImageTag("1.1", time.Now(), ""),
			},
		}
		gitMock.On("Checkout", TemplateBranchName(wbc.GitWriteBranch, cl), mock.Anything).Return(nil)

		err = commitChanges(&app, wbc, cl)
		assert.NoError(t, err)
	})

	t.Run("Good commit to helm override", func(t *testing.T) {
		app := app.DeepCopy()
		app.Status.SourceType = "Helm"
		app.Spec.Source.Helm = &v1alpha1.ApplicationSourceHelm{Parameters: []v1alpha1.HelmParameter{
			{Name: "bar", Value: "bar", ForceString: true},
			{Name: "baz", Value: "baz", ForceString: true},
		}}
		gitMock, dir, cleanup := mockGit(t)
		defer cleanup()
		of := filepath.Join(dir, ".argocd-source-testapp.yaml")
		assert.NoError(t, os.WriteFile(of, []byte(`
helm:
  parameters:
  - name: foo
    value: foo
    forcestring: true
`), os.ModePerm))

		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mydefaultbranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""

		err = commitChanges(app, wbc, nil)
		assert.NoError(t, err)
		override, err := os.ReadFile(of)
		assert.NoError(t, err)
		assert.YAMLEq(t, `
helm:
  parameters:
  - name: foo
    value: foo
    forcestring: true
  - name: bar
    value: bar
    forcestring: true
  - name: baz
    value: baz
    forcestring: true
`, string(override))
	})

	t.Run("Good commit to helm override with argocd namespace", func(t *testing.T) {
		kubeClient.KubeClient.Namespace = "argocd"
		app := app.DeepCopy()
		app.Status.SourceType = "Helm"
		app.ObjectMeta.Namespace = "argocd"
		app.Spec.Source.Helm = &v1alpha1.ApplicationSourceHelm{Parameters: []v1alpha1.HelmParameter{
			{Name: "bar", Value: "bar", ForceString: true},
			{Name: "baz", Value: "baz", ForceString: true},
		}}
		gitMock, dir, cleanup := mockGit(t)
		defer cleanup()
		of := filepath.Join(dir, ".argocd-source-testapp.yaml")
		assert.NoError(t, os.WriteFile(of, []byte(`
helm:
  parameters:
  - name: foo
    value: foo
    forcestring: true
`), os.ModePerm))

		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mydefaultbranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""

		err = commitChanges(app, wbc, nil)
		assert.NoError(t, err)
		override, err := os.ReadFile(of)
		assert.NoError(t, err)
		assert.YAMLEq(t, `
helm:
  parameters:
  - name: foo
    value: foo
    forcestring: true
  - name: bar
    value: bar
    forcestring: true
  - name: baz
    value: baz
    forcestring: true
`, string(override))
	})

	t.Run("Good commit to helm override with another namespace", func(t *testing.T) {
		kubeClient.KubeClient.Namespace = "argocd"
		app := app.DeepCopy()
		app.Status.SourceType = "Helm"
		app.ObjectMeta.Namespace = "testNS"
		app.Spec.Source.Helm = &v1alpha1.ApplicationSourceHelm{Parameters: []v1alpha1.HelmParameter{
			{Name: "bar", Value: "bar", ForceString: true},
			{Name: "baz", Value: "baz", ForceString: true},
		}}
		gitMock, dir, cleanup := mockGit(t)
		defer cleanup()
		of := filepath.Join(dir, ".argocd-source-testNS_testapp.yaml")
		assert.NoError(t, os.WriteFile(of, []byte(`
helm:
  parameters:
  - name: foo
    value: foo
    forcestring: true
`), os.ModePerm))

		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mydefaultbranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""

		err = commitChanges(app, wbc, nil)
		assert.NoError(t, err)
		override, err := os.ReadFile(of)
		assert.NoError(t, err)
		assert.YAMLEq(t, `
helm:
  parameters:
  - name: foo
    value: foo
    forcestring: true
  - name: bar
    value: bar
    forcestring: true
  - name: baz
    value: baz
    forcestring: true
`, string(override))
	})

	t.Run("Good commit to kustomization", func(t *testing.T) {
		app := app.DeepCopy()
		app.Annotations[common.WriteBackTargetAnnotation] = "kustomization"
		app.Spec.Source.Kustomize = &v1alpha1.ApplicationSourceKustomize{Images: v1alpha1.KustomizeImages{"foo=bar", "bar=baz:123"}}
		gitMock, dir, cleanup := mockGit(t)
		defer cleanup()
		kf := filepath.Join(dir, "kustomization.yml")
		assert.NoError(t, os.WriteFile(kf, []byte(`
kind: Kustomization
apiVersion: kustomize.config.k8s.io/v1beta1

replacements: []
`), os.ModePerm))

		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mydefaultbranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""

		err = commitChanges(app, wbc, nil)
		assert.NoError(t, err)
		kust, err := os.ReadFile(kf)
		assert.NoError(t, err)
		assert.YAMLEq(t, `
kind: Kustomization
apiVersion: kustomize.config.k8s.io/v1beta1
images:
  - name: foo
    newName: bar
  - name: bar
    newName: baz
    newTag: "123"

replacements: []
`, string(kust))

		// test the merge case too
		app.Spec.Source.Kustomize.Images = v1alpha1.KustomizeImages{"foo:123", "bar=qux"}
		err = commitChanges(app, wbc, nil)
		assert.NoError(t, err)
		kust, err = os.ReadFile(kf)
		assert.NoError(t, err)
		assert.YAMLEq(t, `
kind: Kustomization
apiVersion: kustomize.config.k8s.io/v1beta1
images:
  - name: foo
    newTag: "123"
  - name: bar
    newName: qux

replacements: []
`, string(kust))
	})

	t.Run("Good commit with author information", func(t *testing.T) {
		app := app.DeepCopy()
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mydefaultbranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)
		gitMock.On("Config", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "someone", "someone@example.com")
		}).Return(nil)
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""
		wbc.GitCommitUser = "someone"
		wbc.GitCommitEmail = "someone@example.com"

		err = commitChanges(app, wbc, nil)
		assert.NoError(t, err)
	})

	t.Run("Cannot set author information", func(t *testing.T) {
		app := app.DeepCopy()
		gitMock := &gitmock.Client{}
		gitMock.On("Init").Return(nil)
		gitMock.On("Root").Return(t.TempDir())
		gitMock.On("ShallowFetch", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Checkout", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "mydefaultbranch", false)
		}).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("mydefaultbranch", nil)
		gitMock.On("Config", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			args.Assert(t, "someone", "someone@example.com")
		}).Return(fmt.Errorf("could not configure git"))
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""
		wbc.GitCommitUser = "someone"
		wbc.GitCommitEmail = "someone@example.com"

		err = commitChanges(app, wbc, nil)
		assert.Errorf(t, err, "could not configure git")
	})

	t.Run("Cannot init", func(t *testing.T) {
		gitMock := &gitmock.Client{}
		gitMock.On("Init").Return(fmt.Errorf("cannot init"))
		gitMock.On("ShallowFetch", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Checkout", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock

		err = commitChanges(&app, wbc, nil)
		assert.Errorf(t, err, "cannot init")
	})

	t.Run("Cannot fetch", func(t *testing.T) {
		gitMock := &gitmock.Client{}
		gitMock.On("Init").Return(nil)
		gitMock.On("ShallowFetch", mock.Anything, mock.Anything).Return(fmt.Errorf("cannot fetch"))
		gitMock.On("Checkout", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock

		err = commitChanges(&app, wbc, nil)
		assert.Errorf(t, err, "cannot init")
	})
	t.Run("Cannot checkout", func(t *testing.T) {
		gitMock := &gitmock.Client{}
		gitMock.On("Init").Return(nil)
		gitMock.On("ShallowFetch", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Checkout", mock.Anything, mock.Anything).Return(fmt.Errorf("cannot checkout"))
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock

		err = commitChanges(&app, wbc, nil)
		assert.Errorf(t, err, "cannot checkout")
	})

	t.Run("Cannot commit", func(t *testing.T) {
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Checkout", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("cannot commit"))
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock

		err = commitChanges(&app, wbc, nil)
		assert.Errorf(t, err, "cannot commit")
	})

	t.Run("Cannot push", func(t *testing.T) {
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Checkout", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(fmt.Errorf("cannot push"))
		wbc, err := getWriteBackConfig(&app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock

		err = commitChanges(&app, wbc, nil)
		assert.Errorf(t, err, "cannot push")
	})

	t.Run("Cannot resolve default branch", func(t *testing.T) {
		app := app.DeepCopy()
		gitMock, _, cleanup := mockGit(t)
		defer cleanup()
		gitMock.On("Checkout", mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Add", mock.Anything).Return(nil)
		gitMock.On("Commit", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		gitMock.On("SymRefToBranch", mock.Anything).Return("", fmt.Errorf("failed to resolve ref"))
		wbc, err := getWriteBackConfig(app, &kubeClient, &argoClient)
		require.NoError(t, err)
		wbc.GitClient = gitMock
		app.Spec.Source.TargetRevision = "HEAD"
		wbc.GitBranch = ""

		err = commitChanges(app, wbc, nil)
		assert.Errorf(t, err, "failed to resolve ref")
	})
}

func Test_parseKustomizeBase(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		target   string
		path     string
	}{
		{"default", ".", "kustomization", ""},
		{"explicit default", ".", "kustomization:.", "."},
		{"default path, explicit target", ".", "kustomization:.", ""},
		{"default target with path", "foo/bar", "kustomization", "foo/bar"},
		{"default both", ".", "kustomization", ""},
		{"absolute path", "foo", "kustomization:/foo", "bar"},
		{"relative path", "bar/foo", "kustomization:foo", "bar"},
		{"sibling path", "bar/baz", "kustomization:../baz", "bar/foo"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, parseKustomizeBase(tt.target, tt.path))
		})
	}
}

func Test_parseTarget(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		target   string
		path     string
	}{
		{"default", "values.yaml", "helmvalues", ""},
		{"explicit default", "values.yaml", "helmvalues:./values.yaml", "."},
		{"default path, explicit target", "values.yaml", "helmvalues:./values.yaml", ""},
		{"default target with path", "foo/bar/values.yaml", "helmvalues", "foo/bar"},
		{"default both", "values.yaml", "helmvalues", ""},
		{"absolute path", "foo/app-values.yaml", "helmvalues:/foo/app-values.yaml", "bar"},
		{"relative path", "bar/foo/app-values.yaml", "helmvalues:foo/app-values.yaml", "bar"},
		{"sibling path", "bar/baz/app-values.yaml", "helmvalues:../baz/app-values.yaml", "bar/foo"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, parseTarget(tt.target, tt.path))
		})
	}
}

func mockGit(t *testing.T) (gitMock *gitmock.Client, dir string, cleanup func()) {
	dir, err := os.MkdirTemp("", "wb-kust")
	assert.NoError(t, err)
	gitMock = &gitmock.Client{}
	gitMock.On("Root").Return(dir)
	gitMock.On("Init").Return(nil)
	gitMock.On("ShallowFetch", mock.Anything, mock.Anything).Return(nil)
	return gitMock, dir, func() {
		_ = os.RemoveAll(dir)
	}
}

func Test_GetRepositoryLock(t *testing.T) {
	state := NewSyncIterationState()

	// Test case 1: Get lock for a repository that doesn't exist in the state
	repo1 := "repo1"
	lock1 := state.GetRepositoryLock(repo1)
	require.NotNil(t, lock1)
	require.Equal(t, lock1, state.repositoryLocks[repo1])

	// Test case 2: Get lock for the same repository again, should return the same lock
	lock2 := state.GetRepositoryLock(repo1)
	require.Equal(t, lock1, lock2)

	// Test case 3: Get lock for a different repository, should return a different lock
	repo2 := "repo2"
	lock3 := state.GetRepositoryLock(repo2)
	require.NotNil(t, lock3)
	require.NotNil(t, state.repositoryLocks[repo2])
	require.Equal(t, lock3, state.repositoryLocks[repo2])
}

func Test_mergeKustomizeOverride(t *testing.T) {
	tests := []struct {
		name     string
		existing v1alpha1.KustomizeImages
		new      v1alpha1.KustomizeImages
		expected v1alpha1.KustomizeImages
	}{
		{"with-tag", []v1alpha1.KustomizeImage{"nginx:foo"},
			[]v1alpha1.KustomizeImage{"nginx:foo"},
			[]v1alpha1.KustomizeImage{"nginx:foo"}},
		{"no-tag", []v1alpha1.KustomizeImage{"nginx:foo"},
			[]v1alpha1.KustomizeImage{"nginx"},
			[]v1alpha1.KustomizeImage{"nginx:foo"}},
		{"with-tag-1", []v1alpha1.KustomizeImage{"nginx"},
			[]v1alpha1.KustomizeImage{"nginx:latest"},
			[]v1alpha1.KustomizeImage{"nginx:latest"}},
		{"with-tag-sha", []v1alpha1.KustomizeImage{"nginx:latest"},
			[]v1alpha1.KustomizeImage{"nginx:latest@sha256:91734281c0ebfc6f1aea979cffeed5079cfe786228a71cc6f1f46a228cde6e34"},
			[]v1alpha1.KustomizeImage{"nginx:latest@sha256:91734281c0ebfc6f1aea979cffeed5079cfe786228a71cc6f1f46a228cde6e34"}},

		{"2-images", []v1alpha1.KustomizeImage{"nginx:latest",
			"bitnami/nginx:latest@sha256:1a2fe3f9f6d1d38d5a7ee35af732fdb7d15266ec3dbc79bbc0355742cd24d3ec"},
			[]v1alpha1.KustomizeImage{"nginx:latest@sha256:91734281c0ebfc6f1aea979cffeed5079cfe786228a71cc6f1f46a228cde6e34",
				"bitnami/nginx@sha256:1a2fe3f9f6d1d38d5a7ee35af732fdb7d15266ec3dbc79bbc0355742cd24d3ec"},
			[]v1alpha1.KustomizeImage{"nginx:latest@sha256:91734281c0ebfc6f1aea979cffeed5079cfe786228a71cc6f1f46a228cde6e34",
				"bitnami/nginx:latest@sha256:1a2fe3f9f6d1d38d5a7ee35af732fdb7d15266ec3dbc79bbc0355742cd24d3ec"}},

		{"with-registry", []v1alpha1.KustomizeImage{"quay.io/nginx:latest"},
			[]v1alpha1.KustomizeImage{"quay.io/nginx:latest"},
			[]v1alpha1.KustomizeImage{"quay.io/nginx:latest"}},
		{"with-registry-1", []v1alpha1.KustomizeImage{"quay.io/nginx:latest"},
			[]v1alpha1.KustomizeImage{"docker.io/nginx:latest"},
			[]v1alpha1.KustomizeImage{"docker.io/nginx:latest", "quay.io/nginx:latest"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existingImages := kustomizeOverride{
				Kustomize: kustomizeImages{
					Images: &tt.existing,
				},
			}
			newImages := kustomizeOverride{
				Kustomize: kustomizeImages{
					Images: &tt.new,
				},
			}
			expectedImages := kustomizeOverride{
				Kustomize: kustomizeImages{
					Images: &tt.expected,
				},
			}

			mergeKustomizeOverride(&existingImages, &newImages)
			assert.ElementsMatch(t, *expectedImages.Kustomize.Images, *existingImages.Kustomize.Images)
		})
	}
}
