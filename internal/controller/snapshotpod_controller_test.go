/*
Copyright 2024.

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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	snapshotpodv1alpha1 "github.com/baizeai/kube-snapshot/api/v1alpha1"
)

var _ = Describe("SnapshotPod Controller", func() {
	Context("When testing withRandomTag function", func() {
		It("should return original tag when appendSnappedSuffix is false", func() {
			By("Testing with appendSnappedSuffix = false")

			// Test case 1: empty tag
			result := withRandomTag("", false)
			Expect(result).To(Equal(""))

			// Test case 2: latest tag
			result = withRandomTag("latest", false)
			Expect(result).To(Equal("latest"))

			// Test case 3: custom tag
			result = withRandomTag("v1.0.0", false)
			Expect(result).To(Equal("v1.0.0"))

			// Test case 4: tag with special characters
			result = withRandomTag("release-2024-01", false)
			Expect(result).To(Equal("release-2024-01"))
		})

		It("should append snapped suffix when appendSnappedSuffix is true", func() {
			By("Testing with appendSnappedSuffix = true")

			// Test case 1: empty tag
			result := withRandomTag("", true)
			Expect(result).To(ContainSubstring("snapped-"))
			Expect(result).To(MatchRegexp(`^snapped-\d+-[a-f0-9]{8}$`))

			// Test case 2: latest tag
			result = withRandomTag("latest", true)
			Expect(result).To(ContainSubstring("snapped-"))
			Expect(result).To(MatchRegexp(`^snapped-\d+-[a-f0-9]{8}$`))

			// Test case 3: custom tag
			result = withRandomTag("v1.0.0", true)
			Expect(result).To(ContainSubstring("v1.0.0-snapped-"))
			Expect(result).To(MatchRegexp(`^v1\.0\.0-snapped-\d+-[a-f0-9]{8}$`))

			// Test case 4: tag with special characters
			result = withRandomTag("release-2024-01", true)
			Expect(result).To(ContainSubstring("release-2024-01-snapped-"))
			Expect(result).To(MatchRegexp(`^release-2024-01-snapped-\d+-[a-f0-9]{8}$`))
		})

		It("should not append suffix to already snapped tags", func() {
			By("Testing with tags that already contain 'snapped-'")

			// Test case 1: already snapped tag
			alreadySnapped := "v1.0.0-snapped-1704067200-abc12345"
			result := withRandomTag(alreadySnapped, true)
			Expect(result).To(Equal(alreadySnapped))

			// Test case 2: already snapped tag with appendSnappedSuffix = false
			result = withRandomTag(alreadySnapped, false)
			Expect(result).To(Equal(alreadySnapped))
		})

		It("should generate unique suffixes for different calls", func() {
			By("Testing that multiple calls generate different suffixes")

			result1 := withRandomTag("test", true)
			result2 := withRandomTag("test", true)

			Expect(result1).NotTo(Equal(result2))
			Expect(result1).To(ContainSubstring("test-snapped-"))
			Expect(result2).To(ContainSubstring("test-snapped-"))
		})
	})

	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		snapshotpod := &snapshotpodv1alpha1.SnapshotPod{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind SnapshotPod")
			err := k8sClient.Get(ctx, typeNamespacedName, snapshotpod)
			if err != nil && errors.IsNotFound(err) {
				resource := &snapshotpodv1alpha1.SnapshotPod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					// TODO(user): Specify other spec details if needed.
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &snapshotpodv1alpha1.SnapshotPod{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance SnapshotPod")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &SnapshotPodReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
