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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/liferay/liferay-portal/liferay-cx-ns-controller/internal/utils"
	tutils "github.com/liferay/liferay-portal/liferay-cx-ns-controller/test/utils"
)

// namespace where the project is deployed in
const namespace = "liferay-cx-ns-controller-system"

// serviceAccountName created for the project
const serviceAccountName = "liferay-cx-ns-controller-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "liferay-cx-ns-controller-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "liferay-cx-ns-controller-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		_, err := tutils.Kubectl(nil, "create", "ns", namespace)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		_, err = tutils.Kubectl(nil, "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		// By("installing CRDs")
		// cmd = exec.Command("make", "install")
		// _, err = tutils.Run(cmd)
		// Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd := exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = tutils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		_, _ = tutils.Kubectl(nil, "delete", "pod", "curl-metrics", "-n", namespace)

		By("undeploying the controller-manager")
		cmd := exec.Command("make", "undeploy")
		_, _ = tutils.Run(cmd)

		// By("uninstalling CRDs")
		// cmd = exec.Command("make", "uninstall")
		// _, _ = tutils.Run(cmd)

		By("removing manager namespace")
		_, _ = tutils.Kubectl(nil, "delete", "ns", namespace)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			controllerLogs, err := tutils.GetLogs(controllerPodName, namespace)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			eventsOutput, err := tutils.Kubectl(nil, "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			metricsOutput, err := tutils.GetLogs("curl-metrics", namespace)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			podDescription, err := tutils.Kubectl(nil, "describe", "pod", controllerPodName, "-n", namespace)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				podOutput, err := tutils.Kubectl(nil, "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := tutils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				output, err := tutils.Kubectl(nil, "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			_, err := tutils.Kubectl(nil, "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=liferay-cx-ns-controller-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			_, err = tutils.Kubectl(nil, "get", "service", metricsServiceName, "-n", namespace)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				output, err := tutils.Kubectl(nil, "get", "endpoints", metricsServiceName, "-n", namespace)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				output, err := tutils.GetLogs(controllerPodName, namespace)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			_, err = tutils.Kubectl(nil, "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				output, err := tutils.Kubectl(nil, "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

	})

	Context("DXP Metadata ConfigMap tracking", func() {
		It("should ignore ConfigMaps without the type label", func() {
			By("by creating a ConfigMap that does not have the proper type label and making sure the controller ignores it")

			cmName := tutils.GenerateRandomConfigMapName()

			testConfigMap := &corev1.ConfigMap{
				Data: map[string]string{
					"test-key": "test-value",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: namespace,
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
			}

			_, err := tutils.Kubectl(testConfigMap, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create test ConfigMap", err)

			check := func(g Gomega) {
				controllerOutput, err := tutils.GetLogs(controllerPodName, namespace)
				g.Expect(err).NotTo(HaveOccurred())

				// _, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerOutput)

				g.Expect(controllerOutput).To(
					And(
						ContainSubstring(`Predicate returned false, ignoring event`),
						ContainSubstring(cmName)))
			}
			Eventually(check).Should(Succeed())
		})

		It("should ignore ConfigMaps with the sync label", func() {
			By("by creating a ConfigMap that has the sync label and making sure the controller ignores it")

			cmName := tutils.GenerateRandomConfigMapName()

			testConfigMap := &corev1.ConfigMap{
				Data: map[string]string{
					"test-key": "test-value",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: namespace,
					Labels: map[string]string{
						"lxc.liferay.com/synced-from-configmap": "foo",
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
			}

			_, err := tutils.Kubectl(testConfigMap, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create test ConfigMap", err)

			check := func(g Gomega) {
				controllerOutput, err := tutils.GetLogs(controllerPodName, namespace)
				g.Expect(err).NotTo(HaveOccurred())

				// _, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerOutput)

				g.Expect(controllerOutput).To(
					And(
						ContainSubstring(`Predicate returned false, ignoring event`),
						ContainSubstring(cmName)))
			}
			Eventually(check).Should(Succeed())
		})

		It("should create a default client extension namespace", func() {
			By(`by creating a ConfigMap that has the type label and virtual
				instance label and making sure the controller creates the
				default client extension namespace for it`)

			cmName := tutils.GenerateRandomConfigMapName()
			virtualInstanceId := tutils.GenerateRandomConfigMapName()
			expectedNamespaceName, _ := utils.VirtualInstanceIdToNamespace(namespace, virtualInstanceId, "default")

			testConfigMap := &corev1.ConfigMap{
				Data: map[string]string{
					"test-key": "test-value",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: namespace,
					Labels: map[string]string{
						"lxc.liferay.com/metadataType":          "dxp",
						"dxp.lxc.liferay.com/virtualInstanceId": virtualInstanceId,
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
			}

			_, err := tutils.Kubectl(testConfigMap, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create test ConfigMap", err)

			check := func(g Gomega) {
				// controllerOutput, _ := getLogs(controllerPodName, namespace)
				// g.Expect(err).NotTo(HaveOccurred())

				// _, _ = fmt.Fprintf(
				// 	GinkgoWriter, "=======================================\nController logs:\n %s", controllerOutput)

				output, err := tutils.Kubectl(nil, "get", "ns", "--selector", "dxp.lxc.liferay.com/virtualInstanceId="+virtualInstanceId,
					"-o", "jsonpath={.items[0].metadata.name}")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(expectedNamespaceName), "namespace wrong name")
			}
			Eventually(check).Should(Succeed())
			_, err = tutils.Kubectl(nil, "delete", "cm", cmName, "-n", namespace, "--wait", "--timeout", "5m")
			Expect(err).NotTo(HaveOccurred(), "Failed to delete ConfigMap", err)
		})
	})

	Context("Extension Namespace tracking", func() {
		It("should ignore Namespaces without the expected labels", func() {
			By("by creating a Namespace that does not have the proper labels and making sure the controller ignores it")

			nsName := tutils.GenerateRandomConfigMapName()

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
			}

			_, err := tutils.Kubectl(testNamespace, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace", err)
			Eventually(func(g Gomega) {
				controllerOutput, err := tutils.GetLogs(controllerPodName, namespace)
				g.Expect(err).NotTo(HaveOccurred())
				// _, _ = fmt.Fprintf(GinkgoWriter, "\nController logs:\n %v", records)

				g.Expect(controllerOutput).To(
					And(
						ContainSubstring(`Predicate returned false, ignoring event`),
						ContainSubstring(nsName)))
			}).Should(Succeed())
			_, err = tutils.Kubectl(nil, "delete", "ns", nsName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test namespace", err)
		})

		It("should ignore Namespaces with the first but not second expected label", func() {
			By("by creating a Namespace that has only the second expected label")

			nsName := tutils.GenerateRandomConfigMapName()

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by-resource": namespace,
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
			}

			_, err := tutils.Kubectl(testNamespace, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace", err)
			Eventually(func(g Gomega) {
				controllerOutput, err := tutils.GetLogs(controllerPodName, namespace)
				g.Expect(err).NotTo(HaveOccurred())
				// _, _ = fmt.Fprintf(GinkgoWriter, "\nController logs:\n %v", records)

				g.Expect(controllerOutput).To(
					And(
						ContainSubstring(`Predicate returned false, ignoring event`),
						ContainSubstring("extension-namespace-controller")))
			}).Should(Succeed())
			_, err = tutils.Kubectl(nil, "delete", "ns", nsName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test namespace", err)
		})

		It("should ignore Namespaces with the second but not first expected label", func() {
			By("by creating a Namespace that has only the first expected label")

			nsName := tutils.GenerateRandomConfigMapName()

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						"dxp.lxc.liferay.com/virtualInstanceId": "vi1",
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
			}

			_, err := tutils.Kubectl(testNamespace, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace", err)
			Eventually(func(g Gomega) {
				controllerOutput, err := tutils.GetLogs(controllerPodName, namespace)
				g.Expect(err).NotTo(HaveOccurred())
				// _, _ = fmt.Fprintf(GinkgoWriter, "\nController logs:\n %v", records)

				g.Expect(controllerOutput).To(
					And(
						ContainSubstring(`Predicate returned false, ignoring event`),
						ContainSubstring("extension-namespace-controller")))
			}).Should(Succeed())
			_, err = tutils.Kubectl(nil, "delete", "ns", nsName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test namespace", err)
		})

		It("should take over management of Namespaces with the expected labels", func() {
			By("creating a DXP metadata Config Map")

			cmName := tutils.GenerateRandomConfigMapName()
			virtualInstanceId := tutils.GenerateRandomConfigMapName()

			testConfigMap := &corev1.ConfigMap{
				Data: map[string]string{
					"test-key": "test-value",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: namespace,
					Labels: map[string]string{
						"lxc.liferay.com/metadataType":          "dxp",
						"dxp.lxc.liferay.com/virtualInstanceId": virtualInstanceId,
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
			}

			_, err := tutils.Kubectl(testConfigMap, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred())

			By("then by creating a custom Namespace that has the expected labels")

			nsName := tutils.GenerateRandomConfigMapName()

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by-resource": namespace,
						"dxp.lxc.liferay.com/virtualInstanceId": virtualInstanceId,
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
			}

			_, err = tutils.Kubectl(testNamespace, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace", err)
			Eventually(func(g Gomega) {
				nsContent, err := tutils.Kubectl(nil, "get", "ns", nsName, "-o", "yaml")
				g.Expect(err).NotTo(HaveOccurred())
				tutils.FromYAML(nsContent, testNamespace)
				g.Expect(testNamespace.Labels["app.kubernetes.io/managed-by"]).To(Equal("cx-liferay-controller-group"))

				copiedCM := &corev1.ConfigMap{}
				cmContent, err := tutils.Kubectl(nil, "get", "cm", cmName, "-n", testNamespace.Name, "-o", "yaml")
				g.Expect(err).NotTo(HaveOccurred())
				tutils.FromYAML(cmContent, copiedCM)
				g.Expect(copiedCM.ObjectMeta.Labels).To(MatchKeys(IgnoreExtras, Keys{
					"cx.liferay.com/synced-from-configmap":           Equal(cmName),
					"cx.liferay.com/synced-from-configmap-namespace": Equal(namespace),
					"dxp.lxc.liferay.com/virtualInstanceId":          Equal(virtualInstanceId),
				}))
			}).Should(Succeed())
			_, err = tutils.Kubectl(nil, "delete", "ns", nsName)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test namespace", err)
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	metricsOutput, err := tutils.GetLogs("curl-metrics", namespace)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
