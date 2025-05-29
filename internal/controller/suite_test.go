/*
Copyright 2025.

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

package controller

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx        context.Context
	cancel     context.CancelFunc
	testEnv    *envtest.Environment
	cfg        *rest.Config
	k8sClient  client.Client
	k8sManager ctrl.Manager
	logTrap    LogTrapSink
)

type Record struct {
	Error         *error
	KeysAndValues []interface{}
	Level         int
	Msg           string
	Name          string // Name of the logger that produced this record
}

type Records struct {
	Records []Record
}

// LogTrapSink is a logr.LogSink that captures log records for testing.
type LogTrapSink struct {
	// keysAndValues are the prefix key-value pairs for this specific sink instance.
	keysAndValues []interface{}
	// name is the name of this specific sink instance.
	name string

	// records is a pointer to the shared slice where all log records are stored.
	// This allows multiple sink instances (created by WithValues/WithName) to log to the same place.
	records *[]Record
	// mu protects access to the shared records slice.
	mu *sync.Mutex
}

// Ensure LogTrapSink implements logr.LogSink.
// Note: We will pass *LogTrapSink to logr.New(), so methods like Info, Error, Enabled, Init
// will be called with a pointer receiver. WithValues and WithName must have value receivers
// to satisfy the interface and should return a new logr.LogSink (which can be *LogTrapSink).
var _ logr.LogSink = &LogTrapSink{}

// Init is part of the logr.LogSink interface.
func (t *LogTrapSink) Init(_ logr.RuntimeInfo) {}

// Enabled is part of the logr.LogSink interface.
func (t *LogTrapSink) Enabled(_ int) bool {
	return true
}

// Info is part of the logr.LogSink interface.
func (t *LogTrapSink) Info(level int, msg string, keysAndValues ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Combine the sink's prefix KVs with the KVs from this specific Info call.
	finalKVs := make([]interface{}, len(t.keysAndValues)+len(keysAndValues))
	copy(finalKVs, t.keysAndValues)
	copy(finalKVs[len(t.keysAndValues):], keysAndValues)

	record := Record{
		Level:         level,
		Msg:           msg,
		KeysAndValues: finalKVs,
		Name:          t.name,
	}
	if t.records == nil {
		// This should not happen if logTrap is initialized correctly
		return
	}
	*t.records = append(*t.records, record)
}

// Error is part of the logr.LogSink interface.
func (t *LogTrapSink) Error(err error, msg string, keysAndValues ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()

	finalKVs := make([]interface{}, len(t.keysAndValues)+len(keysAndValues))
	copy(finalKVs, t.keysAndValues)
	copy(finalKVs[len(t.keysAndValues):], keysAndValues)

	record := Record{
		Error:         &err,
		Msg:           msg,
		KeysAndValues: finalKVs,
		Name:          t.name,
	}
	if t.records == nil {
		return
	}
	*t.records = append(*t.records, record)
}

// WithValues is part of the logr.LogSink interface. It must have a value receiver.
func (t LogTrapSink) WithValues(keysAndValues ...any) logr.LogSink {
	newKVs := make([]interface{}, len(t.keysAndValues)+len(keysAndValues))
	copy(newKVs, t.keysAndValues)
	copy(newKVs[len(t.keysAndValues):], keysAndValues)

	return &LogTrapSink{
		keysAndValues: newKVs,
		name:          t.name,
		records:       t.records, // Share the pointer to the slice and the mutex
		mu:            t.mu,
	}
}

// WithName is part of the logr.LogSink interface. It must have a value receiver.
func (t LogTrapSink) WithName(name string) logr.LogSink {
	newName := t.name
	if t.name != "" && name != "" {
		newName = t.name + "/" + name
	} else if name != "" {
		newName = name
	}

	// Copy current KVs for the new sink
	kvsCopy := make([]interface{}, len(t.keysAndValues))
	copy(kvsCopy, t.keysAndValues)

	return &LogTrapSink{
		keysAndValues: kvsCopy,
		name:          newName,
		records:       t.records, // Share the pointer to the slice and the mutex
		mu:            t.mu,
	}
}

// Clear removes all captured records.
func (t *LogTrapSink) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.records != nil {
		*t.records = []Record{}
	}
}

// GetRecords returns a copy of the captured records.
func (t *LogTrapSink) GetRecords() []Record {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.records == nil || *t.records == nil {
		return nil
	}
	// Return a copy to prevent modification issues if the caller modifies the slice
	// while new logs are being added.
	recsCopy := make([]Record, len(*t.records))
	copy(recsCopy, *t.records)
	return recsCopy
}

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	// logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))) // We'll use logTrap for the manager

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = corev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: false,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Initialize logTrap
	sharedRecords := make([]Record, 0)
	logTrap = LogTrapSink{
		records: &sharedRecords,
		mu:      &sync.Mutex{},
	}

	// Create a logger that writes to logTrap
	testLogger := logr.New(&logTrap)
	// Optionally, set the global logger if other parts of the code use logf.Log directly
	// and you want to capture those too. For this specific problem, ensuring the manager
	// and its derived loggers (like for the predicate) use logTrap is key.
	// logf.SetLogger(testLogger)

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Logger: testLogger, // Use the logger that writes to logTrap
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	// setup our controller
	err = (&ClientExtensionNamespaceReconciler{
		Client: k8sManager.GetClient(),
		Scheme: k8sManager.GetScheme(),
	}).SetupWithManager(k8sManager, true)
	Expect(err).ToNot(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()
})

var _ = AfterEach(func() {
	By("Clear the LogTrapSink")
	logTrap.Clear()
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
