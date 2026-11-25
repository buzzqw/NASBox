//go:build !debugsize

package ui

// debugMinSize is only compiled with -tags debugsize (see debugsize.go).
func (c *clientApp) debugMinSize() {}
