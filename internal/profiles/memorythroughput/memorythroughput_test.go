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

package memorythroughput

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestEnumerateDevices(t *testing.T) {
	profile := NewProfile("node-a", 3)

	resources, err := profile.EnumerateDevices()
	require.NoError(t, err)
	require.Contains(t, resources.Pools, "node-a")

	pool := resources.Pools["node-a"]
	require.Len(t, pool.Slices, 1)
	require.Len(t, pool.Slices[0].Devices, 3)

	firstThroughput := pool.Slices[0].Devices[0].Capacity["mem"].Value
	assert.Equal(t, "numa-0", pool.Slices[0].Devices[0].Name)
	assert.Equal(t, int64(0), *pool.Slices[0].Devices[0].Attributes["numa"].IntValue)
	assert.Zero(t, firstThroughput.Cmp(resource.MustParse("10Gi")))

	secondThroughput := pool.Slices[0].Devices[1].Capacity["mem"].Value
	assert.Equal(t, "numa-1", pool.Slices[0].Devices[1].Name)
	assert.Equal(t, int64(1), *pool.Slices[0].Devices[1].Attributes["numa"].IntValue)
	assert.Zero(t, secondThroughput.Cmp(resource.MustParse("20Gi")))

	thirdThroughput := pool.Slices[0].Devices[2].Capacity["mem"].Value
	assert.Equal(t, "numa-2", pool.Slices[0].Devices[2].Name)
	assert.Equal(t, int64(2), *pool.Slices[0].Devices[2].Attributes["numa"].IntValue)
	assert.Zero(t, thirdThroughput.Cmp(resource.MustParse("30Gi")))
}
