//go:build !linux

package filesearch

func mountsToSkip() map[string]bool { return nil }
