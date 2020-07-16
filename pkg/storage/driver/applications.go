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
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	rspb "helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"sigs.k8s.io/application/api/app/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kblabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	cs "sigs.k8s.io/application/client/clientset/versioned/typed/app/v1beta1"
)

var _ driver.Driver = (*Applications)(nil)

// ApplicationsDriverName is the string name of the driver.
const ApplicationsDriverName = "Application"

// Applications is a wrapper around an implementation of a kubernetes
// ApplicationsInterface.
type Applications struct {
	impl cs.ApplicationInterface
	Log  func(string, ...interface{})
}

// NewApplications initializes a new Applications wrapping an implementation of
// the kubernetes ApplicationsInterface.
func NewApplications(impl cs.ApplicationInterface) *Applications {
	return &Applications{
		impl: impl,
		Log:  func(_ string, _ ...interface{}) {},
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
	obj, err := apps.impl.Get(context.Background(), key, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, driver.ErrReleaseNotFound
		}

		apps.Log("get: failed to get %q: %s", key, err)
		return nil, err
	}
	// found the configmap, decode the base64 data string
	r, err := decodeRelease(obj.Data["release"])
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

	list, err := apps.impl.List(context.Background(), opts)
	if err != nil {
		apps.Log("list: failed to list: %s", err)
		return nil, err
	}

	var results []*rspb.Release

	// iterate over the configmaps object list
	// and decode each release
	for _, item := range list.Items {
		rls, err := decodeRelease(item.Data["release"])
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

	list, err := apps.impl.List(context.Background(), opts)
	if err != nil {
		apps.Log("query: failed to query with labels: %s", err)
		return nil, err
	}

	if len(list.Items) == 0 {
		return nil, driver.ErrReleaseNotFound
	}

	var results []*rspb.Release
	for _, item := range list.Items {
		rls, err := decodeRelease(item.Data["release"])
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
	obj, err := newApplicationsObject(key, rls, lbs)
	if err != nil {
		apps.Log("create: failed to encode release %q: %s", rls.Name, err)
		return err
	}
	// push the configmap object out into the kubiverse
	if _, err := apps.impl.Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
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
	obj, err := newApplicationsObject(key, rls, lbs)
	if err != nil {
		apps.Log("update: failed to encode release %q: %s", rls.Name, err)
		return err
	}
	// push the configmap object out into the kubiverse
	_, err = apps.impl.Update(context.Background(), obj, metav1.UpdateOptions{})
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
	if err = apps.impl.Delete(context.Background(), key, metav1.DeleteOptions{}); err != nil {
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
func newApplicationsObject(key string, rls *rspb.Release, lbs labels) (*v1beta1.Application, error) {
	const owner = "helm"

	// encode the release
	s, err := encodeRelease(rls)
	if err != nil {
		return nil, err
	}

	if lbs == nil {
		lbs.init()
	}

	// apply labels
	lbs.set("name", rls.Name)
	lbs.set("owner", owner)
	lbs.set("status", rls.Info.Status.String())
	lbs.set("version", strconv.Itoa(rls.Version))

	// create and return configmap object
	return &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:   key,
			Labels: lbs.toMap(),
		},
		Data: map[string]string{"release": s},
	}, nil
}
