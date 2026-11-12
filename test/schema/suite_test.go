//go:build envtest

package schema

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/test/schema/runner"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/komega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var env runner.Env
var testEnv *envtest.Environment
var ctx = context.Background()
var suites []runner.SuiteSpec

func TestSchema(t *testing.T) {
	RegisterFailHandler(Fail)

	g := NewGomegaWithT(t)

	// Resolve the CRD directory relative to this file so that the tests do not depend on the working directory.
	_, thisFile, _, ok := runtime.Caller(0)
	g.Expect(ok).To(BeTrue(), "could not determine the path of the test file")
	crdDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "pkg", "assets", "hypershift")

	var err error
	suites, err = runner.LoadTestSuiteSpecs(filepath.Join(crdDir, "tests"), crdDir)
	g.Expect(err).ToNot(HaveOccurred())

	RunSpecs(t, schemaSuiteName())
}

// schemaSuiteName names the junit suite after the kube-apiserver the suite ran against.
func schemaSuiteName() string {
	version := os.Getenv("SCHEMA_TEST_K8S_VERSION")
	if version == "" {
		return "NodeClass CRD schema"
	}

	return fmt.Sprintf("NodeClass CRD schema (k8s %s)", version)
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{}

	var err error
	env.Config, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(env.Config).NotTo(BeNil())

	// The runner reads and updates CRDs through the client in addition to the custom resources under test.
	scheme := k8sruntime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(apiextensionsv1.AddToScheme(scheme)).To(Succeed())

	env.Client, err = client.New(env.Config, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(env.Client).NotTo(BeNil())

	komega.SetClient(env.Client)
	komega.SetContext(ctx)
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

var _ = Describe("", func() {
	for _, suite := range suites {
		runner.GenerateTestSuite(&env, suite)
	}
})
