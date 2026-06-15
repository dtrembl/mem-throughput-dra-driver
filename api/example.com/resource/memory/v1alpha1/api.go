/*
 * Copyright 2026 The Kubernetes Authors.
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

package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const MemoryConfigKind = "MemoryConfig"

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MemoryConfig holds the set of parameters for configuring memory throughput.
//
// The profile currently only needs the type to be registered in the scheme so
// that opaque parameters can be decoded. No strategy-specific behavior exists.
type MemoryConfig struct {
	metav1.TypeMeta  `json:",inline"`
	MemoryThroughput resource.Quantity `json:"memoryThroughput,omitempty"`
}

// DefaultMemoryConfig provides the default memory configuration object.
func DefaultMemoryConfig() *MemoryConfig {
	return &MemoryConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupName + "/" + Version,
			Kind:       MemoryConfigKind,
		},
	}
}

// Normalize updates a MemoryConfig config with implied default values based on other settings.
func (c *MemoryConfig) Normalize() error {
	if c == nil {
		return fmt.Errorf("config is 'nil'")
	}
	if c.MemoryThroughput.IsZero() {
		c.MemoryThroughput = resource.MustParse("100Gi")
	}
	return nil
}
