//go:build envtest

// This runner is adapted from github.com/openshift/api/tests. It differs from the original as follows:
//   - There is no feature-gate, cluster-profile or feature-set handling. The CRD under test is the
//     file in the CRD directory whose metadata.name equals the suite's crdName.
//   - Suite files are found by walking the tests directory for `*.testsuite.yaml`, sorted by path.
//     Finding none is an error.
//   - Suite files are parsed strictly, and each suite is validated when it is loaded.
//   - `expected` is optional for onCreate cases, and `expectedError` must be a validation error.
//   - CRD patches are applied with github.com/evanphx/json-patch/v5.
//   - Each suite continues after a failed case, CRD installation is retried, and CRD removal is awaited.

package schema

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/komega"
	"sigs.k8s.io/yaml"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

const suiteFileSuffix = ".testsuite.yaml"

var (
	timeout = 5 * time.Second
)

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
		suite, err := loadSuiteFile(filename, crdFiles)
		if err != nil {
			return nil, fmt.Errorf("could not set up test suite: %w", err)
		}

		out = append(out, suite)
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

// loadSuiteFile loads and validates an individual SuiteSpec from the given file name.
func loadSuiteFile(path string, crdFiles []crdFile) (SuiteSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SuiteSpec{}, fmt.Errorf("could not read file %q: %w", path, err)
	}

	s := SuiteSpec{}
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return SuiteSpec{}, fmt.Errorf("could not unmarshal YAML file %q: %w", path, err)
	}

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

// GenerateTestSuite generates a Ginkgo test suite from the provided SuiteSpec.
func GenerateTestSuite(suiteSpec SuiteSpec) {
	suiteName := generateSuiteName(suiteSpec)

	Describe(suiteName, Ordered, ContinueOnFailure, func() {
		var crdOptions envtest.CRDInstallOptions
		var crd *apiextensionsv1.CustomResourceDefinition

		BeforeEach(OncePerOrdered, func() {
			Expect(k8sClient).ToNot(BeNil(), "Kubernetes client is not initialized")

			// A previous suite for the same CRD may still be deleting it, so retry the installation.
			// Each attempt installs a fresh copy because a failed attempt can leave a resourceVersion on the CRD.
			var crds []*apiextensionsv1.CustomResourceDefinition
			Eventually(func() error {
				var err error
				crds, err = envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{
					CRDs: []*apiextensionsv1.CustomResourceDefinition{
						suiteSpec.CRD.DeepCopy(),
					},
				})

				return err
			}, "120s", "1s").Should(Succeed(), "CRD should install successfully")

			Expect(crds).To(HaveLen(1), "Only one CRD should have been installed")
			crd = crds[0]

			crdOptions = envtest.CRDInstallOptions{CRDs: crds}
			Expect(envtest.WaitForCRDs(cfg, crds, crdOptions)).To(Succeed())
		})

		AfterEach(func() {
			// Remove all of the resources we created during the test.
			for _, u := range newUnstructuredsFor(crd) {
				Expect(k8sClient.DeleteAllOf(ctx, u, client.InNamespace("default"))).To(Succeed())
			}
		})

		AfterEach(OncePerOrdered, func() {
			// Remove the CRD and wait for it to be removed from the API.
			// If we don't wait then subsequent tests may fail.
			Expect(envtest.UninstallCRDs(cfg, crdOptions)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(crd), &apiextensionsv1.CustomResourceDefinition{})

				return apierrors.IsNotFound(err)
			}, "120s", "1s").Should(BeTrue(), fmt.Sprintf("CRD %s should be fully removed", crd.Name))
		})

		generateOnCreateTable(suiteSpec.Tests.OnCreate)
		generateOnUpdateTable(suiteSpec.Tests.OnUpdate, suiteSpec.CRDFilename)
	})
}

// generateSuiteName prepends the specified suite name with the GVR string
// for the CRD under test.
func generateSuiteName(suiteSpec SuiteSpec) string {
	gvr := schema.GroupVersionResource{
		Group:    suiteSpec.CRD.Spec.Group,
		Resource: suiteSpec.CRD.Spec.Names.Plural,
		Version:  suiteSpec.Version,
	}

	return fmt.Sprintf("[%s] %s", gvr.String(), suiteSpec.Name)
}

// generateOnCreateTable generates a table of tests from the defined OnCreate tests
// within the test suite test spec.
func generateOnCreateTable(onCreateTests []OnCreateTestSpec) {
	type onCreateTableInput struct {
		initial       []byte
		expected      []byte
		expectedError string
	}

	// assertOnCreate runs the actual test for each table entry
	var assertOnCreate any = func(in onCreateTableInput) {
		initialObj, err := newUnstructuredFrom(in.initial)
		Expect(err).ToNot(HaveOccurred(), "initial data should be a valid Kubernetes YAML resource")

		err = k8sClient.Create(ctx, initialObj)
		if in.expectedError != "" {
			assertValidationError(err, in.expectedError)
			return
		}
		Expect(err).ToNot(HaveOccurred())

		if len(in.expected) == 0 {
			return
		}

		// Fetch the object we just created from the API.
		gotObj := newEmptyUnstructuredFrom(initialObj)
		Expect(k8sClient.Get(ctx, objectKey(initialObj), gotObj)).To(Succeed())

		expectedObj, err := newUnstructuredFrom(in.expected)
		Expect(err).ToNot(HaveOccurred(), "expected data should be a valid Kubernetes YAML resource")

		// Ensure the name and namespace match.
		// The IgnoreAutogeneratedMetadata will ignore any additional meta set in the API.
		expectedObj.SetName(gotObj.GetName())
		expectedObj.SetNamespace(gotObj.GetNamespace())

		Expect(gotObj).To(komega.EqualObject(expectedObj, komega.IgnoreAutogeneratedMetadata))
	}

	// First argument to the table is the test function.
	tableEntries := []any{assertOnCreate}

	// Convert the test specs into table entries
	for _, testEntry := range onCreateTests {
		tableEntries = append(tableEntries, Entry(testEntry.Name, onCreateTableInput{
			initial:       []byte(testEntry.Initial),
			expected:      []byte(testEntry.Expected),
			expectedError: testEntry.ExpectedError,
		}))
	}

	if len(tableEntries) > 1 {
		DescribeTable("On Create", tableEntries...)
	}
}

// generateOnUpdateTable generates a table of tests from the defined OnUpdate tests
// within the test suite test spec.
func generateOnUpdateTable(onUpdateTests []OnUpdateTestSpec, crdFileName string) {
	type onUpdateTableInput struct {
		crdPatches          []Patch
		initial             []byte
		updated             []byte
		expected            []byte
		expectedError       string
		expectedStatusError string
	}

	var assertOnUpdate any = func(in onUpdateTableInput) {
		var originalCRDObjectKey client.ObjectKey
		var originalCRDSpec apiextensionsv1.CustomResourceDefinitionSpec

		initialObj, err := newUnstructuredFrom(in.initial)
		Expect(err).ToNot(HaveOccurred(), "initial data should be a valid Kubernetes YAML resource")

		if len(in.crdPatches) > 0 {
			patchedCRD, err := getPatchedCRD(crdFileName, in.crdPatches)
			Expect(err).ToNot(HaveOccurred(), "could not load patched crd")

			originalCRDObjectKey = objectKey(patchedCRD)

			originalCRD := &apiextensionsv1.CustomResourceDefinition{}
			Expect(k8sClient.Get(ctx, originalCRDObjectKey, originalCRD)).To(Succeed())

			originalCRDSpec = *originalCRD.Spec.DeepCopy()
			originalCRD.Spec = patchedCRD.Spec

			// Add a sentinel field so that we can check that the schema update has persisted.
			originalCRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["sentinel"] = apiextensionsv1.JSONSchemaProps{
				Type: "string",
				Enum: []apiextensionsv1.JSON{
					{Raw: []byte(fmt.Sprintf(`"%s+patched"`, initialObj.GetUID()))},
				},
			}
			initialObj.Object["sentinel"] = initialObj.GetUID() + "+patched"

			Expect(k8sClient.Update(ctx, originalCRD)).To(Succeed(), "failed updating patched CRD schema")
		}

		initialStatus, ok, err := unstructured.NestedFieldNoCopy(initialObj.Object, "status")
		Expect(err).ToNot(HaveOccurred())
		if ok {
			Expect(initialStatus).ToNot(BeNil())
		}

		// Use an eventually here, so that we retry until the sentinel correctly applies.
		Eventually(func() error {
			return k8sClient.Create(ctx, initialObj)
		}, timeout).Should(Succeed(), "initial object should create successfully")

		if initialStatus != nil {
			Expect(unstructured.SetNestedField(initialObj.Object, initialStatus, "status")).To(Succeed(), "should be able to restore initial status")
			Expect(k8sClient.Status().Update(ctx, initialObj)).ToNot(HaveOccurred(), "initial object status should update successfully")
		}

		if len(in.crdPatches) > 0 {
			originalCRD := &apiextensionsv1.CustomResourceDefinition{}
			Expect(k8sClient.Get(ctx, originalCRDObjectKey, originalCRD)).To(Succeed())

			originalCRD.Spec = originalCRDSpec

			// Add a sentinel field so that we can check that the schema update has persisted.
			originalCRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["sentinel"] = apiextensionsv1.JSONSchemaProps{
				Type: "string",
				Enum: []apiextensionsv1.JSON{
					{Raw: []byte(fmt.Sprintf(`"%s+restored"`, initialObj.GetUID()))},
				},
			}

			Expect(k8sClient.Update(ctx, originalCRD)).To(Succeed())

			Eventually(func() error {
				updatedObj := initialObj.DeepCopy()
				updatedObj.Object["sentinel"] = initialObj.GetUID() + "+restored"

				return k8sClient.Update(ctx, updatedObj)
			}, timeout).Should(Succeed(), "Sentinel should be persisted")

			// Drop the sentinel field now we know the rest of the CRD schema is up to date.
			originalCRD.Spec = originalCRDSpec
			Expect(k8sClient.Update(ctx, originalCRD)).To(Succeed())
		}

		// Fetch the object we just created from the API.
		gotObj := newEmptyUnstructuredFrom(initialObj)
		Expect(k8sClient.Get(ctx, objectKey(initialObj), gotObj)).To(Succeed())

		updatedObj, err := newUnstructuredFrom(in.updated)
		Expect(err).ToNot(HaveOccurred(), "updated data should be a valid Kubernetes YAML resource")

		updatedObjStatus, ok, err := unstructured.NestedFieldNoCopy(updatedObj.Object, "status")
		Expect(err).ToNot(HaveOccurred())
		if ok {
			Expect(updatedObjStatus).ToNot(BeNil())
		}

		// The updated object needs the following fields copied over.
		updatedObj.SetName(gotObj.GetName())
		updatedObj.SetNamespace(gotObj.GetNamespace())
		updatedObj.SetResourceVersion(gotObj.GetResourceVersion())

		err = k8sClient.Update(ctx, updatedObj)
		if in.expectedError != "" {
			assertValidationError(err, in.expectedError)
			return
		}
		Expect(err).ToNot(HaveOccurred(), "unexpected error updating spec")

		if updatedObjStatus != nil {
			Expect(unstructured.SetNestedField(updatedObj.Object, updatedObjStatus, "status")).To(Succeed(), "should be able to restore updated status")

			err := k8sClient.Status().Update(ctx, updatedObj)
			if in.expectedStatusError != "" {
				assertValidationError(err, in.expectedStatusError)
				return
			}
			Expect(err).ToNot(HaveOccurred(), "unexpected error updating status")
		}

		Expect(k8sClient.Get(ctx, objectKey(initialObj), gotObj)).To(Succeed())

		expectedObj, err := newUnstructuredFrom(in.expected)
		Expect(err).ToNot(HaveOccurred(), "expected data should be a valid Kubernetes YAML resource")

		// Ensure the name and namespace match.
		// The IgnoreAutogeneratedMetadata will ignore any additional meta set in the API.
		expectedObj.SetName(gotObj.GetName())
		expectedObj.SetNamespace(gotObj.GetNamespace())

		Expect(gotObj).To(komega.EqualObject(expectedObj, komega.IgnoreAutogeneratedMetadata))
	}

	// First argument to the table is the test function.
	tableEntries := []any{assertOnUpdate}

	// Convert the test specs into table entries
	for _, testEntry := range onUpdateTests {
		tableEntries = append(tableEntries, Entry(testEntry.Name, onUpdateTableInput{
			crdPatches:          testEntry.InitialCRDPatches,
			initial:             []byte(testEntry.Initial),
			updated:             []byte(testEntry.Updated),
			expected:            []byte(testEntry.Expected),
			expectedError:       testEntry.ExpectedError,
			expectedStatusError: testEntry.ExpectedStatusError,
		}))
	}

	if len(tableEntries) > 1 {
		DescribeTable("On Update", tableEntries...)
	}
}

// assertValidationError asserts that err is a validation error whose message contains substring.
func assertValidationError(err error, substring string) {
	GinkgoHelper()

	Expect(err).To(HaveOccurred(), "expected an error containing %q", substring)
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected a validation error, got: %v", err)
	Expect(err.Error()).To(ContainSubstring(substring))
}

// newUnstructuredsFor creates a set of unstructured resources for each version of the CRD.
// This allows us to ensure all CR instances are deleted after each test.
func newUnstructuredsFor(crd *apiextensionsv1.CustomResourceDefinition) []*unstructured.Unstructured {
	out := []*unstructured.Unstructured{}

	for _, version := range crd.Spec.Versions {
		u := &unstructured.Unstructured{}

		u.SetAPIVersion(fmt.Sprintf("%s/%s", crd.Spec.Group, version.Name))
		u.SetKind(crd.Spec.Names.Kind)

		out = append(out, u)
	}

	return out
}

// newUnstructuredFrom unmarshals the raw YAML data into an unstructured,
// and then sets the namespace and generateName ahead of the test.
func newUnstructuredFrom(raw []byte) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}

	if err := k8syaml.Unmarshal(raw, &u.Object); err != nil {
		return nil, fmt.Errorf("could not unmarshal raw YAML: %w", err)
	}

	// Names should be unique for each test so ensure we generate a name
	u.SetGenerateName("test-")
	// We need to have a namespace, use the default.
	u.SetNamespace("default")

	return u, nil
}

// newEmptyUnstructuredFrom creates a new unstructured with the same GVK as the input object,
// all other fields are cleared.
func newEmptyUnstructuredFrom(initial *unstructured.Unstructured) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}

	if initial != nil {
		u.GetObjectKind().SetGroupVersionKind(initial.GetObjectKind().GroupVersionKind())
	}

	return u
}

// objectKey extracts a client.ObjectKey from the given object.
func objectKey(obj client.Object) client.ObjectKey {
	return client.ObjectKey{Namespace: obj.GetNamespace(), Name: obj.GetName()}
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
