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

package memorythroughput

import (
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/utils/ptr"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"

	configapi "sigs.k8s.io/dra-memory-driver/api/example.com/resource/memory/v1alpha1"
	"sigs.k8s.io/dra-memory-driver/internal/profiles"
)

const ProfileName = "mem"

type Profile struct {
	profiles.NoopConfigHandler

	nodeName string
	numNuma  int
}

func NewProfile(nodeName string, numNuma int) Profile {
	return Profile{
		nodeName: nodeName,
		numNuma:  numNuma,
	}
}

// SchemeBuilder implements [profiles.ConfigHandler].
func (p Profile) SchemeBuilder() runtime.SchemeBuilder {
	return runtime.NewSchemeBuilder(
		configapi.AddToScheme,
	)
}

// Validate implements [profiles.ConfigHandler].
func (p Profile) Validate(config runtime.Object) error {
	return nil
}

// ApplyConfig implements [profiles.ConfigHandler].
func (p Profile) ApplyConfig(config runtime.Object, results []*resourceapi.DeviceRequestAllocationResult) (profiles.PerDeviceCDIContainerEdits, error) {
	if config == nil {
		config = configapi.DefaultMemoryConfig()
	}
	if config, ok := config.(*configapi.MemoryConfig); ok {
		return applyMemoryConfig(config, results)
	}
	return nil, fmt.Errorf("runtime object is not a recognized configuration")
}

// In this example driver there is no actual configuration applied. We simply
// define a set of environment variables to be injected into the containers
// that include a given device. A real driver would likely need to do some sort
// of hardware configuration as well, based on the config passed in.
func applyMemoryConfig(config *configapi.MemoryConfig, results []*resourceapi.DeviceRequestAllocationResult) (profiles.PerDeviceCDIContainerEdits, error) {
	perDeviceEdits := make(profiles.PerDeviceCDIContainerEdits)

	print("TESTTTTT!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!\n")
	print("Memory TP:" + config.MemoryThroughput.String() + "\n")

	// Normalize the config to set any implied defaults.
	if err := config.Normalize(); err != nil {
		return nil, fmt.Errorf("error normalizing memory config: %w", err)
	}

	// Validate the config to ensure its integrity.
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("error validating memory config: %w", err)
	}

	for _, result := range results {
		envs := []string{
			fmt.Sprintf("MEMORY_DEVICE_%s=%s", result.Device[4:], result.Device),
		}

		if !config.MemoryThroughput.IsZero() {
			envs = append(envs, fmt.Sprintf("NUMA_%s_MEMORY_THROUGHPUT=%s", result.Device[4:], config.MemoryThroughput.String()))
		}

		edits := &cdispec.ContainerEdits{
			Env: envs,
		}

		perDeviceEdits[result.Device] = &cdiapi.ContainerEdits{ContainerEdits: edits}
	}

	return perDeviceEdits, nil
}

func (p Profile) EnumerateDevices() (resourceslice.DriverResources, error) {
	devices := make([]resourceapi.Device, 0, p.numNuma)
	for numaNode := 0; numaNode < p.numNuma; numaNode++ {
		device := resourceapi.Device{
			Name:                     fmt.Sprintf("numa-%d", numaNode),
			AllowMultipleAllocations: ptr.To(true),
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				"numa": {
					IntValue: ptr.To(int64(numaNode)),
				},
			},
			Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
				"mem": {
					Value: resource.MustParse(fmt.Sprintf("%dGi", 100)),
				},
			},
		}
		devices = append(devices, device)
	}

	return resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			p.nodeName: {
				Slices: []resourceslice.Slice{{
					Devices: devices,
				}},
			},
		},
	}, nil
}
