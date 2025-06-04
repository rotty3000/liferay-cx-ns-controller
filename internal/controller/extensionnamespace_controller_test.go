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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tutils "github.com/liferay/liferay-portal/liferay-cx-ns-controller/test/utils"
)

var _ = Describe("Extension Namespace Controller", func() {
	const (
		namespace = "default"
	)

	Context("When reconciling a resource", func() {
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

			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())
			Eventually(func(g Gomega) {
				records := logTrap.GetRecords()
				// _, _ = fmt.Fprintf(GinkgoWriter, "\nController logs:\n %v", records)

				g.Expect(
					records,
				).To(
					ContainElement(
						MatchFields(
							IgnoreExtras,
							Fields{
								"KeysAndValues": ContainElement(extensionNamespaceControllerName),
								"Msg":           ContainSubstring("Predicate returned false, ignoring event"),
							},
						),
					),
				)
			}).Should(Succeed())
			Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
		})

		It("should ignore Namespaces with the first but not second expected label", func() {
			By("by creating a Namespace that has only the second expected label")

			nsName := tutils.GenerateRandomConfigMapName()

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						managedByResourceLabelKey: namespace,
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
			}

			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())
			Eventually(func(g Gomega) {
				records := logTrap.GetRecords()
				// _, _ = fmt.Fprintf(GinkgoWriter, "\nController logs:\n %v", records)

				g.Expect(
					records,
				).To(
					ContainElement(
						MatchFields(
							IgnoreExtras,
							Fields{
								"KeysAndValues": ContainElement(extensionNamespaceControllerName),
								"Msg":           ContainSubstring("Predicate returned false, ignoring event"),
							},
						),
					),
				)
			}).Should(Succeed())
			Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
		})

		It("should ignore Namespaces with the second but not first expected label", func() {
			By("by creating a Namespace that has only the first expected label")

			nsName := tutils.GenerateRandomConfigMapName()

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						liferayVirtualInstanceIdLabelKey: "vi1",
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
			}

			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())
			Eventually(func(g Gomega) {
				records := logTrap.GetRecords()
				// _, _ = fmt.Fprintf(GinkgoWriter, "\nController logs:\n %v", records)

				g.Expect(
					records,
				).To(
					ContainElement(
						MatchFields(
							IgnoreExtras,
							Fields{
								"KeysAndValues": ContainElement(extensionNamespaceControllerName),
								"Msg":           ContainSubstring("Predicate returned false, ignoring event"),
							},
						),
					),
				)
			}).Should(Succeed())
			Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
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

			Expect(k8sClient.Create(ctx, testConfigMap)).To(Succeed())

			By("then by creating a custom Namespace that has the expected labels")

			nsName := tutils.GenerateRandomConfigMapName()

			testNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
					Labels: map[string]string{
						managedByResourceLabelKey:        namespace,
						liferayVirtualInstanceIdLabelKey: virtualInstanceId,
					},
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
			}

			Expect(k8sClient.Create(ctx, testNamespace)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testNamespace), testNamespace)).To(Succeed())
				g.Expect(testNamespace.Labels[managedByLabelKey]).To(Equal(controllerGroupName))

				copiedCM := &corev1.ConfigMap{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cmName, Namespace: testNamespace.Name}, copiedCM)).To(Succeed())
				g.Expect(copiedCM.ObjectMeta.Labels).To(MatchKeys(IgnoreExtras, Keys{
					syncedFromConfigMapLabelKey:          Equal(cmName),
					syncedFromConfigMapNamespaceLabelKey: Equal(namespace),
					liferayVirtualInstanceIdLabelKey:     Equal(virtualInstanceId),
				}))
			}).Should(Succeed())
			Expect(k8sClient.Delete(ctx, testNamespace)).To(Succeed())
		})
	})
})
