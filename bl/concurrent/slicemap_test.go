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
