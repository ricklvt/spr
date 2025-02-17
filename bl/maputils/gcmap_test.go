package maputils_test

import (
	"testing"

	"github.com/ejoffe/spr/bl/maputils"
	"github.com/stretchr/testify/require"
)

func TestNewGCLookupAndPurgeUnaccessed(t *testing.T) {
	all := map[string]int{
		"a": 0,
		"b": 1,
		"c": 2,
		"d": 3,
	}

	accessed := map[string]int{
		"a": 0,
		"c": 2,
	}

	purgeMap := maputils.NewGC(all)
	purgeMap.Lookup("a")
	purgeMap.Lookup("c")
	purgeMap.Lookup("x")

	require.Equal(t, accessed, purgeMap.PurgeUnaccessed())
}
