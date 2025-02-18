package concurrent

type ret3[R0, R1 any] struct {
	v0  R0
	v1  R1
	err error
}

type await3[R0, R1 any] struct {
	ch chan ret3[R0, R1]
}

func (a await3[R0, R1]) Await() (R0, R1, error) {
	r := <-a.ch

	return r.v0, r.v1, r.err
}

func Async4Ret3[A, B, C, D any, R0, R1 any](
	fn func(A, B, C, D) (R0, R1, error),
	a A, b B, c C, d D,
) await3[R0, R1] {

	ch := make(chan ret3[R0, R1])

	go func() {
		v0, v1, err := fn(a, b, c, d)
		ch <- ret3[R0, R1]{v0: v0, v1: v1, err: err}
	}()

	return await3[R0, R1]{ch: ch}
}

func Async5Ret3[A, B, C, D, E any, R0, R1 any](
	fn func(A, B, C, D, E) (R0, R1, error),
	a A, b B, c C, d D, e E,
) await3[R0, R1] {

	ch := make(chan ret3[R0, R1])

	go func() {
		v0, v1, err := fn(a, b, c, d, e)
		ch <- ret3[R0, R1]{v0: v0, v1: v1, err: err}
	}()

	return await3[R0, R1]{ch: ch}
}
