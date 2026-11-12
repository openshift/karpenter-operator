package runner

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// SuiteSpec defines a test suite specification.
type SuiteSpec struct {
	// APIVersion is the apiVersion of the suite file, kept so that suites written
	// in the openshift/api format are accepted by strict parsing.
	APIVersion string `json:"apiVersion,omitempty"`

	// Name is the name of the test suite.
	Name string `json:"name"`

	// CRDName is the metadata.name of the CRD under test.
	CRDName string `json:"crdName"`

	// Version is the version of the CRD under test in this file.
	// When omitted, the CRD must have a single version, which is used.
	Version string `json:"version,omitempty"`

	// Targets makes the suite generic. The suite is run once per target, and CRDName and Version
	// must be omitted. The documents of the cases omit `apiVersion` and `kind`, which are
	// set per target.
	Targets []TargetSpec `json:"targets,omitempty"`

	// Tests defines the test cases to run for this test suite.
	Tests TestSpec `json:"tests"`

	// CRDFilename is the path of the CRD file matching CRDName. It is resolved at load time.
	CRDFilename string `json:"-"`

	// CRD is the CRD matching CRDName reduced to Version. It is resolved at load time.
	CRD *apiextensionsv1.CustomResourceDefinition `json:"-"`
}

// TestSpec defines the test specs for individual tests in this suite.
type TestSpec struct {
	// OnCreate defines a list of on create style tests.
	OnCreate []OnCreateTestSpec `json:"onCreate"`

	// OnUpdate defines a list of on update style tests.
	OnUpdate []OnUpdateTestSpec `json:"onUpdate"`
}

// OnCreateTestSpec defines an individual test case for the on create style tests.
type OnCreateTestSpec struct {
	// Name is the name of this test case.
	Name string `json:"name"`

	// Initial is a literal string containing the initial YAML content from which to
	// create the resource.
	// Note `apiVersion` and `kind` fields are required though `metadata` can be omitted.
	// Typically this will vary in `spec` only test to test.
	Initial string `json:"initial"`

	// ExpectedError defines the error string that should be returned when the initial resource is invalid.
	// This will be matched as a substring of the actual error when non-empty.
	// It must not be combined with Expected.
	ExpectedError string `json:"expectedError"`

	// Expected is a literal string containing the expected YAML content that should be
	// persisted when the resource is created.
	// When empty, the case only asserts that the resource is created successfully.
	// Note `apiVersion` and `kind` fields are required though `metadata` can be omitted.
	// Typically this will vary in `spec` only test to test.
	Expected string `json:"expected"`
}

// OnUpdateTestSpec defines an individual test case for the on update style tests.
// Exactly one of Expected, ExpectedError and ExpectedStatusError must be set.
type OnUpdateTestSpec struct {
	// Name is the name of this test case.
	Name string `json:"name"`

	// InitialCRDPatches is a list of JSON patches to apply to the CRD before applying
	// the initial version of the resource.
	// Once the initial version has been applied, the CRD will be restored to its
	// original state before the updated object is applied.
	// This can be used to test ratcheting validation of CRD schema changes over time.
	InitialCRDPatches []Patch `json:"initialCRDPatches"`

	// Initial is a literal string containing the initial YAML content from which to
	// create the resource.
	// Note `apiVersion` and `kind` fields are required though `metadata` can be omitted.
	// Typically this will vary in `spec` only test to test.
	Initial string `json:"initial"`

	// Updated is a literal string containing the updated YAML content from which to
	// update the resource.
	// Note `apiVersion` and `kind` fields are required though `metadata` can be omitted.
	// Typically this will vary in `spec` only test to test.
	Updated string `json:"updated"`

	// ExpectedError defines the error string that should be returned when the updated resource is invalid.
	// This will be matched as a substring of the actual error when non-empty.
	ExpectedError string `json:"expectedError"`

	// ExpectedStatusError defines the error string that should be returned when the updated resource status is invalid.
	// This will be matched as a substring of the actual error when non-empty.
	// Updated must contain a non-null status.
	ExpectedStatusError string `json:"expectedStatusError"`

	// Expected is a literal string containing the expected YAML content that should be
	// persisted when the resource is updated.
	// Note `apiVersion` and `kind` fields are required though `metadata` can be omitted.
	// Typically this will vary in `spec` only test to test.
	Expected string `json:"expected"`
}

// Patch represents a single operation to be applied to a YAML document.
// It follows the JSON Patch format as defined in RFC 6902.
type Patch struct {
	// Op is the operation to be performed. Common operations include "add", "remove", "replace", "move", "copy", and "test".
	Op string `json:"op"`

	// Path is a JSON Pointer that indicates the location in the YAML document where the operation is to be performed.
	Path string `json:"path"`

	// Value is the value to be used within the operation. This field is required for operations like "add" and "replace".
	Value *any `json:"value"`
}
