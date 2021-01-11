package pkg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"kubepack.dev/chart-doc-gen/api"

	"github.com/Masterminds/sprig"
	"github.com/gobuffalo/flect"
	"github.com/spf13/cobra"
	y3 "gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chart"
	crdv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	ylib "k8s.io/apimachinery/pkg/util/yaml"
	"kmodules.xyz/resource-metadata/hub"
	"sigs.k8s.io/yaml"
)

var (
	sampleDir   = ""
	chartDir    = ""
	chartName   = ""
	chartSchema = crdv1.JSONSchemaProps{
		Type:       "object",
		Properties: map[string]crdv1.JSONSchemaProps{},
	}
	modelValues  = map[string]ObjectContainer{}
	registry     = hub.NewRegistryOfKnownResources()
	resourceKeys = sets.NewString()
)

type ObjectModel struct {
	Key    string                     `json:"key"`
	Object *unstructured.Unstructured `json:"object"`
}

type ObjectContainer struct {
	metav1.TypeMeta `json:",inline"`
}

func NewCmdFuse() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "fuse-chart",
		Short:             `Fuse YAMLs`,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			tplDir := filepath.Join(chartDir, chartName, "templates")
			err := os.MkdirAll(tplDir, 0755)
			if err != nil {
				return err
			}
			err = GenerateChartMetadata()
			if err != nil {
				return err
			}

			err = ProcessResource(filepath.Join(sampleDir, chartName), func(obj *unstructured.Unstructured) error {
				rsKey, err := resourceKey(obj.GetAPIVersion(), obj.GetKind(), chartName, obj.GetName())
				if err != nil {
					return err
				}
				resourceKeys.Insert(rsKey)
				_, _, rsFilename := resourceFilename(obj.GetAPIVersion(), obj.GetKind(), chartName, obj.GetName())

				// values
				modelValues[rsKey] = ObjectContainer{
					TypeMeta: metav1.TypeMeta{
						APIVersion: obj.GetAPIVersion(),
						Kind:       obj.GetKind(),
					},
				}

				// schema
				gvr, err := registry.GVR(obj.GetObjectKind().GroupVersionKind())
				if err != nil {
					return err
				}
				descriptor, err := registry.LoadByGVR(gvr)
				if err != nil {
					return err
				}
				if descriptor.Spec.Validation != nil && descriptor.Spec.Validation.OpenAPIV3Schema != nil {
					delete(descriptor.Spec.Validation.OpenAPIV3Schema.Properties, "status")
					chartSchema.Properties[rsKey] = *descriptor.Spec.Validation.OpenAPIV3Schema
				}

				// templates
				filename := filepath.Join(tplDir, rsFilename+".yaml")
				f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
				if err != nil {
					return err
				}
				defer f.Close()

				objModel := ObjectModel{
					Key:    rsKey,
					Object: obj,
				}
				modelJSON, err := json.Marshal(objModel)
				if err != nil {
					return err
				}

				var data map[string]interface{}
				err = json.Unmarshal(modelJSON, &data)
				if err != nil {
					panic(err)
				}

				resourceTemplate := `{{"{{- with .Values."}}{{ .key }} {{"}}"}}
{{"{{- . | toYaml }}"}}
{{"{{- end }}"}}
`
				funcMap := sprig.TxtFuncMap()
				funcMap["toYaml"] = toYAML
				funcMap["toJson"] = toJSON
				tpl := template.Must(template.New("resourceTemplate").Funcs(funcMap).Parse(resourceTemplate))
				err = tpl.Execute(f, &data)
				if err != nil {
					return err
				}

				return nil
			})
			if err != nil {
				return err
			}

			{
				data3, err := yaml.Marshal(chartSchema)
				if err != nil {
					return err
				}
				schemaFilename := filepath.Join(chartDir, chartName, "values.openapiv3_schema.yaml")
				err = ioutil.WriteFile(schemaFilename, data3, 0644)
				if err != nil {
					return err
				}
			}

			{
				data, err := yaml.Marshal(modelValues)
				if err != nil {
					panic(err)
				}

				var root y3.Node
				err = y3.Unmarshal(data, &root)
				if err != nil {
					return err
				}
				addDocComments(&root)

				//data, err = y3.Marshal(&root)
				//if err != nil {
				//	return err
				//}

				var buf bytes.Buffer
				enc := y3.NewEncoder(&buf)
				enc.SetIndent(2)
				defer enc.Close()
				err = enc.Encode(&root)
				if err != nil {
					return err
				}

				filename := filepath.Join(chartDir, chartName, "values.yaml")
				err = ioutil.WriteFile(filename, buf.Bytes(), 0644)
				if err != nil {
					return err
				}
			}

			{
				desc := flect.Titleize(strings.ReplaceAll(chartName, "-", " "))
				doc := api.DocInfo{
					Project: api.ProjectInfo{
						Name:        fmt.Sprintf("%s by AppsCode", desc),
						ShortName:   fmt.Sprintf("%s", desc),
						URL:         "https://byte.builders",
						Description: fmt.Sprintf("%s", desc),
						App:         fmt.Sprintf("a %s", desc),
					},
					Repository: api.RepositoryInfo{
						URL:  "https://bundles.bytebuilders.dev/ui/",
						Name: "bytebuilders-ui",
					},
					Chart: api.ChartInfo{
						Name:          chartName,
						Version:       "v0.1.0",
						Values:        "-- generate from values file --",
						ValuesExample: "-- generate from values file --",
					},
					Prerequisites: []string{
						"Kubernetes 1.14+",
					},
					Release: api.ReleaseInfo{
						Name:      chartName,
						Namespace: metav1.NamespaceDefault,
					},
				}

				data, err := yaml.Marshal(&doc)
				if err != nil {
					return err
				}

				filename := filepath.Join(chartDir, chartName, "doc.yaml")
				err = ioutil.WriteFile(filename, data, 0644)
				if err != nil {
					return err
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&sampleDir, "sample-dir", sampleDir, "Sample dir")
	cmd.Flags().StringVar(&chartDir, "chart-dir", chartDir, "Charts dir")
	cmd.Flags().StringVar(&chartName, "chart-name", chartName, "Charts name")

	return cmd
}

func GenerateChartMetadata() error {
	chartMeta := chart.Metadata{
		Name:        chartName,
		Home:        "https://byte.builders",
		Version:     "v0.1.0",
		Description: "Ui Wizard Chart",
		Keywords:    []string{"appscode"},
		Maintainers: []*chart.Maintainer{
			{
				Name:  "AppsCode Engineering",
				Email: "support@appscode.com",
				URL:   "https://appscode.com",
			},
		},
	}
	data4, err := yaml.Marshal(chartMeta)
	if err != nil {
		return err
	}
	filename := filepath.Join(chartDir, chartName, "Chart.yaml")
	return ioutil.WriteFile(filename, data4, 0644)
}

type ResourceFn func(obj *unstructured.Unstructured) error

func ProcessResource(dir string, fn ResourceFn) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		fmt.Println(">>> ", path)
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		reader := ylib.NewYAMLOrJSONDecoder(bytes.NewReader(data), 2048)
		for {
			var obj unstructured.Unstructured
			err := reader.Decode(&obj)
			if err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			if obj.IsList() {
				if err := obj.EachListItem(func(item runtime.Object) error {
					return fn(item.(*unstructured.Unstructured))
				}); err != nil {
					return err
				}
			} else {
				if err := fn(&obj); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func resourceKey(apiVersion, kind, chartName, name string) (string, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "", err
	}

	groupPrefix := gv.Group
	groupPrefix = strings.TrimSuffix(groupPrefix, ".k8s.io")
	groupPrefix = strings.TrimSuffix(groupPrefix, ".kubernetes.io")
	//groupPrefix = strings.TrimSuffix(groupPrefix, ".x-k8s.io")
	groupPrefix = strings.Replace(groupPrefix, ".", "_", -1)
	groupPrefix = flect.Pascalize(groupPrefix)

	var nameSuffix string
	nameSuffix = strings.TrimPrefix(chartName, name)
	nameSuffix = strings.Replace(nameSuffix, ".", "-", -1)
	nameSuffix = strings.Trim(nameSuffix, "-")
	nameSuffix = flect.Pascalize(nameSuffix)

	return flect.Camelize(groupPrefix + kind + nameSuffix), nil
}

func resourceFilename(apiVersion, kind, chartName, name string) (string, string, string) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		panic(err)
	}

	groupPrefix := gv.Group
	groupPrefix = strings.TrimSuffix(groupPrefix, ".k8s.io")
	groupPrefix = strings.TrimSuffix(groupPrefix, ".kubernetes.io")
	//groupPrefix = strings.TrimSuffix(groupPrefix, ".x-k8s.io")
	groupPrefix = strings.Replace(groupPrefix, ".", "_", -1)
	groupPrefix = flect.Pascalize(groupPrefix)

	var nameSuffix string
	nameSuffix = strings.TrimPrefix(chartName, name)
	nameSuffix = strings.Replace(nameSuffix, ".", "-", -1)
	nameSuffix = strings.Trim(nameSuffix, "-")
	nameSuffix = flect.Pascalize(nameSuffix)

	return flect.Underscore(kind), flect.Underscore(kind + nameSuffix), flect.Underscore(groupPrefix + kind + nameSuffix)
}

// toYAML takes an interface, marshals it to yaml, and returns a string. It will
// always return a string, even on marshal error (empty string).
//
// This is designed to be called from a template.
func toYAML(v interface{}) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return strings.TrimSuffix(string(data), "\n")
}

// toJSON takes an interface, marshals it to json, and returns a string. It will
// always return a string, even on marshal error (empty string).
//
// This is designed to be called from a template.
func toJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return string(data)
}

func addDocComments(node *y3.Node) {
	if node.Tag == "!!str" && resourceKeys.Has(node.Value) {
		node.LineComment = "# +doc-gen:break"
	}
	for i := range node.Content {
		addDocComments(node.Content[i])
	}
}
