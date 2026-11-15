//go:build !darwin && !windows && !linux

package awake

// System is nil here: this system has no hold the app knows.
func System() Backend { return nil }
