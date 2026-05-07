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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToScheme(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, AddToScheme(scheme))

	obj, err := scheme.New(SchemeGroupVersion.WithKind(MemoryConfigKind))
	require.NoError(t, err)
	assert.IsType(t, &MemoryConfig{}, obj)
}

func TestMemoryConfigDeepCopy(t *testing.T) {
	config := &MemoryConfig{}
	config.MemoryThroughput = resource.MustParse("250Gi")

	copy := config.DeepCopy()
	require.NotNil(t, copy)
	assert.Zero(t, copy.MemoryThroughput.Cmp(resource.MustParse("250Gi")))
}
