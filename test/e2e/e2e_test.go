//go:build e2e

/*
 * Copyright The Kubernetes Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("Test memory allocation", func() {
	It("should allocate memory devices with capacity not exceeding request", func(ctx SpecContext) {
		namespace := "basic-resourceclaimtemplate"
		pods := []string{"pod0", "pod1"}
		containerName := "ctr0"
		expectedDeviceCount := 1
		// Expected capacity is 1Gi as defined in basic-resourceclaimtemplate.yaml
		expectedCapacity := "1Gi"

		deployManifest(ctx, namespace, "basic-resourceclaimtemplate.yaml")
		checkPodsReadyAndRunning(ctx, namespace, pods)

		for _, podName := range pods {
			verifyMemoryAllocationWithCapacity(ctx, namespace, podName, containerName, expectedDeviceCount, expectedCapacity)
		}
	})
})
