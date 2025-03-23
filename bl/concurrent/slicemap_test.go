package concurrent_test

import (
	"testing"
	"time"

	"github.com/ejoffe/spr/bl/concurrent"
	"github.com/stretchr/testify/require"
)

func TestSliceMap(t *testing.T) {
	in := []int{3, 2, 1}
	out, err := concurrent.SliceMap(in, func(i int) (int, error) {
		// Sleep to make functions (more likely) to finish in non-deterministic order
		time.Sleep(time.Duration(i))
		return i + 1, nil
	})

	require.NoError(t, err)

	require.Equal(t, []int{4, 3, 2}, out)
}

func TestSliceMapWithIndex(t *testing.T) {
	in := []int{30, 20, 10}
	out, err := concurrent.SliceMapWithIndex(in, func(index, i int) (int, error) {
		// Sleep to make functions (more likely) to finish in non-deterministic order
		time.Sleep(time.Duration(i + index))
		return i + index, nil
	})

	require.NoError(t, err)

	require.Equal(t, []int{30, 21, 12}, out)
}
