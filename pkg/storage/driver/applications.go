/*
Copyright The Helm Authors.

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

package driver // import "helm.sh/helm/v3/pkg/storage/driver"

import (
	"context"
	"encoding/json"
	"helm.sh/helm/v3/pkg/chart"
	"io"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"kubepack.dev/kubepack/apis"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"kubepack.dev/kubepack/apis/kubepack/v1alpha1"

	"github.com/gabriel-vasile/mimetype"
	"github.com/pkg/errors"
	rspb "helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kblabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/application/api/app/v1beta1"
	cs "sigs.k8s.io/application/client/clientset/versioned/typed/app/v1beta1"
)

var _ driver.Driver = (*Applications)(nil)

// ApplicationsDriverName is the string name of the driver.
const ApplicationsDriverName = "Application"

// Applications is a wrapper around an implementation of a kubernetes
// ApplicationsInterface.
type Applications struct {
	appClient cs.ApplicationInterface
	secretClient v1.SecretInterface
	Log func(string, ...interface{})
}

// NewApplications initializes a new Applications wrapping an implementation of
// the kubernetes ApplicationsInterface.
func NewApplications(appClient cs.ApplicationInterface, secretClient v1.SecretInterface) *Applications {
	return &Applications{
		appClient: appClient,
		secretClient : secretClient,
		Log:       func(_ string, _ ...interface{}) {},
	}
}

// Name returns the name of the driver.
func (apps *Applications) Name() string {
	return ApplicationsDriverName
}

// Get fetches the release named by key. The corresponding release is returned
// or error if not found.
func (apps *Applications) Get(key string) (*rspb.Release, error) {
	// fetch the configmap holding the release named by key
	obj, err := apps.appClient.Get(context.Background(), key, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, driver.ErrReleaseNotFound
		}

		apps.Log("get: failed to get %q: %s", key, err)
		return nil, err
	}
	// found the configmap, decode the base64 data string
	r, err := decodeRelease(obj)
	if err != nil {
		apps.Log("get: failed to decode data %q: %s", key, err)
		return nil, err
	}
	// return the release object
	return r, nil
}

// List fetches all releases and returns the list releases such
// that filter(release) == true. An error is returned if the
// configmap fails to retrieve the releases.
func (apps *Applications) List(filter func(*rspb.Release) bool) ([]*rspb.Release, error) {
	lsel := kblabels.Set{"owner": "helm"}.AsSelector()
	opts := metav1.ListOptions{LabelSelector: lsel.String()}

	list, err := apps.appClient.List(context.Background(), opts)
	if err != nil {
		apps.Log("list: failed to list: %s", err)
		return nil, err
	}

	var results []*rspb.Release

	// iterate over the configmaps object list
	// and decode each release
	for _, item := range list.Items {
		rls, err := decodeRelease(&item)
		if err != nil {
			apps.Log("list: failed to decode release: %v: %s", item, err)
			continue
		}
		if filter(rls) {
			results = append(results, rls)
		}
	}
	return results, nil
}

// Query fetches all releases that match the provided map of labels.
// An error is returned if the configmap fails to retrieve the releases.
func (apps *Applications) Query(labels map[string]string) ([]*rspb.Release, error) {
	ls := kblabels.Set{}
	for k, v := range labels {
		if errs := validation.IsValidLabelValue(v); len(errs) != 0 {
			return nil, errors.Errorf("invalid label value: %q: %s", v, strings.Join(errs, "; "))
		}
		ls[k] = v
	}

	opts := metav1.ListOptions{LabelSelector: ls.AsSelector().String()}

	list, err := apps.appClient.List(context.Background(), opts)
	if err != nil {
		apps.Log("query: failed to query with labels: %s", err)
		return nil, err
	}

	if len(list.Items) == 0 {
		return nil, driver.ErrReleaseNotFound
	}

	var results []*rspb.Release
	for _, item := range list.Items {
		rls, err := decodeRelease(&item)
		if err != nil {
			apps.Log("query: failed to decode release: %s", err)
			continue
		}
		results = append(results, rls)
	}
	return results, nil
}

// Create creates a new Application holding the release. If the
// Application already exists, ErrReleaseExists is returned.
func (apps *Applications) Create(key string, rls *rspb.Release) error {
	// set labels for configmaps object meta data
	var lbs labels

	lbs.init()
	lbs.set("createdAt", strconv.Itoa(int(time.Now().Unix())))

	// create a new configmap to hold the release
	obj, values,  err := newApplicationsObject(key, rls, lbs)
	if err != nil {
		apps.Log("create: failed to encode release %q: %s", rls.Name, err)
		return err
	}
	// push the configmap object out into the kubiverse
	if _, err := apps.appClient.Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return driver.ErrReleaseExists
		}

		apps.Log("create: failed to create: %s", err)
		return err
	}
	if _, err := apps.secretClient.Create(context.Background(), values, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return driver.ErrReleaseExists
		}

		apps.Log("create: failed to create: %s", err)
		return err
	}
	return nil
}

// Update updates the Application holding the release. If not found
// the Application is created to hold the release.
func (apps *Applications) Update(key string, rls *rspb.Release) error {
	// set labels for configmaps object meta data
	var lbs labels

	lbs.init()
	lbs.set("modifiedAt", strconv.Itoa(int(time.Now().Unix())))

	// create a new configmap object to hold the release
	obj, values, err := newApplicationsObject(key, rls, lbs)
	if err != nil {
		apps.Log("update: failed to encode release %q: %s", rls.Name, err)
		return err
	}
	// push the configmap object out into the kubiverse
	_, err = apps.appClient.Update(context.Background(), obj, metav1.UpdateOptions{})
	if err != nil {
		apps.Log("update: failed to update: %s", err)
		return err
	}
	_, err = apps.secretClient.Update(context.Background(), values, metav1.UpdateOptions{})
	if err != nil {
		apps.Log("update: failed to update: %s", err)
		return err
	}
	return nil
}

// Delete deletes the Application holding the release named by key.
func (apps *Applications) Delete(key string) (rls *rspb.Release, err error) {
	// fetch the release to check existence
	if rls, err = apps.Get(key); err != nil {
		return nil, err
	}
	// delete the release
	if err = apps.appClient.Delete(context.Background(), key, metav1.DeleteOptions{}); err != nil {
		return rls, err
	}
	return rls, nil
}

// newApplicationsObject constructs a kubernetes Application object
// to store a release. Each configmap data entry is the base64
// encoded gzipped string of a release.
//
// The following labels are used within each configmap:
//
//    "modifiedAt"     - timestamp indicating when this configmap was last modified. (set in Update)
//    "createdAt"      - timestamp indicating when this configmap was created. (set in Create)
//    "version"        - version of the release.
//    "status"         - status of the release (see pkg/release/status.go for variants)
//    "owner"          - owner of the configmap, currently "helm".
//    "name"           - name of the release.
//
func newApplicationsObject(key string, rls *rspb.Release, lbs labels) (*v1beta1.Application, *corev1.Secret, error) {
	const owner = "helm"

	if lbs == nil {
		lbs.init()
	}

	// apply labels
	lbs.set("name", rls.Name)
	lbs.set("owner", owner)
	lbs.set("status", rls.Info.Status.String())
	lbs.set("version", strconv.Itoa(rls.Version))

	values, err := json.Marshal(rls.Config)
	if err != nil {
		return nil, nil, err
	}

	vs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key,
			Namespace: rls.Namespace,
		},
		Immutable: nil,
		Data: map[string][]byte{
			"values": values,
		},
		Type: "kubepack.com/helm-values",
	}

	p := v1alpha1.ApplicationPackage{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       "ApplicationPackage",
		},
		// Bundle: x.Chart.Bundle,
		Chart: v1alpha1.ChartRepoRef{
			Name:    rls.Chart.Metadata.Name,
			// URL:     rls.Chart.Metadata.Sources[0],
			Version: rls.Chart.Metadata.Version,
		},
		Channel: v1alpha1.RegularChannel,
	}
	data, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}

	// create and return configmap object
	obj := &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:   key,
			Namespace: rls.Namespace,
			Labels: lbs.toMap(),
			Annotations: map[string]string{
				apis.LabelPackage: string(data),
			},
		},
		Spec: v1beta1.ApplicationSpec{
			Descriptor: v1beta1.Descriptor{
				Type:        rls.Chart.Metadata.Type,
				Version:     rls.Chart.Metadata.AppVersion,
				Description: rls.Chart.Metadata.Description,
				Owners:      nil, // FIX
				Keywords:    rls.Chart.Metadata.Keywords,
				Links: []v1beta1.Link{
					{
						Description: string(v1alpha1.LinkWebsite),
						URL:         rls.Chart.Metadata.Home,
					},
				},
				Notes: rls.Info.Notes,
			},
			ComponentGroupKinds: nil,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{},
			},
			AddOwnerRef:   true, // false
			Info:          []v1beta1.InfoItem{
				{
					Name:      "values",
					Type:      v1beta1.ReferenceInfoItemType,
					Value:     "",
					ValueFrom: &v1beta1.InfoItemSource{
						Type:            v1beta1.SecretKeyRefInfoItemSourceType,
						SecretKeyRef:    &v1beta1.SecretKeySelector{
							ObjectReference: corev1.ObjectReference{
								Namespace:       vs.Namespace,
								Name:            vs.Name,
							},
							Key:             "values",
						},
					},
				},
			},
			AssemblyPhase: v1beta1.Pending,
		},
	}
	if rls.Chart.Metadata.Icon != "" {
		var imgType string
		if resp, err := http.Get(rls.Chart.Metadata.Icon); err == nil {
			if mime, err := mimetype.DetectReader(resp.Body); err == nil {
				imgType = mime.String()
			}
			_ = resp.Body.Close()
		}
		obj.Spec.Descriptor.Icons = []v1beta1.ImageSpec{
			{
				Source: rls.Chart.Metadata.Icon,
				// TotalSize: "",
				Type: imgType,
			},
		}
	}
	for _, maintainer := range rls.Chart.Metadata.Maintainers {
		obj.Spec.Descriptor.Maintainers = append(obj.Spec.Descriptor.Maintainers, v1beta1.ContactData{
			Name:  maintainer.Name,
			URL:   maintainer.URL,
			Email: maintainer.Email,
		})
	}

	components := map[metav1.GroupKind]struct{}{}
	var commonLabels map[string]string

	// Hooks ?
	components, commonLabels, err = extractComponents(rls.Manifest, components, commonLabels)
	if err != nil {
		return nil, nil, err
	}

	gks := make([]metav1.GroupKind, 0, len(components))
	for gk := range components {
		gks = append(gks, gk)
	}
	sort.Slice(gks, func(i, j int) bool {
		if gks[i].Group == gks[j].Group {
			return gks[i].Kind < gks[j].Kind
		}
		return gks[i].Group < gks[j].Group
	})
	obj.Spec.ComponentGroupKinds = gks

	if len(commonLabels) > 0 {
		obj.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: commonLabels,
		}
	}

	return obj, vs, nil
}

var empty = struct{}{}

func extractComponents(data string, components map[metav1.GroupKind]struct{}, commonLabels map[string]string) (map[metav1.GroupKind]struct{}, map[string]string, error) {
	reader := yaml.NewYAMLOrJSONDecoder(strings.NewReader(data), 2048)
	for {
		var obj unstructured.Unstructured
		err := reader.Decode(&obj)
		if err == io.EOF {
			break
		} else if err != nil {
			return components, commonLabels, err
		}
		if obj.IsList() {
			err := obj.EachListItem(func(item runtime.Object) error {
				castItem := item.(*unstructured.Unstructured)

				gv, err := schema.ParseGroupVersion(castItem.GetAPIVersion())
				if err != nil {
					return err
				}
				components[metav1.GroupKind{Group: gv.Group, Kind: castItem.GetKind()}] = empty

				if commonLabels == nil {
					commonLabels = castItem.GetLabels()
				} else {
					for k, v := range castItem.GetLabels() {
						if existing, found := commonLabels[k]; found && existing != v {
							delete(commonLabels, k)
						}
					}
				}
				return nil
			})
			if err != nil {
				return components, commonLabels, err
			}
		} else {
			gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
			if err != nil {
				return  components, commonLabels,err
			}
			components[metav1.GroupKind{Group: gv.Group, Kind: obj.GetKind()}] = empty

			if commonLabels == nil {
				commonLabels = obj.GetLabels()
			} else {
				for k, v := range obj.GetLabels() {
					if existing, found := commonLabels[k]; found && existing != v {
						delete(commonLabels, k)
					}
				}
			}
		}
	}
	return  components, commonLabels,nil
}

func copyMap(src map[string]interface{}) map[string]interface{} {
	m := make(map[string]interface{}, len(src))
	for k, v := range src {
		m[k] = v
	}
	return m
}

// coalesceValues builds up a values map for a particular chart.
//
// Values in v will override the values in the chart.
func coalesceValues(c *chart.Chart, v map[string]interface{}) {
	for key, val := range c.Values {
		if value, ok := v[key]; ok {
			if value == nil {
				// When the YAML value is null, we remove the value's key.
				// This allows Helm's various sources of values (value files or --set) to
				// remove incompatible keys from any previous chart, file, or set values.
				delete(v, key)
			} else if dest, ok := value.(map[string]interface{}); ok {
				// if v[key] is a table, merge nv's val table into v[key].
				src, ok := val.(map[string]interface{})
				if !ok {
					log.Printf("warning: skipped value for %s: Not a table.", key)
					continue
				}
				// Because v has higher precedence than nv, dest values override src
				// values.
				CoalesceTables(dest, src)
			}
		} else {
			// If the key is not in v, copy it from nv.
			v[key] = val
		}
	}
}

// CoalesceTables merges a source map into a destination map.
//
// dest is considered authoritative.
func CoalesceTables(dst, src map[string]interface{}) map[string]interface{} {
	// When --reuse-values is set but there are no modifications yet, return new values
	if src == nil {
		return dst
	}
	if dst == nil {
		return src
	}
	// Because dest has higher precedence than src, dest values override src
	// values.
	for key, val := range src {
		if dv, ok := dst[key]; ok && dv == nil {
			delete(dst, key)
		} else if !ok {
			dst[key] = val
		} else if istable(val) {
			if istable(dv) {
				CoalesceTables(dv.(map[string]interface{}), val.(map[string]interface{}))
			} else {
				log.Printf("warning: cannot overwrite table with non table for %s (%v)", key, val)
			}
		} else if istable(dv) {
			log.Printf("warning: destination for %s is a table. Ignoring non-table value %v", key, val)
		}
	}
	return dst
}

// istable is a special-purpose function to see if the present thing matches the definition of a YAML table.
func istable(v interface{}) bool {
	_, ok := v.(map[string]interface{})
	return ok
}