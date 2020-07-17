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
	"encoding/json"
	"fmt"
	"kubepack.dev/kubepack/apis"
	"kubepack.dev/kubepack/apis/kubepack/v1alpha1"
	"kubepack.dev/kubepack/pkg/lib"
	"sigs.k8s.io/application/api/app/v1beta1"
	"strconv"

	rspb "helm.sh/helm/v3/pkg/release"
)

// decodeRelease decodes the bytes of data into a release
// type. Data must contain a base64 encoded gzipped string of a
// valid release, otherwise an error is returned.
func decodeRelease(app *v1beta1.Application) (*rspb.Release, error) {
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
