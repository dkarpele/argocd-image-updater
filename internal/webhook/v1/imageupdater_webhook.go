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

package v1

import (
	"context"
	api "github.com/argoproj-labs/argocd-image-updater/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var imageupdaterlog = logf.Log.WithName("imageupdater-resource")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&api.ImageUpdater{}).
		WithValidator(&ImageUpdaterCustomValidator{}).
		Complete()
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-argocd-image-updater-argoproj-io-v1alpha1-imageupdater,mutating=false,failurePolicy=fail,sideEffects=None,groups=argocd-image-updater.argoproj.io,resources=imageupdaters,verbs=create;update,versions=v1alpha1,name=vimageupdater.kb.io,admissionReviewVersions=v1

type ImageUpdaterCustomValidator struct {
	res api.ImageUpdater
	// TODO(user): Add more fields as needed for validation
}

func (i ImageUpdaterCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	//TODO implement me
	imageupdaterlog.Info("implement me")
	return
}

func (i ImageUpdaterCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	//TODO implement me
	imageupdaterlog.Info("implement me")
	return
}

func (i ImageUpdaterCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	//TODO implement me
	imageupdaterlog.Info("implement me")
	return
}

var _ webhook.CustomValidator = &ImageUpdaterCustomValidator{}
