//go:build envtest

package schema

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/komega"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var testScheme *k8sruntime.Scheme
var ctx = context.Background()
var suites []SuiteSpec

func TestSchema(t *testing.T) {
	RegisterFailHandler(Fail)

	g := NewGomegaWithT(t)

	// Resolve the CRD directory relative to this file so that the tests do not depend on the working directory.
	_, thisFile, _, ok := runtime.Caller(0)
	g.Expect(ok).To(BeTrue(), "could not determine the path of the test file")
	crdDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "pkg", "assets", "hypershift")

	var err error
	suites, err = LoadTestSuiteSpecs(filepath.Join(crdDir, "tests"), crdDir)
	g.Expect(err).ToNot(HaveOccurred())

	RunSpecs(t, "NodeClass CRD Schema Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// The runner reads and updates CRDs through the client in addition to the custom resources under test.
	testScheme = k8sruntime.NewScheme()
	Expect(clientgoscheme.AddToScheme(testScheme)).To(Succeed())
	Expect(apiextensionsv1.AddToScheme(testScheme)).To(Succeed())

	k8sClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	komega.SetClient(k8sClient)
	komega.SetContext(ctx)
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

var _ = Describe("", func() {
	for _, suite := range suites {
		GenerateTestSuite(suite)
	}
})
