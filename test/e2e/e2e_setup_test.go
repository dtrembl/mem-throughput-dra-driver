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
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

var rootDir, currentDir, demoManifestsDir string
var clientset *kubernetes.Clientset
var dynamicClient dynamic.Interface
var restMapper meta.RESTMapper

const driverNamespace = "dra-memory-driver"
const driverPodSelector = "app.kubernetes.io/component=kubeletplugin"

func init() {
	currentDir, _ = os.Getwd()
	rootDir = filepath.Join(filepath.Dir(currentDir), "..")
	// command line flag for demo manifests directory
	flag.StringVar(&demoManifestsDir, "demo-manifests-dir", filepath.Join(rootDir, "demo"), "Directory containing demo YAML manifests")
}

const (
	checkPodLogsTimeout  = "30s"
	checkPodLogsInterval = "1s"
)

var (
	memDeviceRegexp = regexp.MustCompile(`(?m)^declare -x MEM_DEVICE_NUMA="(.+)"$`)
	numaIDRegexp    = regexp.MustCompile(`^numa-([0-9]+)$`)
)

func TestE2e(t *testing.T) {
	flag.Parse()
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func(ctx SpecContext) {
	// Create a Kubernetes clientset
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	config, err := kubeConfig.ClientConfig()
	Expect(err).NotTo(HaveOccurred())

	clientset, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())

	dynamicClient, err = dynamic.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred())

	// Create a RESTMapper to properly map GVK to GVR
	groupResources, err := restmapper.GetAPIGroupResources(clientset.Discovery())
	Expect(err).NotTo(HaveOccurred())
	restMapper = restmapper.NewDiscoveryRESTMapper(groupResources)
})

// deployManifest creates resources from a manifest file and registers cleanup
// and failure diagnostics via DeferCleanup.
func deployManifest(ctx context.Context, namespace string, manifestFile string) {
	GinkgoHelper()
	absPath := filepath.Join(demoManifestsDir, manifestFile)
	createManifest(ctx, dynamicClient, absPath)
	// DeferCleanup is LIFO: register cleanup first, then diagnostics second.
	// On teardown, diagnostics run first (while pods exist), then cleanup deletes them.
	DeferCleanup(func(ctx context.Context) {
		deleteManifest(ctx, dynamicClient, absPath)
	}, NodeTimeout(30*time.Second))
	DeferCleanup(dumpDiagnosticsOnFailure, namespace, NodeTimeout(15*time.Second))
}

// dumpDiagnosticsOnFailure collects pod status, events, and driver logs
// when a test has failed. Intended for use as a DeferCleanup callback.
func dumpDiagnosticsOnFailure(ctx context.Context, namespace string) {
	if !CurrentSpecReport().Failed() {
		return
	}

	fmt.Fprintf(GinkgoWriter, "\n=== Failure diagnostics for namespace %s ===\n", namespace)

	// Pod status
	podList, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Failed to list pods: %v\n", err)
	} else {
		for _, pod := range podList.Items {
			fmt.Fprintf(GinkgoWriter, "Pod %s: phase=%s conditions=%v\n",
				pod.Name, pod.Status.Phase, pod.Status.Conditions)
		}
	}

	// Events
	events, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Failed to list events: %v\n", err)
	} else {
		for _, e := range events.Items {
			fmt.Fprintf(GinkgoWriter, "Event %s/%s: %s %s\n",
				e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, e.Message)
		}
	}

	// Driver logs
	tailLines := int64(20)
	driverPods, err := clientset.CoreV1().Pods(driverNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: driverPodSelector,
	})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Failed to list driver pods: %v\n", err)
		return
	}
	for _, pod := range driverPods.Items {
		for _, c := range pod.Spec.Containers {
			stream, err := clientset.CoreV1().Pods(driverNamespace).GetLogs(pod.Name, &v1.PodLogOptions{
				Container: c.Name,
				TailLines: &tailLines,
			}).Stream(ctx)
			if err != nil {
				fmt.Fprintf(GinkgoWriter, "Driver pod %s, container %s: failed to get logs: %v\n", pod.Name, c.Name, err)
				continue
			}
			buf := new(bytes.Buffer)
			io.Copy(buf, stream)
			stream.Close()
			fmt.Fprintf(GinkgoWriter, "Driver pod %s, container %s (last %d lines):\n%s\n", pod.Name, c.Name, tailLines, buf.String())
		}
	}
}

// parseManifests reads a YAML file and returns a slice of unstructured objects
func parseManifests(manifestPath string) ([]*unstructured.Unstructured, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file %s: %w", manifestPath, err)
	}

	var objects []*unstructured.Unstructured
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)

	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to decode object from %s: %w", manifestPath, err)
		}
		if len(obj.Object) == 0 {
			continue
		}

		// Set default namespace for namespaced resources if not specified
		gvk := obj.GroupVersionKind()
		namespace := obj.GetNamespace()
		if namespace == "" && (gvk.Kind == "ResourceClaim" || gvk.Kind == "ResourceClaimTemplate") {
			obj.SetNamespace("default")
		}

		objects = append(objects, &obj)
	}

	return objects, nil
}

// getGVRForObject returns the GroupVersionResource for an unstructured object
func getGVRForObject(obj *unstructured.Unstructured) (schema.GroupVersionResource, error) {
	gvk := obj.GroupVersionKind()

	// Use RESTMapper to get the correct resource name
	mapping, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("failed to get REST mapping for %v: %w", gvk, err)
	}

	return mapping.Resource, nil
}

// createObjects creates a list of unstructured objects using the dynamic client
func createObjects(ctx context.Context, dynamicClient dynamic.Interface, objects []*unstructured.Unstructured, dryRun bool) error {
	GinkgoHelper()
	for _, obj := range objects {
		gvr, err := getGVRForObject(obj)
		if err != nil {
			return fmt.Errorf("failed to get GVR for object %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
		}

		namespace := obj.GetNamespace()

		createOptions := metav1.CreateOptions{}
		if dryRun {
			createOptions.DryRun = []string{metav1.DryRunAll}
		}

		if namespace != "" {
			_, err = dynamicClient.Resource(gvr).Namespace(namespace).Create(ctx, obj, createOptions)
		} else {
			_, err = dynamicClient.Resource(gvr).Create(ctx, obj, createOptions)
		}

		if err != nil {
			if dryRun {
				return err
			}
			Expect(err).NotTo(HaveOccurred())
		}
	}
	return nil
}

// deleteObjects deletes a list of unstructured objects using the dynamic client
// and waits for them to be fully removed.
func deleteObjects(ctx context.Context, dynamicClient dynamic.Interface, objects []*unstructured.Unstructured) {
	GinkgoHelper()
	deletePolicy := metav1.DeletePropagationForeground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}

	for _, obj := range objects {
		gvr, err := getGVRForObject(obj)
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "Warning: Failed to get GVR for object %s/%s: %v\n",
				obj.GetNamespace(), obj.GetName(), err)
			continue
		}

		namespace := obj.GetNamespace()
		gvk := obj.GroupVersionKind()

		if namespace != "" {
			err = dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx, obj.GetName(), deleteOptions)
		} else {
			err = dynamicClient.Resource(gvr).Delete(ctx, obj.GetName(), deleteOptions)
		}

		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "Warning: Failed to delete %s/%s in namespace %s: %v\n",
				gvk.Kind, obj.GetName(), namespace, err)
		}
	}

	// Wait for all objects to be fully removed
	for _, obj := range objects {
		gvr, err := getGVRForObject(obj)
		if err != nil {
			continue
		}
		namespace := obj.GetNamespace()
		name := obj.GetName()

		Eventually(func() bool {
			var err error
			if namespace != "" {
				_, err = dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			} else {
				_, err = dynamicClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
			}
			return apierrors.IsNotFound(err)
		}).WithContext(ctx).WithTimeout(30*time.Second).WithPolling(1*time.Second).Should(BeTrue(),
			"Timed out waiting for %s/%s to be deleted", obj.GroupVersionKind().Kind, name)
	}
}

// createManifest creates resources from a manifest file
func createManifest(ctx context.Context, dynamicClient dynamic.Interface, manifestPath string) {
	GinkgoHelper()
	objects, err := parseManifests(manifestPath)
	Expect(err).NotTo(HaveOccurred())

	err = createObjects(ctx, dynamicClient, objects, false)
	Expect(err).NotTo(HaveOccurred())
}

// deleteManifest deletes resources from a manifest file
func deleteManifest(ctx context.Context, dynamicClient dynamic.Interface, manifestPath string) {
	GinkgoHelper()
	objects, err := parseManifests(manifestPath)
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Warning: %v\n", err)
		return
	}

	deleteObjects(ctx, dynamicClient, objects)
}

// createManifestWithDryRun creates objects from a manifest with dry-run mode
func checkPodsReadyAndRunning(ctx context.Context, namespace string, pods []string) {
	GinkgoHelper()
	// check if the pods are Ready and Running
	for _, podName := range pods {
		Eventually(func(g Gomega) {
			pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred(),
				"Failed to get pod %s/%s", namespace, podName)
			g.Expect(pod.Status.Phase).To(Equal(v1.PodRunning),
				"Pod %s/%s has phase %s, expected Running (conditions: %v)",
				namespace, podName, pod.Status.Phase, pod.Status.Conditions)
			ready := false
			for _, cond := range pod.Status.Conditions {
				if cond.Type == v1.PodReady && cond.Status == v1.ConditionTrue {
					ready = true
					break
				}
			}
			g.Expect(ready).To(BeTrue(),
				"Pod %s/%s is Running but not Ready (conditions: %v)",
				namespace, podName, pod.Status.Conditions)
		}, "120s", "5s").Should(Succeed())
	}
}

// getMemoryDevicesFromPodLogs retrieves pod logs and extracts memory device information.
// Returns errors via g so callers inside Eventually can retry on transient failures.
func getMemoryDevicesFromPodLogs(ctx context.Context, g Gomega, namespace, pod, container string) ([]string, string) {
	GinkgoHelper()
	req := clientset.CoreV1().Pods(namespace).GetLogs(pod, &v1.PodLogOptions{
		Container: container,
	})
	podLogs, err := req.Stream(ctx)
	g.Expect(err).NotTo(HaveOccurred(),
		"Failed to stream logs for pod %s/%s, container %s", namespace, pod, container)
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	g.Expect(err).NotTo(HaveOccurred(),
		"Failed to read logs for pod %s/%s, container %s", namespace, pod, container)
	logs := buf.String()

	matches := memDeviceRegexp.FindAllStringSubmatch(logs, -1)

	var memDevices []string
	for _, m := range matches {
		if len(m) > 1 {
			memDevices = append(memDevices, m[1])
		}
	}
	return memDevices, logs
}

func extractMemoryProperty(logs string, id string, property string) string {
	var pattern string
	if property == "DRA_ADMIN_ACCESS" {
		pattern = fmt.Sprintf(`(?m)^declare -x %s="(.+)"$`, property)
	} else {
		pattern = fmt.Sprintf(`(?m)^declare -x MEM_DEVICE_NUMA_%s_%s="(.+)"$`, id, property)
	}
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(logs, -1)

	if len(matches) > 0 && len(matches[0]) > 1 {
		return matches[0][1]
	}
	return ""
}

func getNumaID(numaDevice string) string {
	matches := numaIDRegexp.FindAllStringSubmatch(numaDevice, -1)
	if len(matches) > 0 && len(matches[0]) > 1 {
		return matches[0][1]
	}
	return ""
}

// verifyMemoryAllocationWithCapacity checks that a pod/container has the expected number of memory devices
// and verifies that the allocated capacity does not exceed the requested capacity
func verifyMemoryAllocationWithCapacity(ctx context.Context, namespace, podName, containerName string, expectedDeviceCount int, expectedCapacity string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		// Get pod logs and extract memory devices
		devices, logs := getMemoryDevicesFromPodLogs(ctx, g, namespace, podName, containerName)
		verifyDeviceCount(g, devices, expectedDeviceCount, namespace, podName, containerName)

		// Verify capacity for each device
		for _, device := range devices {
			numaID := getNumaID(device)
			allocatedCapacity := extractMemoryProperty(logs, numaID, "THROUGHPUT")

			if allocatedCapacity != "" {
				verifyCapacityDoesNotExceed(g, allocatedCapacity, expectedCapacity, namespace, podName, containerName, device)
			} else {
				fmt.Fprintf(GinkgoWriter, "Pod %s/%s, container %s, device %s: No capacity information found in logs\n",
					namespace, podName, containerName, device)
			}
		}
	}, checkPodLogsTimeout, checkPodLogsInterval).Should(Succeed())
}

// verifyCapacityDoesNotExceed checks that the allocated capacity does not exceed the expected capacity
func verifyCapacityDoesNotExceed(g Gomega, allocatedCapacity, expectedCapacity, namespace, podName, containerName, device string) {
	GinkgoHelper()

	// Parse the allocated and expected capacity as resource.Quantity
	allocated, err := resource.ParseQuantity(allocatedCapacity)
	g.Expect(err).NotTo(HaveOccurred(),
		fmt.Sprintf("Failed to parse allocated capacity %s for pod %s/%s, container %s, device %s",
			allocatedCapacity, namespace, podName, containerName, device))

	expected, err := resource.ParseQuantity(expectedCapacity)
	g.Expect(err).NotTo(HaveOccurred(),
		fmt.Sprintf("Failed to parse expected capacity %s for pod %s/%s, container %s, device %s",
			expectedCapacity, namespace, podName, containerName, device))

	// Compare: allocated should be <= expected
	comparison := allocated.Cmp(expected)
	g.Expect(comparison).To(BeNumerically("<=", 0),
		fmt.Sprintf("Pod %s/%s, container %s, device %s: allocated capacity %s exceeds expected capacity %s",
			namespace, podName, containerName, device, allocatedCapacity, expectedCapacity))

	fmt.Fprintf(GinkgoWriter, "Pod %s/%s, container %s, device %s: allocated capacity %s <= expected capacity %s ✓\n",
		namespace, podName, containerName, device, allocatedCapacity, expectedCapacity)
}

// verifyDeviceCount verifies that a container has the expected number of memory devices
func verifyDeviceCount(g Gomega, devices []string, expectedDeviceCount int, namespace, podName, containerName string) {
	GinkgoHelper()
	g.Expect(devices).To(HaveLen(expectedDeviceCount),
		fmt.Sprintf("Expected Pod %s/%s, container %s to have %d memory devices, but got %d: %v",
			namespace, podName, containerName, expectedDeviceCount, len(devices), devices))
}

// checkOneOfTwoPodsRunning verifies that exactly one of the two pods is running and one is pending
func checkOneOfTwoPodsRunning(ctx context.Context, namespace string, pods []string) {
	GinkgoHelper()
	Expect(pods).To(HaveLen(2), "This function expects exactly 2 pods")

	Eventually(func(g Gomega) {
		pod0, err := clientset.CoreV1().Pods(namespace).Get(ctx, pods[0], metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod %s/%s", namespace, pods[0])

		pod1, err := clientset.CoreV1().Pods(namespace).Get(ctx, pods[1], metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod %s/%s", namespace, pods[1])

		// Check that one pod is Running and one is Pending
		runningCount := 0
		pendingCount := 0

		if pod0.Status.Phase == v1.PodRunning {
			runningCount++
		} else if pod0.Status.Phase == v1.PodPending {
			pendingCount++
		}

		if pod1.Status.Phase == v1.PodRunning {
			runningCount++
		} else if pod1.Status.Phase == v1.PodPending {
			pendingCount++
		}

		g.Expect(runningCount).To(Equal(1),
			"Expected exactly 1 pod to be Running, got %d (pod0: %s, pod1: %s)",
			runningCount, pod0.Status.Phase, pod1.Status.Phase)
		g.Expect(pendingCount).To(Equal(1),
			"Expected exactly 1 pod to be Pending, got %d (pod0: %s, pod1: %s)",
			pendingCount, pod0.Status.Phase, pod1.Status.Phase)
	}, "120s", "5s").Should(Succeed())
}

// findRunningPod returns the name of the pod that is running from the given list
func findRunningPod(ctx context.Context, namespace string, pods []string) string {
	GinkgoHelper()
	for _, podName := range pods {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred(), "Failed to get pod %s/%s", namespace, podName)

		if pod.Status.Phase == v1.PodRunning {
			// Verify the pod is also Ready
			ready := false
			for _, cond := range pod.Status.Conditions {
				if cond.Type == v1.PodReady && cond.Status == v1.ConditionTrue {
					ready = true
					break
				}
			}
			if ready {
				fmt.Fprintf(GinkgoWriter, "Found running pod: %s/%s\n", namespace, podName)
				return podName
			}
		}
	}
	Fail(fmt.Sprintf("No running pod found in namespace %s among pods %v", namespace, pods))
	return ""
}

// checkPodPendingDueToInsufficientResources verifies that a pod is pending due to insufficient resources
func checkPodPendingDueToInsufficientResources(ctx context.Context, namespace, podName string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod %s/%s", namespace, podName)

		g.Expect(pod.Status.Phase).To(Equal(v1.PodPending),
			"Expected pod %s/%s to be Pending, but got %s", namespace, podName, pod.Status.Phase)

		// Check pod conditions or events to verify it's pending due to resource constraints
		// Look for PodScheduled condition = False
		scheduled := true
		for _, cond := range pod.Status.Conditions {
			if cond.Type == v1.PodScheduled && cond.Status == v1.ConditionFalse {
				scheduled = false
				fmt.Fprintf(GinkgoWriter, "Pod %s/%s is unscheduled: %s - %s\n",
					namespace, podName, cond.Reason, cond.Message)
				break
			}
		}
		g.Expect(scheduled).To(BeFalse(),
			"Expected pod %s/%s to be unscheduled, but PodScheduled condition is True or missing",
			namespace, podName)
	}, "120s", "5s").Should(Succeed())

	fmt.Fprintf(GinkgoWriter, "Verified pod %s/%s is pending due to insufficient resources\n",
		namespace, podName)
}

// verifyAllocatedCapacityWithinLimit verifies that the allocated capacity is within the maximum available limit
func verifyAllocatedCapacityWithinLimit(ctx context.Context, namespace, podName, containerName, maxLimit string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		// Get pod logs and extract memory devices
		devices, logs := getMemoryDevicesFromPodLogs(ctx, g, namespace, podName, containerName)
		g.Expect(devices).NotTo(BeEmpty(),
			"Expected pod %s/%s, container %s to have at least one device", namespace, podName, containerName)

		// Verify allocated capacity for each device is within limit
		for _, device := range devices {
			numaID := getNumaID(device)
			allocatedCapacity := extractMemoryProperty(logs, numaID, "THROUGHPUT")

			if allocatedCapacity != "" {
				allocated, err := resource.ParseQuantity(allocatedCapacity)
				g.Expect(err).NotTo(HaveOccurred(),
					fmt.Sprintf("Failed to parse allocated capacity %s for pod %s/%s, container %s, device %s",
						allocatedCapacity, namespace, podName, containerName, device))

				maxLimitQty, err := resource.ParseQuantity(maxLimit)
				g.Expect(err).NotTo(HaveOccurred(),
					fmt.Sprintf("Failed to parse max limit %s", maxLimit))

				comparison := allocated.Cmp(maxLimitQty)
				g.Expect(comparison).To(BeNumerically("<=", 0),
					fmt.Sprintf("Pod %s/%s, container %s, device %s: allocated capacity %s exceeds maximum available capacity %s",
						namespace, podName, containerName, device, allocatedCapacity, maxLimit))

				fmt.Fprintf(GinkgoWriter, "Pod %s/%s, container %s, device %s: allocated capacity %s <= max limit %s ✓\n",
					namespace, podName, containerName, device, allocatedCapacity, maxLimit)
			}
		}
	}, checkPodLogsTimeout, checkPodLogsInterval).Should(Succeed())
}

// checkAllPodsPending verifies that all pods in the list are in Pending state
func checkAllPodsPending(ctx context.Context, namespace string, pods []string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		for _, podName := range pods {
			pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred(), "Failed to get pod %s/%s", namespace, podName)

			g.Expect(pod.Status.Phase).To(Equal(v1.PodPending),
				"Expected pod %s/%s to be Pending, but got %s", namespace, podName, pod.Status.Phase)

			fmt.Fprintf(GinkgoWriter, "Pod %s/%s is in Pending state ✓\n", namespace, podName)
		}
	}, "120s", "5s").Should(Succeed())

	fmt.Fprintf(GinkgoWriter, "Verified all %d pods are in Pending state\n", len(pods))
}

// verifyRequestExceedsSystemCapacity verifies that the requested capacity exceeds the system capacity
func verifyRequestExceedsSystemCapacity(ctx context.Context, namespace, podName, requestedCapacity, maxSystemCapacity string) {
	GinkgoHelper()

	requested, err := resource.ParseQuantity(requestedCapacity)
	Expect(err).NotTo(HaveOccurred(),
		"Failed to parse requested capacity %s for pod %s/%s", requestedCapacity, namespace, podName)

	maxSystem, err := resource.ParseQuantity(maxSystemCapacity)
	Expect(err).NotTo(HaveOccurred(),
		"Failed to parse max system capacity %s", maxSystemCapacity)

	// Verify requested > maxSystem
	comparison := requested.Cmp(maxSystem)
	Expect(comparison).To(BeNumerically(">", 0),
		"Expected requested capacity %s to exceed system capacity %s, but it doesn't",
		requestedCapacity, maxSystemCapacity)

	fmt.Fprintf(GinkgoWriter, "Pod %s/%s: requested capacity %s > system capacity %s ✓\n",
		namespace, podName, requestedCapacity, maxSystemCapacity)
}

// getAssignedNumaNode extracts the NUMA node assigned to a pod from its logs
func getAssignedNumaNode(ctx context.Context, namespace, podName, containerName string) string {
	GinkgoHelper()
	var numaNode string

	Eventually(func(g Gomega) {
		devices, _ := getMemoryDevicesFromPodLogs(ctx, g, namespace, podName, containerName)
		g.Expect(devices).NotTo(BeEmpty(),
			"Expected pod %s/%s, container %s to have at least one device", namespace, podName, containerName)

		// devices[0] contains just the NUMA ID (e.g., "0", "1", "2", "3")
		// We need to construct the full NUMA node name (e.g., "numa-0", "numa-1", etc.)
		numaID := devices[0]
		g.Expect(numaID).To(MatchRegexp(`^\d+$`),
			"Expected device ID to be a number, got %s", numaID)

		numaNode = fmt.Sprintf("numa-%s", numaID)
		fmt.Fprintf(GinkgoWriter, "Pod %s/%s assigned to NUMA node: %s\n", namespace, podName, numaNode)
	}, checkPodLogsTimeout, checkPodLogsInterval).Should(Succeed())

	return numaNode
}

// verifyNumaCapacityNotExceeded verifies that no NUMA node has exceeded its capacity
func verifyNumaCapacityNotExceeded(ctx context.Context, namespace string, numaAllocations map[string][]string, requestedCapacity string, numaCapacities map[string]string) {
	GinkgoHelper()

	requested, err := resource.ParseQuantity(requestedCapacity)
	Expect(err).NotTo(HaveOccurred(),
		"Failed to parse requested capacity %s", requestedCapacity)

	fmt.Fprintf(GinkgoWriter, "\n=== NUMA Node Capacity Verification ===\n")

	for numaNode, pods := range numaAllocations {
		if len(pods) == 0 {
			continue
		}

		// Get the capacity for this NUMA node
		numaCapacityStr, exists := numaCapacities[numaNode]
		Expect(exists).To(BeTrue(),
			"NUMA node %s not found in capacity map", numaNode)

		numaCapacity, err := resource.ParseQuantity(numaCapacityStr)
		Expect(err).NotTo(HaveOccurred(),
			"Failed to parse NUMA capacity %s for node %s", numaCapacityStr, numaNode)

		// Calculate total allocated capacity on this NUMA node
		totalAllocated := requested.DeepCopy()
		totalAllocated.Set(requested.Value() * int64(len(pods)))

		// Verify allocated <= capacity
		comparison := totalAllocated.Cmp(numaCapacity)
		Expect(comparison).To(BeNumerically("<=", 0),
			"NUMA node %s: allocated capacity %s (from %d pods × %s) exceeds node capacity %s",
			numaNode, totalAllocated.String(), len(pods), requestedCapacity, numaCapacityStr)

		fmt.Fprintf(GinkgoWriter, "NUMA node %s: %d pods × %s = %s <= %s (capacity) ✓\n",
			numaNode, len(pods), requestedCapacity, totalAllocated.String(), numaCapacityStr)
		fmt.Fprintf(GinkgoWriter, "  Pods on %s: %v\n", numaNode, pods)
	}

	fmt.Fprintf(GinkgoWriter, "=== All NUMA nodes within capacity limits ===\n\n")
}
