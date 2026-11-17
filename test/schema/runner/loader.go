package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/yaml"
)

const suiteFileSuffix = ".testsuite.yaml"

// crdFile is a CRD together with the file it was read from.
type crdFile struct {
	filename string
	crd      *apiextensionsv1.CustomResourceDefinition
}

// LoadTestSuiteSpecs walks testsDir recursively looking for files with the suffix `.testsuite.yaml`.
// It loads them in path order into SuiteSpec structs ready for the generator to generate the test cases.
// The CRD under test by each suite is the CRD in crdDir (non-recursive) named by the suite's crdName.
func LoadTestSuiteSpecs(testsDir, crdDir string) ([]SuiteSpec, error) {
	suiteFilenames, err := findSuiteFiles(testsDir)
	if err != nil {
		return nil, err
	}
	if len(suiteFilenames) == 0 {
		return nil, fmt.Errorf("no schema test suites found in %s", testsDir)
	}

	crdFiles, err := loadCRDFiles(crdDir)
	if err != nil {
		return nil, err
	}

	out := []SuiteSpec{}
	for _, filename := range suiteFilenames {
		suiteSpecs, err := loadSuiteFile(filename, crdFiles)
		if err != nil {
			return nil, fmt.Errorf("could not set up test suite: %w", err)
		}

		out = append(out, suiteSpecs...)
	}

	return out, nil
}

// findSuiteFiles returns the sorted paths of all suite files below dir.
func findSuiteFiles(dir string) ([]string, error) {
	filenames := []string{}

	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.IsDir() && strings.HasSuffix(path, suiteFileSuffix) {
			filenames = append(filenames, path)
		}

		return nil
	}); err != nil {
		return nil, fmt.Errorf("could not load files from path %q: %w", dir, err)
	}

	sort.Strings(filenames)

	return filenames, nil
}

// loadCRDFiles loads every `*.yaml` file directly in dir as a CRD.
func loadCRDFiles(dir string) ([]crdFile, error) {
	filenames, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("could not list CRD files in %q: %w", dir, err)
	}

	out := []crdFile{}
	for _, filename := range filenames {
		crd, err := loadCRDFromFile(filename)
		if err != nil {
			return nil, err
		}

		out = append(out, crdFile{filename: filename, crd: crd})
	}

	return out, nil
}

// loadSuiteFile loads and validates the SuiteSpecs of the given file name.
// A suite with targets yields one SuiteSpec per target, a suite without targets yields one.
func loadSuiteFile(path string, crdFiles []crdFile) ([]SuiteSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read file %q: %w", path, err)
	}

	s := SuiteSpec{}
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return nil, fmt.Errorf("could not unmarshal YAML file %q: %w", path, err)
	}

	if len(s.Targets) == 0 {
		suite, err := resolveSuite(path, s, nil, crdFiles)
		if err != nil {
			return nil, err
		}

		return []SuiteSpec{suite}, nil
	}

	if s.CRDName != "" || s.Version != "" {
		return nil, fmt.Errorf("test suite spec %q is invalid: targets must not be combined with `crdName` or `version`", path)
	}

	out := []SuiteSpec{}
	for _, target := range s.Targets {
		suite := s
		suite.Targets = nil
		suite.CRDName = target.CRDName
		suite.Version = target.Version

		resolved, err := resolveSuite(path, suite, &target, crdFiles)
		if err != nil {
			return nil, err
		}

		out = append(out, resolved)
	}

	return out, nil
}

// resolveSuite resolves the CRD under test of the suite and validates it.
// With a target, the suite documents are adapted to the target CRD first.
func resolveSuite(path string, s SuiteSpec, target *TargetSpec, crdFiles []crdFile) (SuiteSpec, error) {
	var err error

	if len(s.CRDName) == 0 {
		return SuiteSpec{}, fmt.Errorf("test suite spec %q is invalid: missing required field `crdName`", path)
	}

	matches := []crdFile{}
	for _, f := range crdFiles {
		if f.crd.Name == s.CRDName {
			matches = append(matches, f)
		}
	}
	if len(matches) != 1 {
		return SuiteSpec{}, fmt.Errorf("test suite spec %q is invalid: found %d CRDs named %q, expected exactly one", path, len(matches), s.CRDName)
	}
	s.CRDFilename = matches[0].filename

	s.Version, err = suiteVersion(s.Version, matches[0].crd)
	if err != nil {
		return SuiteSpec{}, fmt.Errorf("could not determine test suite CRD version for %q: %w", path, err)
	}

	s.CRD = crdForVersion(matches[0].crd, s.Version)

	if target != nil {
		if s.Tests, err = adaptTests(s.Tests, s.CRD, s.Version); err != nil {
			return SuiteSpec{}, fmt.Errorf("could not adapt test suite %q to %q: %w", path, target.CRDName, err)
		}
	}

	if err := validateSuiteSpec(s); err != nil {
		return SuiteSpec{}, fmt.Errorf("test suite spec %q is invalid: %w", path, err)
	}

	return s, nil
}

// suiteVersion returns the CRD version a suite tests. When the suite does not name a version,
// the CRD must have exactly one.
func suiteVersion(version string, crd *apiextensionsv1.CustomResourceDefinition) (string, error) {
	if version == "" {
		if len(crd.Spec.Versions) != 1 {
			return "", fmt.Errorf("CRD %q has %d versions, specify one in the suite", crd.Name, len(crd.Spec.Versions))
		}

		return crd.Spec.Versions[0].Name, nil
	}

	for _, v := range crd.Spec.Versions {
		if v.Name == version {
			return version, nil
		}
	}

	return "", fmt.Errorf("CRD %q has no version %q", crd.Name, version)
}

// crdForVersion returns a copy of the CRD that only has the given version.
// This allows testing of CRDs for versions that are not currently the storage version.
func crdForVersion(crd *apiextensionsv1.CustomResourceDefinition, version string) *apiextensionsv1.CustomResourceDefinition {
	out := crd.DeepCopy()
	out.Spec.Versions = nil

	for _, v := range crd.Spec.Versions {
		if v.Name != version {
			continue
		}

		v.Storage = true
		v.Served = true
		out.Spec.Versions = append(out.Spec.Versions, v)
	}

	return out
}

// validateSuiteSpec checks that the suite has test cases and that each case is well formed.
func validateSuiteSpec(suite SuiteSpec) error {
	if len(suite.Tests.OnCreate)+len(suite.Tests.OnUpdate) == 0 {
		return fmt.Errorf("suite has no onCreate or onUpdate cases")
	}

	for _, test := range suite.Tests.OnCreate {
		if err := validateOnCreateSpec(test, suite); err != nil {
			return fmt.Errorf("onCreate case %q: %w", test.Name, err)
		}
	}

	for _, test := range suite.Tests.OnUpdate {
		if err := validateOnUpdateSpec(test, suite); err != nil {
			return fmt.Errorf("onUpdate case %q: %w", test.Name, err)
		}
	}

	return nil
}

func validateOnCreateSpec(test OnCreateTestSpec, suite SuiteSpec) error {
	if test.Expected != "" && test.ExpectedError != "" {
		return fmt.Errorf("expected and expectedError must not both be set")
	}

	if err := validateDocument("initial", test.Initial, suite); err != nil {
		return err
	}

	if test.Expected != "" {
		return validateDocument("expected", test.Expected, suite)
	}

	return nil
}

func validateOnUpdateSpec(test OnUpdateTestSpec, suite SuiteSpec) error {
	expectations := 0
	for _, expectation := range []string{test.Expected, test.ExpectedError, test.ExpectedStatusError} {
		if expectation != "" {
			expectations++
		}
	}
	if expectations != 1 {
		return fmt.Errorf("exactly one of expected, expectedError and expectedStatusError must be set")
	}

	if err := validateDocument("initial", test.Initial, suite); err != nil {
		return err
	}

	if err := validateDocument("updated", test.Updated, suite); err != nil {
		return err
	}

	if test.Expected != "" {
		if err := validateDocument("expected", test.Expected, suite); err != nil {
			return err
		}
	}

	if test.ExpectedStatusError != "" {
		updated, err := newUnstructuredFrom([]byte(test.Updated))
		if err != nil {
			return fmt.Errorf("updated: %w", err)
		}

		if status, _, _ := unstructured.NestedFieldNoCopy(updated.Object, "status"); status == nil {
			return fmt.Errorf("expectedStatusError requires updated to contain a status")
		}
	}

	return nil
}

// validateDocument checks that the raw YAML document has the apiVersion and kind of the CRD under test.
func validateDocument(field, raw string, suite SuiteSpec) error {
	obj, err := newUnstructuredFrom([]byte(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}

	wantAPIVersion := fmt.Sprintf("%s/%s", suite.CRD.Spec.Group, suite.Version)
	if obj.GetAPIVersion() != wantAPIVersion {
		return fmt.Errorf("%s has apiVersion %q, expected %q", field, obj.GetAPIVersion(), wantAPIVersion)
	}

	if obj.GetKind() != suite.CRD.Spec.Names.Kind {
		return fmt.Errorf("%s has kind %q, expected %q", field, obj.GetKind(), suite.CRD.Spec.Names.Kind)
	}

	return nil
}

func loadCRDFromFile(filename string) (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("could not load CRD: %w", err)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(raw, crd); err != nil {
		return nil, fmt.Errorf("could not unmarshal CRD file %q: %w", filename, err)
	}

	return crd, nil
}
