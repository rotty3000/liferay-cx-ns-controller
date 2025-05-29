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
	"fmt"

	"github.com/liferay/liferay-portal/liferay-cx-ns-controller/internal/utils"
	tutils "github.com/liferay/liferay-portal/liferay-cx-ns-controller/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("ConfigMap Controller", func() {
	const (
		namespace = "default"
	)

	Context("When reconciling a resource", func() {
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

			Expect(k8sClient.Create(ctx, testConfigMap)).To(Succeed())
			Eventually(func(g Gomega) {
				records := logTrap.GetRecords()
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %v", records)
				g.Expect(records).To(ContainElement(HaveField("Msg", ContainSubstring("Predicate returned false, ignoring event"))))
			}).Should(Succeed())
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

			Expect(k8sClient.Create(ctx, testConfigMap)).To(Succeed())

			check := func(g Gomega) {
				records := logTrap.GetRecords()
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %v", records)
				g.Expect(records).To(ContainElement(HaveField("Msg", ContainSubstring("Predicate returned false, ignoring event"))))
			}
			Eventually(check).Should(Succeed())
		})

		It("should create a default client extension namespace", func() {
			By("by creating a ConfigMap that has the type label and virtual instance label and making sure the controller creates the default client extension namespace for it")

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

			Expect(k8sClient.Create(ctx, testConfigMap)).To(Succeed())

			check := func(g Gomega) {
				ns := &corev1.NamespaceList{}
				labelSelector := client.MatchingLabels{
					"dxp.lxc.liferay.com/virtualInstanceId": virtualInstanceId,
				}
				err := k8sClient.List(ctx, ns, labelSelector)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ns.Items).To(HaveLen(1))
				g.Expect(ns.Items[0].Name).To(Equal(expectedNamespaceName))
			}
			Eventually(check).Should(Succeed())
		})
	})
})
