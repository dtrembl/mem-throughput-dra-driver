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
	resourceapi "k8s.io/api/resource/v1"
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

	// Check if the webhook is ready
	// Even after verifying that the Pod is Ready and the expected Endpoints resource
	// exists with the Pod's IP, the webhook still seems to have "connection refused"
	// issues, so retry here until we can ensure it's available before the real tests start.
	By("Ensuring the webhook is ready")
	verifyWebhook(ctx)
})
//
func verifyWebhook(ctx context.Context) {
	GinkgoHelper()
	fmt.Fprintln(GinkgoWriter, "Waiting for webhook to be available")

	// Create a simple ResourceClaim to test webhook availability
	testClaim := &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "webhook-test",
			Namespace: "default",
		},
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name: "memory",
						Exactly: &resourceapi.ExactDeviceRequest{
							DeviceClassName: "mem.example.com",
						},
					},
				},
			},
		},
	}

	// Wait for webhook to be available by trying to create the ResourceClaim with dry-run mode
	Eventually(func() error {
		_, err := clientset.ResourceV1().ResourceClaims("default").Create(
			ctx,
			testClaim,
			metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}},
		)
		if err != nil {
			return fmt.Errorf("webhook not ready: %w", err)
		}
		return nil
	}, "30s", "1s").WithContext(ctx).Should(Succeed())
}
//
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
//
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
//
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
//
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
func createManifestWithDryRun(ctx context.Context, dynamicClient dynamic.Interface, manifestPath string) error {
	GinkgoHelper()
	objects, err := parseManifests(manifestPath)
	if err != nil {
		return err
	}
	return createObjects(ctx, dynamicClient, objects, true)
}

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
//
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

// verifyMemoryAllocation checks that a pod/container has the expected number of memory devices
// and tracks them in observedDevices to ensure no device is claimed twice within a test
func verifyMemoryAllocation(ctx context.Context, namespace, podName, containerName string, expectedDeviceCount int, observedDevices map[string]string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		// Get pod logs and extract memory devices
		devices, _ := getMemoryDevicesFromPodLogs(ctx, g, namespace, podName, containerName)
		verifyDeviceCount(g, devices, expectedDeviceCount, namespace, podName, containerName)

		// Verify each device is unclaimed
		for _, device := range devices {
			claimNewDevice(g, observedDevices, device, namespace, podName, containerName)
		}
	}, checkPodLogsTimeout, checkPodLogsInterval).Should(Succeed())
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

// verifyDRAAdminAccess verifies that DRA_ADMIN_ACCESS is set to the expected value
func verifyDRAAdminAccess(ctx context.Context, namespace, podName, containerName, expectedValue string) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		_, logs := getMemoryDevicesFromPodLogs(ctx, g, namespace, podName, containerName)
		draAdminAccess := extractMemoryProperty(logs, "", "DRA_ADMIN_ACCESS")
		g.Expect(draAdminAccess).To(Equal(expectedValue),
			fmt.Sprintf("Expected Pod %s/%s, container %s to have DRA_ADMIN_ACCESS=%s, but got %s",
				namespace, podName, containerName, expectedValue, draAdminAccess))
		fmt.Fprintf(GinkgoWriter, "Pod %s/%s, container %s has DRA_ADMIN_ACCESS=%s\n",
			namespace, podName, containerName, draAdminAccess)
	}, checkPodLogsTimeout, checkPodLogsInterval).Should(Succeed())
}

// claimNewDevice verifies that a device is unclaimed and adds it to observedDevices
func claimNewDevice(g Gomega, observedDevices map[string]string, device, namespace, podName, containerName string) {
	GinkgoHelper()
	claimedBy, alreadySeen := observedDevices[device]
	g.Expect(alreadySeen).To(BeFalse(),
		fmt.Sprintf("Pod %s/%s, container %s should have a new memory device but claimed %s which is already claimed by %s",
			namespace, podName, containerName, device, claimedBy))
	observedDevices[device] = namespace + "/" + podName
	fmt.Fprintf(GinkgoWriter, "Pod %s/%s, container %s claimed %s\n",
		namespace, podName, containerName, device)
}

// verifyDeviceCount verifies that a container has the expected number of memory devices
func verifyDeviceCount(g Gomega, devices []string, expectedDeviceCount int, namespace, podName, containerName string) {
	GinkgoHelper()
	g.Expect(devices).To(HaveLen(expectedDeviceCount),
		fmt.Sprintf("Expected Pod %s/%s, container %s to have %d memory devices, but got %d: %v",
			namespace, podName, containerName, expectedDeviceCount, len(devices), devices))
}

// verifyMemoryProperties verifies memory sharing strategy and an optional additional property
func verifyMemoryProperties(g Gomega, logs, namespace, podName, containerName string, devices []string, expectedSharingStrategy, expectedProperty, expectedPropertyValue string) {
	GinkgoHelper()
	sharingStrategy := extractMemoryProperty(logs, getNumaID(devices[0]), "SHARING_STRATEGY")
	g.Expect(sharingStrategy).To(Equal(expectedSharingStrategy),
		fmt.Sprintf("Expected Pod %s/%s, container %s to have sharing strategy %s, got %s",
			namespace, podName, containerName, expectedSharingStrategy, sharingStrategy))

	if expectedProperty != "" {
		propertyValue := extractMemoryProperty(logs, getNumaID(devices[0]), expectedProperty)
		g.Expect(propertyValue).To(Equal(expectedPropertyValue),
			fmt.Sprintf("Expected Pod %s/%s, container %s to have %s=%s, got %s",
				namespace, podName, containerName, expectedProperty, expectedPropertyValue, propertyValue))
	}
}

// podContainer identifies a specific container in a specific pod.
type podContainer struct {
	pod       string
	container string
}

// sharingGroup describes a set of pod/containers that should all share the same memory device,
// along with the expected sharing properties.
type sharingGroup struct {
	// members lists the pod/container pairs that should all see the same memory device.
	members []podContainer
	// expectedStrategy is the expected memory sharing strategy (e.g. "TimeSlicing", "SpacePartitioning").
	expectedStrategy string
	// expectedProperty is the name of the memory property to verify (e.g. "TIMESLICE_INTERVAL", "PARTITION_COUNT").
	expectedProperty string
	// expectedPropValue is the expected value of expectedProperty (e.g. "Default", "Long", "10").
	expectedPropValue string
}

// verifySharedMemoryGroup verifies that all members of a sharing group see the same memory device
// and that the device has the expected sharing properties.
func verifySharedMemoryGroup(ctx context.Context, namespace string, group sharingGroup) {
	GinkgoHelper()
	var firstDevice string
	for i, member := range group.members {
		Eventually(func(g Gomega) {
			devices, logs := getMemoryDevicesFromPodLogs(ctx, g, namespace, member.pod, member.container)
			verifyDeviceCount(g, devices, 1, namespace, member.pod, member.container)
			if i == 0 {
				firstDevice = devices[0]
				fmt.Fprintf(GinkgoWriter, "Pod %s/%s, container %s has memory device %s (first in group)\n",
					namespace, member.pod, member.container, firstDevice)
			} else {
				g.Expect(devices[0]).To(Equal(firstDevice),
					fmt.Sprintf("Pod %s/%s, container %s should claim the same memory device as previous, but got %s instead of %s",
						namespace, member.pod, member.container, devices[0], firstDevice))
				fmt.Fprintf(GinkgoWriter, "Pod %s/%s, container %s shares memory device %s\n",
					namespace, member.pod, member.container, devices[0])
			}
			verifyMemoryProperties(g, logs, namespace, member.pod, member.container, devices,
				group.expectedStrategy, group.expectedProperty, group.expectedPropValue)
		}, checkPodLogsTimeout, checkPodLogsInterval).Should(Succeed())
	}
}
//
