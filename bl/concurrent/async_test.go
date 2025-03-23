package concurrent_test

import (
	"testing"

	"github.com/ejoffe/spr/bl/concurrent"
	"github.com/stretchr/testify/require"
)

func TestAsync1Ret1(t *testing.T) {
	await := concurrent.Async1Ret1(
		func(a int) error {
			return nil
		},
		1,
	)

	err := await.Await()

	require.NoError(t, err)
}

func TestAsync4Ret3(t *testing.T) {
	await := concurrent.Async4Ret3(
		func(a, b, c, d int) (int, int, error) {
			return a + b + c + d, a * b * c * d, nil
		},
		1, 2, 3, 4,
	)

	add, mult, err := await.Await()

	require.NoError(t, err)
	require.Equal(t, 10, add)
	require.Equal(t, 24, mult)
}

func TestAsync5Ret3(t *testing.T) {
	await := concurrent.Async5Ret3(
		func(a, b, c, d, e int) (int, int, error) {
			return a + b + c + d + e, a * b * c * d * e, nil
		},
		1, 2, 3, 4, 5,
	)

	add, mult, err := await.Await()

	require.NoError(t, err)
	require.Equal(t, 15, add)
	require.Equal(t, 120, mult)
}
