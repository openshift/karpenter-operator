package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/google/go-cmp/cmp"
)

// CompareWithFixture compares output with a YAML fixture under the calling
// package's testdata directory. Set UPDATE=true to regenerate the fixture.
// Strings and byte slices are compared verbatim; other values are marshaled as YAML.
func CompareWithFixture(t *testing.T, output any) {
	t.Helper()

	actual, err := marshalFixture(output)
	if err != nil {
		t.Fatalf("marshal fixture output of type %T: %v", output, err)
	}
	path := fixturePath(t.Name())
	if os.Getenv("UPDATE") == "true" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(path, actual, 0644); err != nil { //nolint:gosec // Fixtures are reviewable test data, not secrets.
			t.Fatalf("update fixture %s: %v", path, err)
		}
	}

	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v\nTo create it, run UPDATE=true go test ./... -count=1 -run %q", path, err, t.Name())
	}
	if diff := cmp.Diff(string(expected), string(actual)); diff != "" {
		t.Errorf("fixture %s differs (-want +got):\n%s\nIf intentional, run UPDATE=true go test ./... -count=1 -run %q and review the fixture diff.", path, diff, t.Name())
	}
}

func marshalFixture(output any) ([]byte, error) {
	switch value := output.(type) {
	case []byte:
		return value, nil
	case string:
		return []byte(value), nil
	default:
		result, err := yaml.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("marshal YAML: %w", err)
		}
		return result, nil
	}
}

var unsafeFilenameCharacters = regexp.MustCompile("[^a-zA-Z0-9_.]+")

func fixturePath(testName string) string {
	return filepath.Join("testdata", "zz_fixture_"+unsafeFilenameCharacters.ReplaceAllString(testName, "_")+".yaml")
}
