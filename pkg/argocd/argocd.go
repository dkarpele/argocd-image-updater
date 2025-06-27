package argocd

import (
	"context"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"slices"

	iuapi "github.com/argoproj-labs/argocd-image-updater/api/v1alpha1"

	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	"github.com/argoproj-labs/argocd-image-updater/pkg/common"
	"github.com/argoproj-labs/argocd-image-updater/pkg/metrics"
	registryCommon "github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/common"
	"github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/image"
	"github.com/argoproj-labs/argocd-image-updater/registry-scanner/pkg/log"

	argocdclient "github.com/argoproj/argo-cd/v2/pkg/apiclient"
	"github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	argocdapi "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
)

// Kubernetes based client
type k8sClient struct {
	ctrlClient ctrlclient.Client
}

// GetApplication retrieves a single application by its name and namespace.
// TODO: the function will be probably updated to use `NamePattern` in GITOPS-7119
func (client *k8sClient) GetApplication(ctx context.Context, appNamespace string, appName string) (*argocdapi.Application, error) {
	app := &argocdapi.Application{}

	if err := client.ctrlClient.Get(ctx, types.NamespacedName{Namespace: appNamespace, Name: appName}, app); err != nil {
		return nil, err
	}
	return app, nil
}

// GetApplicationInAllNamespaces
// TODO: the function will be probably used when we implement `NamePattern` in GITOPS-7119
// It has 0 usages now.
func (client *k8sClient) GetApplicationInAllNamespaces(appName string) (*argocdapi.Application, error) {
	appList, err := client.ListApplications(context.TODO(), nil)
	if err != nil {
		return nil, fmt.Errorf("error listing applications: %w", err)
	}

	// Filter applications by name using nameMatchesPatterns
	var matchedApps []argocdapi.Application

	for _, app := range appList {
		log.Debugf("Found application: %s in namespace %s", app.Name, app.Namespace)
		if nameMatchesPatterns(app.Name, []string{appName}) {
			log.Debugf("Application %s matches the pattern", app.Name)
			matchedApps = append(matchedApps, app)
		}
	}

	if len(matchedApps) == 0 {
		return nil, fmt.Errorf("application %s not found", appName)
	}

	if len(matchedApps) > 1 {
		return nil, fmt.Errorf("multiple applications found matching %s", appName)
	}

	// Retrieve the application in the specified namespace
	return &matchedApps[0], nil
}

// ListApplications lists all applications for the current ImageUpdater CR in the namespace.
// TODO: LabelSelector was not implemented here. It will be done in a separate task GITOPS-7119
func (client *k8sClient) ListApplications(ctx context.Context, cr *iuapi.ImageUpdater) ([]argocdapi.Application, error) {
	log := log.LoggerFromContext(ctx)

	// A list to hold the successfully found applications.
	foundApps := make([]argocdapi.Application, 0)
	// A map to prevent processing the same application name twice if it appears in multiple refs.
	seenApps := make(map[string]bool)
	// The target namespace is defined once in the spec.
	targetNamespace := cr.Spec.Namespace

	// Iterate through each application reference in the spec.
	for _, appRef := range cr.Spec.ApplicationRefs {
		// We are now treating NamePattern as an exact name.
		appName := appRef.NamePattern

		appKey := fmt.Sprintf("%s/%s", targetNamespace, appName)
		if seenApps[appKey] {
			continue // Already fetched this app, skip to the next ref.
		}

		log.Debugf("Attempting to fetch application '%s' in namespace '%s'", appName, targetNamespace)
		app, err := client.GetApplication(ctx, targetNamespace, appName)

		if err != nil {
			if errors.IsNotFound(err) {
				log.Warnf("Application '%s' in namespace '%s' specified in ImageUpdater '%s' was not found, skipping.", appName, targetNamespace, cr.Name)
				seenApps[appKey] = true // Mark as seen so we don't try again.
				continue
			}
			return nil, fmt.Errorf("failed to get application '%s' in namespace '%s': %w", appName, targetNamespace, err)
		}
		log.Debugf("Application '%s' in namespace '%s' found", appName, targetNamespace)
		foundApps = append(foundApps, *app)
		seenApps[appKey] = true
	}

	log.Debugf("Applications listed: %d", len(foundApps))
	return foundApps, nil
}

// UpdateSpec updates the spec for given application
func (client *k8sClient) UpdateSpec(ctx context.Context, spec *application.ApplicationUpdateSpecRequest) (*argocdapi.ApplicationSpec, error) {
	log := log.LoggerFromContext(ctx)
	app := &argocdapi.Application{}
	var err error

	// Use RetryOnConflict to handle potential conflicts gracefully.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// 1. Get the latest version of the Application within the retry loop.
		app, err = client.GetApplication(ctx, spec.GetAppNamespace(), spec.GetName())
		if err != nil {
			log.Errorf("could not get application: %s, error: %v", spec.GetName(), err)
			return err
		}

		app.Spec = *spec.Spec

		// 3. Attempt to update the object. If there is a conflict,
		//    RetryOnConflict will automatically re-fetch and re-apply the changes.
		return client.ctrlClient.Update(ctx, app)
	})

	if err != nil {
		log.Errorf("could not update application spec for %s: %v", spec.GetName(), err)
		return nil, fmt.Errorf("failed to update application spec for %s after retries: %w", spec.GetName(), err)
	}

	log.Infof("Successfully updated application spec for %s", spec.GetName())
	return &app.Spec, nil
}

type K8SClientOptions struct {
	AppNamespace string
}

// NewK8SClient creates a new kubernetes client to interact with kubernetes api-server.
func NewK8SClient(ctrlClient ctrlclient.Client) (ArgoCD, error) {
	return &k8sClient{
		ctrlClient: ctrlClient,
	}, nil
}

// Native
type argoCD struct {
	Client argocdclient.Client
}

// ArgoCD is the interface for accessing Argo CD functions we need
//
//go:generate mockery --name ArgoCD --output ./mocks --outpkg mocks
type ArgoCD interface {
	GetApplication(ctx context.Context, appNamespace string, appName string) (*argocdapi.Application, error)
	ListApplications(ctx context.Context, cr *iuapi.ImageUpdater) ([]argocdapi.Application, error)
	UpdateSpec(ctx context.Context, spec *application.ApplicationUpdateSpecRequest) (*argocdapi.ApplicationSpec, error)
	FilterApplicationsForUpdate(ctx context.Context, cr *iuapi.ImageUpdater) (map[string]ApplicationImages, error)
}

// ApplicationType Type of the application
type ApplicationType int

const (
	ApplicationTypeUnsupported ApplicationType = 0
	ApplicationTypeHelm        ApplicationType = 1
	ApplicationTypeKustomize   ApplicationType = 2
)

// Basic wrapper struct for ArgoCD client options
type ClientOptions struct {
	ServerAddr      string
	Insecure        bool
	Plaintext       bool
	Certfile        string
	GRPCWeb         bool
	GRPCWebRootPath string
	AuthToken       string
}

// NewAPIClient creates a new API client for ArgoCD and connects to the ArgoCD
// API server.
func NewAPIClient(opts *ClientOptions) (ArgoCD, error) {

	envAuthToken := os.Getenv("ARGOCD_TOKEN")
	if envAuthToken != "" && opts.AuthToken == "" {
		opts.AuthToken = envAuthToken
	}

	rOpts := argocdclient.ClientOptions{
		ServerAddr:      opts.ServerAddr,
		PlainText:       opts.Plaintext,
		Insecure:        opts.Insecure,
		CertFile:        opts.Certfile,
		GRPCWeb:         opts.GRPCWeb,
		GRPCWebRootPath: opts.GRPCWebRootPath,
		AuthToken:       opts.AuthToken,
	}
	client, err := argocdclient.NewClient(&rOpts)
	if err != nil {
		return nil, err
	}
	return &argoCD{Client: client}, nil
}

type ApplicationImages struct {
	Application argocdapi.Application
	Images      image.ContainerImageList
}

// Will hold a list of applications with the images allowed to considered for
// update.
type ImageList map[string]ApplicationImages

// nameMatchesPatterns Matches a name against a list of patterns
func nameMatchesPatterns(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if nameMatchesPattern(name, p) {
			return true
		}
	}
	return false
}

// nameMatchesPattern Matches a name against a pattern
func nameMatchesPattern(name string, pattern string) bool {
	log.Tracef("Matching application name %s against pattern %s", name, pattern)

	m, err := filepath.Match(pattern, name)
	if err != nil {
		log.Warnf("Invalid application name pattern '%s': %v", pattern, err)
		return false
	}
	return m
}

// nameMatchesLabels checks if the given labels match the provided LabelSelector.
// It returns true if the selectors are nil (no filtering), or if all MatchLabels
// and MatchExpressions conditions are met.
func nameMatchesLabels(labels map[string]string, selectors *metav1.LabelSelector) bool {
	if selectors == nil {
		return true // No selectors means no filtering by labels
	}

	// MatchLabels: All must match
	if selectors.MatchLabels != nil {
		for key, value := range selectors.MatchLabels {
			if actualValue, ok := labels[key]; !ok || actualValue != value {
				return false
			}
		}
	}

	// MatchExpressions: All must match
	if selectors.MatchExpressions != nil {
		for _, expr := range selectors.MatchExpressions {
			switch expr.Operator {
			case metav1.LabelSelectorOpIn:
				if actualValue, ok := labels[expr.Key]; !ok || !slices.Contains(expr.Values, actualValue) {
					return false
				}
			case metav1.LabelSelectorOpNotIn:
				if actualValue, ok := labels[expr.Key]; ok && slices.Contains(expr.Values, actualValue) {
					return false
				}
			case metav1.LabelSelectorOpExists:
				if _, ok := labels[expr.Key]; !ok {
					return false
				}
			case metav1.LabelSelectorOpDoesNotExist:
				if _, ok := labels[expr.Key]; ok {
					return false
				}
			default:
				// Unknown operator, treat as no match to be safe
				return false
			}
		}
	}

	return true
}

// processExactNamePatternMatches processes all EXACT name matches
func (client *k8sClient) processExactNamePatternMatches(ctx context.Context, cr *iuapi.ImageUpdater) (map[string]ApplicationImages, error) {
	log := log.LoggerFromContext(ctx)

	if !areAllAppNamePatternsExactMatches(ctx, cr.Spec.ApplicationRefs) {
		log.Warnf("NamePatterns have wildcards in their names. Falling back to wildcard matching.")
		return nil, nil
	}

	var appsForUpdate = make(map[string]ApplicationImages)

	for _, applicationRef := range cr.Spec.ApplicationRefs {
		app, err := client.GetApplication(ctx, cr.Spec.Namespace, applicationRef.NamePattern)
		if err != nil {
			return nil, err
		}

		appNSName := fmt.Sprintf("%s/%s", cr.Spec.Namespace, applicationRef.NamePattern)
		sourceType := getApplicationSourceType(app)

		// Check for valid application type
		if !IsValidApplicationType(app) {
			log.Warnf("skipping app '%s' of type '%s' because it's not of supported source type", appNSName, sourceType)
			continue
		}

		log.Tracef("processing app '%s' of type '%v'", appNSName, sourceType)
		appImages := ApplicationImages{}
		appImages.Application = *app
		imageList := parseImageList(applicationRef.Images)
		appImages.Images = *imageList
		appsForUpdate[appNSName] = appImages
	}
	return appsForUpdate, nil
}

func areAllAppNamePatternsExactMatches(ctx context.Context, applicationRefs []iuapi.ApplicationRef) bool {
	log := log.LoggerFromContext(ctx)
	if len(applicationRefs) > 500 {
		log.Warnf("Too many name applicationRefs in the Image Updater CR. Skipping the exact matching check.")
		return false
	}
	for _, applicationRef := range applicationRefs {
		if strings.ContainsAny(applicationRef.NamePattern, "*?[]") {
			return false
		}
	}
	return true
}

func sortApplicationRefs(applicationRefs []iuapi.ApplicationRef) []iuapi.ApplicationRef {
	// Create a slice to hold the rules for sorting.
	sortedRefs := make([]iuapi.ApplicationRef, len(applicationRefs))
	copy(sortedRefs, applicationRefs) // Work on a copy

	// Sort the slice from most specific to least specific.
	slices.SortStableFunc(sortedRefs, func(a, b iuapi.ApplicationRef) int {
		scoreA := calculateSpecificity(a)
		scoreB := calculateSpecificity(b)

		// We want higher scores first (descending order).
		// A standard cmp function returns negative if a < b.
		// So, we reverse the comparison on the scores.
		if scoreA > scoreB {
			return -1 // A is more specific, so it should come first.
		}
		if scoreA < scoreB {
			return 1 // B is more specific, so it should come first.
		}

		// Scores are equal. Return 0 to tell the stable sort algorithm
		// to keep their original relative order.
		return 0
	})
	return sortedRefs
}

// calculateSpecificity computes a numerical score for an ApplicationRef to determine
// its precedence. A higher score means higher specificity.
func calculateSpecificity(applicationRef iuapi.ApplicationRef) int {
	score := 0
	pattern := applicationRef.NamePattern

	// 1. Check for an exact name match (highest precedence).
	// We define an exact match as not containing any glob wildcards.
	if !strings.ContainsAny(pattern, "*?[]") {
		score += 1_000_000
	}

	// 2. Add points for the number of literal characters in the pattern.
	// This makes "app-prod-*" more specific than "app-*".
	// We do this by removing wildcards and counting the length of what's left.
	literals := strings.ReplaceAll(pattern, "*", "")
	for _, c := range "?[]" {
		literals = strings.ReplaceAll(literals, string(c), "")
	}
	score += len(literals)

	// 3. Add a significant bonus if a label selector is present.
	if applicationRef.LabelSelectors != nil {
		score += 10_000

		// 4. Add smaller points for each label/expression in the selector.
		// This makes a more complex selector win over a simpler one.
		if applicationRef.LabelSelectors.MatchLabels != nil {
			score += len(applicationRef.LabelSelectors.MatchLabels) * 100
		}
		if applicationRef.LabelSelectors.MatchExpressions != nil {
			score += len(applicationRef.LabelSelectors.MatchExpressions) * 100
		}
	}

	return score
}

// FilterApplicationsForUpdate Retrieve a list of applications from ArgoCD that qualify for image updates
// Application needs either to be of type Kustomize or Helm and must have the
// correct annotation in order to be considered.
func (client *k8sClient) FilterApplicationsForUpdate(ctx context.Context, cr *iuapi.ImageUpdater) (map[string]ApplicationImages, error) {
	log := log.LoggerFromContext(ctx)

	if areAllAppNamePatternsExactMatches(ctx, cr.Spec.ApplicationRefs) {
		log.Debugf("All name patterns don't have wildcards. Getting all applications by thier names.")
		return client.processExactNamePatternMatches(ctx, cr)
	}

	allAppsInNamespace := &argocdapi.ApplicationList{}
	listOpts := []ctrlclient.ListOption{
		ctrlclient.InNamespace(cr.Spec.Namespace),
	}

	// Perform the list operation with the specified options.
	log.Infof("Listing all applications in target namespace: %s", cr.Spec.Namespace)
	if err := client.ctrlClient.List(ctx, allAppsInNamespace, listOpts...); err != nil {
		log.Errorf("Failed to list applications in namespace: %s, error: %v", cr.Spec.Namespace, err)
		return nil, err
	}

	var appsForUpdate = make(map[string]ApplicationImages)

	// Sort the slice from most specific to least specific.
	applicationRefsSorted := sortApplicationRefs(cr.Spec.ApplicationRefs)

	// For each app in the list, find its best matching rule from the CR.
	for _, app := range allAppsInNamespace.Items {
		appNSName := fmt.Sprintf("%s/%s", cr.Spec.Namespace, app.GetName())
		sourceType := getApplicationSourceType(&app)

		// Check for valid application type
		if !IsValidApplicationType(&app) {
			log.Warnf("skipping app '%s' of type '%s' because it's not of supported source type", appNSName, sourceType)
			continue
		}

		for _, applicationRef := range applicationRefsSorted {
			if nameMatchesPattern(app.Name, applicationRef.NamePattern) && nameMatchesLabels(app.Labels, applicationRef.LabelSelectors) {
				log.Tracef("processing app '%s' of type '%v'", appNSName, sourceType)
				imageList := &image.ContainerImageList{}
				appImages := ApplicationImages{}
				appImages.Application = app
				appImages.Images = *imageList
				appsForUpdate[appNSName] = appImages
				break
			}
		}

	}

	return appsForUpdate, nil
}

func parseImageList(images []iuapi.ImageConfig) *image.ContainerImageList {
	results := make(image.ContainerImageList, 0)

	for _, im := range images {
		img := image.NewFromIdentifier(im.Alias + "=" + im.ImageName)
		if kustomizeImage := im.ManifestTarget.Kustomize.Name; kustomizeImage != "" {
			img.KustomizeImage = image.NewFromIdentifier(kustomizeImage)
		}
		results = append(results, img)
	}

	return &results
}

// GetApplication gets the application named appName from Argo CD API
func (client *argoCD) GetApplication(ctx context.Context, appNamespace string, appName string) (*argocdapi.Application, error) {
	conn, appClient, err := client.Client.NewApplicationClient()
	metrics.Clients().IncreaseArgoCDClientRequest(client.Client.ClientOptions().ServerAddr, 1)
	if err != nil {
		metrics.Clients().IncreaseArgoCDClientError(client.Client.ClientOptions().ServerAddr, 1)
		return nil, err
	}
	defer conn.Close()

	metrics.Clients().IncreaseArgoCDClientRequest(client.Client.ClientOptions().ServerAddr, 1)
	app, err := appClient.Get(ctx, &application.ApplicationQuery{Name: &appName})
	if err != nil {
		metrics.Clients().IncreaseArgoCDClientError(client.Client.ClientOptions().ServerAddr, 1)
		return nil, err
	}

	return app, nil
}

// ListApplications returns a list of all application names that the API user
// has access to.
func (client *argoCD) ListApplications(ctx context.Context, cr *iuapi.ImageUpdater) ([]argocdapi.Application, error) {
	conn, appClient, err := client.Client.NewApplicationClient()
	metrics.Clients().IncreaseArgoCDClientRequest(client.Client.ClientOptions().ServerAddr, 1)
	if err != nil {
		metrics.Clients().IncreaseArgoCDClientError(client.Client.ClientOptions().ServerAddr, 1)
		return nil, err
	}
	defer conn.Close()

	metrics.Clients().IncreaseArgoCDClientRequest(client.Client.ClientOptions().ServerAddr, 1)
	tmpSelector := "tmpSelector"
	apps, err := appClient.List(ctx, &application.ApplicationQuery{Selector: &tmpSelector})
	if err != nil {
		metrics.Clients().IncreaseArgoCDClientError(client.Client.ClientOptions().ServerAddr, 1)
		return nil, err
	}

	return apps.Items, nil
}

// UpdateSpec updates the spec for given application
func (client *argoCD) UpdateSpec(ctx context.Context, in *application.ApplicationUpdateSpecRequest) (*argocdapi.ApplicationSpec, error) {
	conn, appClient, err := client.Client.NewApplicationClient()
	metrics.Clients().IncreaseArgoCDClientRequest(client.Client.ClientOptions().ServerAddr, 1)
	if err != nil {
		metrics.Clients().IncreaseArgoCDClientError(client.Client.ClientOptions().ServerAddr, 1)
		return nil, err
	}
	defer conn.Close()

	metrics.Clients().IncreaseArgoCDClientRequest(client.Client.ClientOptions().ServerAddr, 1)
	spec, err := appClient.UpdateSpec(ctx, in)
	if err != nil {
		metrics.Clients().IncreaseArgoCDClientError(client.Client.ClientOptions().ServerAddr, 1)
		return nil, err
	}

	return spec, nil
}

// getHelmParamNamesFromAnnotation inspects the given annotations for whether
// the annotations for specifying Helm parameter names are being set and
// returns their values.
func getHelmParamNamesFromAnnotation(annotations map[string]string, img *image.ContainerImage) (string, string) {
	// Return default values without symbolic name given
	if img.ImageAlias == "" {
		return "image.name", "image.tag"
	}

	var annotationName, helmParamName, helmParamVersion string

	// Image spec is a full-qualified specifier, if we have it, we return early
	if param := img.GetParameterHelmImageSpec(annotations, common.ImageUpdaterAnnotationPrefix); param != "" {
		log.Tracef("found annotation %s", annotationName)
		return strings.TrimSpace(param), ""
	}

	if param := img.GetParameterHelmImageName(annotations, common.ImageUpdaterAnnotationPrefix); param != "" {
		log.Tracef("found annotation %s", annotationName)
		helmParamName = param
	}

	if param := img.GetParameterHelmImageTag(annotations, common.ImageUpdaterAnnotationPrefix); param != "" {
		log.Tracef("found annotation %s", annotationName)
		helmParamVersion = param
	}

	return helmParamName, helmParamVersion
}

// Get a named helm parameter from a list of parameters
func getHelmParam(params []argocdapi.HelmParameter, name string) *argocdapi.HelmParameter {
	for _, param := range params {
		if param.Name == name {
			return &param
		}
	}
	return nil
}

// mergeHelmParams merges a list of Helm parameters specified by merge into the
// Helm parameters given as src.
func mergeHelmParams(src []argocdapi.HelmParameter, merge []argocdapi.HelmParameter) []argocdapi.HelmParameter {
	retParams := make([]argocdapi.HelmParameter, 0)
	merged := make(map[string]interface{})

	// first look for params that need replacement
	for _, srcParam := range src {
		found := false
		for _, mergeParam := range merge {
			if srcParam.Name == mergeParam.Name {
				retParams = append(retParams, mergeParam)
				merged[mergeParam.Name] = true
				found = true
				break
			}
		}
		if !found {
			retParams = append(retParams, srcParam)
		}
	}

	// then check which we still need in dest list and merge those, too
	for _, mergeParam := range merge {
		if _, ok := merged[mergeParam.Name]; !ok {
			retParams = append(retParams, mergeParam)
		}
	}

	return retParams
}

// GetHelmImage gets the image set in Application source matching new image
// or an empty string if match is not found
func GetHelmImage(app *argocdapi.Application, newImage *image.ContainerImage) (string, error) {

	if appType := getApplicationType(app); appType != ApplicationTypeHelm {
		return "", fmt.Errorf("cannot set Helm params on non-Helm application")
	}

	var hpImageName, hpImageTag, hpImageSpec string

	hpImageSpec = newImage.GetParameterHelmImageSpec(app.Annotations, common.ImageUpdaterAnnotationPrefix)
	hpImageName = newImage.GetParameterHelmImageName(app.Annotations, common.ImageUpdaterAnnotationPrefix)
	hpImageTag = newImage.GetParameterHelmImageTag(app.Annotations, common.ImageUpdaterAnnotationPrefix)

	if hpImageSpec == "" {
		if hpImageName == "" {
			hpImageName = registryCommon.DefaultHelmImageName
		}
		if hpImageTag == "" {
			hpImageTag = registryCommon.DefaultHelmImageTag
		}
	}

	appSource := getApplicationSource(app)

	if appSource.Helm == nil {
		return "", nil
	}

	if appSource.Helm.Parameters == nil {
		return "", nil
	}

	if hpImageSpec != "" {
		if p := getHelmParam(appSource.Helm.Parameters, hpImageSpec); p != nil {
			return p.Value, nil
		}
	} else {
		imageName := getHelmParam(appSource.Helm.Parameters, hpImageName)
		imageTag := getHelmParam(appSource.Helm.Parameters, hpImageTag)
		if imageName == nil || imageTag == nil {
			return "", nil
		}
		return imageName.Value + ":" + imageTag.Value, nil
	}

	return "", nil
}

// SetHelmImage sets image parameters for a Helm application
func SetHelmImage(app *argocdapi.Application, newImage *image.ContainerImage) error {
	if appType := getApplicationType(app); appType != ApplicationTypeHelm {
		return fmt.Errorf("cannot set Helm params on non-Helm application")
	}

	appName := app.GetName()
	appNamespace := app.GetNamespace()

	var hpImageName, hpImageTag, hpImageSpec string

	hpImageSpec = newImage.GetParameterHelmImageSpec(app.Annotations, common.ImageUpdaterAnnotationPrefix)
	hpImageName = newImage.GetParameterHelmImageName(app.Annotations, common.ImageUpdaterAnnotationPrefix)
	hpImageTag = newImage.GetParameterHelmImageTag(app.Annotations, common.ImageUpdaterAnnotationPrefix)

	if hpImageSpec == "" {
		if hpImageName == "" {
			hpImageName = registryCommon.DefaultHelmImageName
		}
		if hpImageTag == "" {
			hpImageTag = registryCommon.DefaultHelmImageTag
		}
	}

	log.WithContext().
		AddField("application", appName).
		AddField("image", newImage.GetFullNameWithoutTag()).
		AddField("namespace", appNamespace).
		Debugf("target parameters: image-spec=%s image-name=%s, image-tag=%s", hpImageSpec, hpImageName, hpImageTag)

	mergeParams := make([]argocdapi.HelmParameter, 0)

	// The logic behind this is that image-spec is an override - if this is set,
	// we simply ignore any image-name and image-tag parameters that might be
	// there.
	if hpImageSpec != "" {
		p := argocdapi.HelmParameter{Name: hpImageSpec, Value: newImage.GetFullNameWithTag(), ForceString: true}
		mergeParams = append(mergeParams, p)
	} else {
		if hpImageName != "" {
			p := argocdapi.HelmParameter{Name: hpImageName, Value: newImage.GetFullNameWithoutTag(), ForceString: true}
			mergeParams = append(mergeParams, p)
		}
		if hpImageTag != "" {
			p := argocdapi.HelmParameter{Name: hpImageTag, Value: newImage.GetTagWithDigest(), ForceString: true}
			mergeParams = append(mergeParams, p)
		}
	}

	appSource := getApplicationSource(app)

	if appSource.Helm == nil {
		appSource.Helm = &argocdapi.ApplicationSourceHelm{}
	}

	if appSource.Helm.Parameters == nil {
		appSource.Helm.Parameters = make([]argocdapi.HelmParameter, 0)
	}

	appSource.Helm.Parameters = mergeHelmParams(appSource.Helm.Parameters, mergeParams)

	return nil
}

// GetKustomizeImage gets the image set in Application source matching new image
// or an empty string if match is not found
func GetKustomizeImage(app *argocdapi.Application, newImage *image.ContainerImage) (string, error) {
	if appType := getApplicationType(app); appType != ApplicationTypeKustomize {
		return "", fmt.Errorf("cannot set Kustomize image on non-Kustomize application")
	}

	ksImageName := newImage.GetParameterKustomizeImageName(app.Annotations, common.ImageUpdaterAnnotationPrefix)

	appSource := getApplicationSource(app)

	if appSource.Kustomize == nil {
		return "", nil
	}

	ksImages := appSource.Kustomize.Images

	if ksImages == nil {
		return "", nil
	}

	for _, a := range ksImages {
		if a.Match(argocdapi.KustomizeImage(ksImageName)) {
			return string(a), nil
		}
	}

	return "", nil
}

// SetKustomizeImage sets a Kustomize image for given application
func SetKustomizeImage(app *argocdapi.Application, newImage *image.ContainerImage) error {
	if appType := getApplicationType(app); appType != ApplicationTypeKustomize {
		return fmt.Errorf("cannot set Kustomize image on non-Kustomize application")
	}

	var ksImageParam string
	ksImageName := newImage.GetParameterKustomizeImageName(app.Annotations, common.ImageUpdaterAnnotationPrefix)
	if ksImageName != "" {
		ksImageParam = fmt.Sprintf("%s=%s", ksImageName, newImage.GetFullNameWithTag())
	} else {
		ksImageParam = newImage.GetFullNameWithTag()
	}

	log.WithContext().AddField("application", app.GetName()).Tracef("Setting Kustomize parameter %s", ksImageParam)

	appSource := getApplicationSource(app)

	if appSource.Kustomize == nil {
		appSource.Kustomize = &argocdapi.ApplicationSourceKustomize{}
	}

	for i, kImg := range appSource.Kustomize.Images {
		curr := image.NewFromIdentifier(string(kImg))
		override := image.NewFromIdentifier(ksImageParam)

		if curr.ImageName == override.ImageName {
			curr.ImageAlias = override.ImageAlias
			appSource.Kustomize.Images[i] = argocdapi.KustomizeImage(override.String())
		}

	}

	appSource.Kustomize.MergeImage(argocdapi.KustomizeImage(ksImageParam))

	return nil
}

// GetImagesFromApplication returns the list of known images for the given application
func GetImagesFromApplication(app *argocdapi.Application) image.ContainerImageList {
	images := make(image.ContainerImageList, 0)

	// Get images deployed with the current ArgoCD app.
	for _, imageStr := range app.Status.Summary.Images {
		image := image.NewFromIdentifier(imageStr)
		images = append(images, image)
	}

	// The Application may wish to update images that don't create a container we can detect.
	// Check the image list for images with a force-update annotation, and add them if they are not already present.
	annotations := app.Annotations
	for _, img := range *parseImageList(annotations) {
		if img.HasForceUpdateOptionAnnotation(annotations, common.ImageUpdaterAnnotationPrefix) {
			img.ImageTag = nil // the tag from the image list will be a version constraint, which isn't a valid tag
			images = append(images, img)
		}
	}

	return images
}

// GetImagesFromApplicationImagesAnnotation returns the list of known images for the given application from the images annotation
func GetImagesAndAliasesFromApplication(app *argocdapi.Application) image.ContainerImageList {
	images := GetImagesFromApplication(app)

	// We update the ImageAlias field of the Images found in the app.Status.Summary.Images list.
	for _, img := range *parseImageList(app.Annotations) {
		if image := images.ContainsImage(img, false); image != nil {
			if image.ImageAlias != "" {
				// this image has already been matched to an alias, so create a copy
				// and assign this alias to the image copy to avoid overwriting the existing alias association
				imageCopy := *image
				if img.ImageAlias == "" {
					imageCopy.ImageAlias = img.ImageName
				} else {
					imageCopy.ImageAlias = img.ImageAlias
				}
				images = append(images, &imageCopy)
			} else {
				if img.ImageAlias == "" {
					image.ImageAlias = img.ImageName
				} else {
					image.ImageAlias = img.ImageAlias
				}
			}
		}
	}

	return images
}

// GetApplicationTypeByName first retrieves application with given appName and
// returns its application type
func GetApplicationTypeByName(client ArgoCD, appName string) (ApplicationType, error) {
	app, err := client.GetApplication(context.TODO(), appName, "")
	if err != nil {
		return ApplicationTypeUnsupported, err
	}
	return getApplicationType(app), nil
}

// GetApplicationType returns the type of the ArgoCD application
func GetApplicationType(app *argocdapi.Application) ApplicationType {
	return getApplicationType(app)
}

// GetApplicationSourceType returns the source type of the ArgoCD application
func GetApplicationSourceType(app *argocdapi.Application) argocdapi.ApplicationSourceType {
	return getApplicationSourceType(app)
}

// GetApplicationSource returns the main source of a Helm or Kustomize type of the ArgoCD application
func GetApplicationSource(app *argocdapi.Application) *argocdapi.ApplicationSource {
	return getApplicationSource(app)
}

// IsValidApplicationType returns true if we can update the application
func IsValidApplicationType(app *argocdapi.Application) bool {
	return getApplicationType(app) != ApplicationTypeUnsupported
}

// getApplicationType returns the type of the application
func getApplicationType(app *argocdapi.Application) ApplicationType {
	sourceType := getApplicationSourceType(app)

	if sourceType == argocdapi.ApplicationSourceTypeKustomize {
		return ApplicationTypeKustomize
	} else if sourceType == argocdapi.ApplicationSourceTypeHelm {
		return ApplicationTypeHelm
	} else {
		return ApplicationTypeUnsupported
	}
}

// getApplicationSourceType returns the source type of the application
func getApplicationSourceType(app *argocdapi.Application) argocdapi.ApplicationSourceType {

	if st, set := app.Annotations[common.WriteBackTargetAnnotation]; set &&
		strings.HasPrefix(st, common.KustomizationPrefix) {
		return argocdapi.ApplicationSourceTypeKustomize
	}

	if app.Spec.HasMultipleSources() {
		for _, st := range app.Status.SourceTypes {
			if st == argocdapi.ApplicationSourceTypeHelm {
				return argocdapi.ApplicationSourceTypeHelm
			} else if st == argocdapi.ApplicationSourceTypeKustomize {
				return argocdapi.ApplicationSourceTypeKustomize
			} else if st == argocdapi.ApplicationSourceTypePlugin {
				return argocdapi.ApplicationSourceTypePlugin
			}
		}
		return argocdapi.ApplicationSourceTypeDirectory
	}

	return app.Status.SourceType
}

// getApplicationSource returns the main source of a Helm or Kustomize type of the application
func getApplicationSource(app *argocdapi.Application) *argocdapi.ApplicationSource {

	if app.Spec.HasMultipleSources() {
		for _, s := range app.Spec.Sources {
			if s.Helm != nil || s.Kustomize != nil {
				return &s
			}
		}

		log.WithContext().AddField("application", app.GetName()).Tracef("Could not get Source of type Helm or Kustomize from multisource configuration. Returning first source from the list")
		return &app.Spec.Sources[0]
	}

	return app.Spec.Source
}

// String returns a string representation of the application type
func (a ApplicationType) String() string {
	switch a {
	case ApplicationTypeKustomize:
		return "Kustomize"
	case ApplicationTypeHelm:
		return "Helm"
	case ApplicationTypeUnsupported:
		return "Unsupported"
	default:
		return "Unknown"
	}
}
