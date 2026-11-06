package runner

import (
	"encoding/json"
	"fmt"
	"os"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"sigs.k8s.io/yaml"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

// getPatchedCRD loads the CRD file and applies the RFC 6902 patches to it.
func getPatchedCRD(crdFileName string, patches []Patch) (*apiextensionsv1.CustomResourceDefinition, error) {
	patchJSON, err := json.Marshal(patches)
	if err != nil {
		return nil, fmt.Errorf("could not marshal patch: %w", err)
	}

	patch, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return nil, fmt.Errorf("could not decode patch: %w", err)
	}

	baseDoc, err := os.ReadFile(crdFileName)
	if err != nil {
		return nil, fmt.Errorf("could not read file %q: %w", crdFileName, err)
	}

	baseJSON, err := yaml.YAMLToJSON(baseDoc)
	if err != nil {
		return nil, fmt.Errorf("could not convert CRD to JSON: %w", err)
	}

	patchedJSON, err := patch.Apply(baseJSON)
	if err != nil {
		return nil, fmt.Errorf("could not apply patch: %w", err)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := json.Unmarshal(patchedJSON, crd); err != nil {
		return nil, fmt.Errorf("could not unmarshal CRD: %w", err)
	}

	return crd, nil
}
