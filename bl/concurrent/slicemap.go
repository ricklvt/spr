package concurrent

// SliceMap executes a function in parallel for each element in the slice. Returns the output in a slice.
// The output elements will be in the same order as the input.
func SliceMap[I any, O any](ins []I, fn func(I) (O, error)) ([]O, error) {
	type res struct {
		val   O
		err   error
		index int
	}
	ch := make(chan res)
	defer func() { close(ch) }()

	for i, in := range ins {
		go func() {
			o, err := fn(in)
			ch <- res{val: o, err: err, index: i}
		}()
	}

	var err error
	out := make([]O, len(ins))
	for _ = range ins {
		res := <-ch
		if res.err != nil {
			err = res.err
		}
		out[res.index] = res.val
	}

	return out, err
}

// SliceMapWithIndex executes a function in parallel for each element in the slice. Returns the output in a slice.
// The output elements will be in the same order as the input.
func SliceMapWithIndex[I any, O any](ins []I, fn func(int, I) (O, error)) ([]O, error) {
	type res struct {
		val   O
		err   error
		index int
	}
	ch := make(chan res)
	defer func() { close(ch) }()

	for i, in := range ins {
		go func() {
			o, err := fn(i, in)
			ch <- res{val: o, err: err, index: i}
		}()
	}

	var err error
	out := make([]O, len(ins))
	for _ = range ins {
		res := <-ch
		if res.err != nil {
			err = res.err
		}
		out[res.index] = res.val
	}

	return out, err
}
