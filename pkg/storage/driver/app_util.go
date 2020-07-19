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

package driver

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"kubepack.dev/kubepack/apis"
	"kubepack.dev/kubepack/apis/kubepack/v1alpha1"
	"kubepack.dev/kubepack/pkg/lib"

	"github.com/gabriel-vasile/mimetype"
	"helm.sh/helm/v3/pkg/chart"
	rspb "helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/application/api/app/v1beta1"
)

// newApplicationSecretsObject constructs a kubernetes Application object
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
func newApplicationObject(rls *rspb.Release, lbs labels) (*v1beta1.Application, error) {
	const owner = "helm"

	if lbs == nil {
		lbs.init()
	}

	// apply labels
	lbs.set("name", rls.Name)
	lbs.set("owner", owner)
	lbs.set("status", rls.Info.Status.String())
	lbs.set("version", strconv.Itoa(rls.Version))

	p := v1alpha1.ApplicationPackage{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       "ApplicationPackage",
		},
		// Bundle: x.Chart.Bundle,
		Chart: v1alpha1.ChartRepoRef{
			Name: rls.Chart.Metadata.Name,
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
			Name:      rls.Name,
			Namespace: rls.Namespace,
			Labels:    lbs.toMap(),
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
			AddOwnerRef:   true, // TODO
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
		return nil, err
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

	return obj, nil
}

func mergeSecret(app *v1beta1.Application, s *corev1.Secret) {
	var found bool
	for _, info := range app.Spec.Info {
		if info.Name == s.Name {
			found = true
			break
		}
	}
	if !found {
		app.Spec.Info = append(app.Spec.Info, v1beta1.InfoItem{
			Name: s.Name,
			Type: v1beta1.ReferenceInfoItemType,
			ValueFrom: &v1beta1.InfoItemSource{
				Type: v1beta1.SecretKeyRefInfoItemSourceType,
				SecretKeyRef: &v1beta1.SecretKeySelector{
					ObjectReference: corev1.ObjectReference{
						Namespace: s.Namespace,
						Name:      s.Name,
					},
					Key: "values",
				},
			},
		})
	}
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
				return components, commonLabels, err
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
	return components, commonLabels, nil
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

// decodeRelease decodes the bytes of data into a release
// type. Data must contain a base64 encoded gzipped string of a
// valid release, otherwise an error is returned.
func decodeReleaseFromApp(app *v1beta1.Application) (*rspb.Release, error) {
	var rls rspb.Release

	rls.Name = app.Labels["name"]
	rls.Namespace = app.Namespace
	rls.Version, _ = strconv.Atoi(app.Labels["version"])

	var ap v1alpha1.ApplicationPackage
	if data, ok := app.Labels[apis.LabelPackage]; ok {
		err := json.Unmarshal([]byte(data), &ap)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("application %s/%s is missing %s label", app.Namespace, app.Name, apis.LabelPackage)
	}

	chrt, err := lib.DefaultRegistry.GetChart(ap.Chart.URL, ap.Chart.Name, ap.Chart.Version)
	if err != nil {
		return nil, err
	}
	rls.Chart = chrt.Chart

	return &rls, nil
}
