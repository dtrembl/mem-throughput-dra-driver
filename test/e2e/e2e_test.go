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

	It("should handle overcapacity requests with one pod pending", func(ctx SpecContext) {
		namespace := "basic-resourceclaimtemplate"
		pods := []string{"pod0", "pod1"}
		containerName := "ctr0"
		expectedDeviceCount := 1
		// Expected capacity is 40Gi as defined in basic-oneovercapacity.yaml
		requestedCapacity := "40Gi"
		// Maximum available capacity on any single NUMA node is 40Gi (numa-3)
		maxAvailableCapacity := "40Gi"

		deployManifest(ctx, namespace, "basic-oneovercapacity.yaml")

		// Wait for one pod to be running and one to remain pending
		// We don't know which one will succeed, so we check both possibilities
		checkOneOfTwoPodsRunning(ctx, namespace, pods)

		// Find which pod is running
		runningPod := findRunningPod(ctx, namespace, pods)
		pendingPod := pods[0]
		if runningPod == pods[0] {
			pendingPod = pods[1]
		}

		// Verify the running pod has correct allocation and doesn't exceed capacity
		verifyMemoryAllocationWithCapacity(ctx, namespace, runningPod, containerName, expectedDeviceCount, requestedCapacity)
		
		// Verify the allocated capacity does not exceed the maximum available
		verifyAllocatedCapacityWithinLimit(ctx, namespace, runningPod, containerName, maxAvailableCapacity)

		// Verify the other pod is pending due to insufficient resources
		checkPodPendingDueToInsufficientResources(ctx, namespace, pendingPod)
	})

	It("should reject all pods when requests exceed system capacity", func(ctx SpecContext) {
		namespace := "basic-resourceclaimtemplate"
		pods := []string{"pod0", "pod1"}
		// Requested capacity is 50Gi as defined in basic-overcapacity.yaml
		requestedCapacity := "50Gi"
		// Maximum available capacity on any single NUMA node is 40Gi (numa-3)
		maxSystemCapacity := "40Gi"

		deployManifest(ctx, namespace, "basic-overcapacity.yaml")

		// Wait and verify that both pods remain in Pending state
		// since no NUMA node has 50Gi capacity (max is 40Gi on numa-3)
		checkAllPodsPending(ctx, namespace, pods)

		// Verify both pods are pending due to insufficient resources
		for _, podName := range pods {
			checkPodPendingDueToInsufficientResources(ctx, namespace, podName)
			
			// Verify the requested capacity exceeds system capacity
			verifyRequestExceedsSystemCapacity(ctx, namespace, podName, requestedCapacity, maxSystemCapacity)
		}
	})

	It("should allocate multiple pods across NUMA nodes without exceeding capacity", func(ctx SpecContext) {
		namespace := "basic-resourceclaimtemplate"
		pods := []string{"pod0", "pod1", "pod2", "pod3", "pod4", "pod5"}
		containerName := "ctr0"
		expectedDeviceCount := 1
		// Each pod requests 10Gi as defined in basic-multicapacity.yaml
		requestedCapacity := "10Gi"
		// NUMA node capacities: numa-0=10Gi, numa-1=20Gi, numa-2=30Gi, numa-3=40Gi
		numaCapacities := map[string]string{
			"numa-0": "10Gi",
			"numa-1": "20Gi",
			"numa-2": "30Gi",
			"numa-3": "40Gi",
		}

		deployManifest(ctx, namespace, "basic-multicapacity.yaml")

		// All pods should become ready since total request (60Gi) <= total capacity (100Gi)
		checkPodsReadyAndRunning(ctx, namespace, pods)

		// Track allocations per NUMA node
		numaAllocations := make(map[string][]string)

		// Verify each pod's allocation and track NUMA node usage
		for _, podName := range pods {
			verifyMemoryAllocationWithCapacity(ctx, namespace, podName, containerName, expectedDeviceCount, requestedCapacity)
			
			// Get the NUMA node assigned to this pod and track it
			numaNode := getAssignedNumaNode(ctx, namespace, podName, containerName)
			numaAllocations[numaNode] = append(numaAllocations[numaNode], podName)
		}

		// Verify that no NUMA node's capacity has been exceeded
		verifyNumaCapacityNotExceeded(ctx, namespace, numaAllocations, requestedCapacity, numaCapacities)
	})
})
