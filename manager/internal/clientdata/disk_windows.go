//go:build windows

package clientdata

// Windows developer builds skip the space check (drives differ; CI targets
// Linux where this matters for real).
func checkDiskSpace(dir string, need int64) error { return nil }
