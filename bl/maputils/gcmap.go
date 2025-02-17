package maputils

import (
	mapset "github.com/deckarep/golang-set/v2"
)

type GCMap[K comparable, V any] struct {
	m        map[K]V
	accessed mapset.Set[K]
}

// NewGC Creates a map that can GC unaccessed items
func NewGC[K comparable, V any](m map[K]V) GCMap[K, V] {
	return GCMap[K, V]{
		m:        m,
		accessed: mapset.NewSet[K](),
	}
}

// Looks up an item from the map
func (ngc GCMap[K, V]) Lookup(k K) (V, bool) {
	val, ok := ngc.m[k]

	ngc.accessed.Add(k)

	return val, ok
}

// Returns only the items in the map that were looked up.
func (ngc GCMap[K, V]) PurgeUnaccessed() map[K]V {
	lookedUp := map[K]V{}
	for k, v := range ngc.m {
		if ngc.accessed.Contains(k) {
			lookedUp[k] = v
		}
	}

	return lookedUp
}
