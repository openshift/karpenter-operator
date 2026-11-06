package runner

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// TargetSpec is a CRD a generic suite is run against.
type TargetSpec struct {
	// CRDName is the metadata.name of the CRD under test.
	CRDName string `json:"crdName"`

	// Version is the version of the CRD under test.
	// When omitted, the CRD must have a single version, which is used.
	Version string `json:"version,omitempty"`
}

// adaptTests returns the cases with every document rewritten for the target CRD.
func adaptTests(tests TestSpec, crd *apiextensionsv1.CustomResourceDefinition, version string) (TestSpec, error) {
	out := TestSpec{}

	for _, c := range tests.OnCreate {
		var err error
		if c.Initial, err = adaptDocument(c.Initial, crd, version); err != nil {
			return TestSpec{}, fmt.Errorf("onCreate case %q: %w", c.Name, err)
		}
		if c.Expected, err = adaptDocument(c.Expected, crd, version); err != nil {
			return TestSpec{}, fmt.Errorf("onCreate case %q: %w", c.Name, err)
		}
		out.OnCreate = append(out.OnCreate, c)
	}

	for _, c := range tests.OnUpdate {
		var err error
		for _, doc := range []*string{&c.Initial, &c.Updated, &c.Expected} {
			if *doc, err = adaptDocument(*doc, crd, version); err != nil {
				return TestSpec{}, fmt.Errorf("onUpdate case %q: %w", c.Name, err)
			}
		}
		out.OnUpdate = append(out.OnUpdate, c)
	}

	return out, nil
}

// adaptDocument sets apiVersion and kind of the document.
// An empty document stays empty, and a document that sets apiVersion or kind is an error.
func adaptDocument(raw string, crd *apiextensionsv1.CustomResourceDefinition, version string) (string, error) {
	if raw == "" {
		return raw, nil
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return "", err
	}

	for _, key := range []string{"apiVersion", "kind"} {
		if _, found := doc[key]; found {
			return "", fmt.Errorf("a generic suite must not set %s, it is set per target", key)
		}
	}

	doc["apiVersion"] = fmt.Sprintf("%s/%s", crd.Spec.Group, version)
	doc["kind"] = crd.Spec.Names.Kind

	out, err := yaml.Marshal(doc)

	return string(out), err
}
