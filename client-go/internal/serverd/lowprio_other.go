//go:build !linux

package serverd

// withLowPriority is a no-op on platforms without setpriority semantics.
func withLowPriority[T any](fn func() (T, error), nice int) (T, error) {
	return fn()
}
