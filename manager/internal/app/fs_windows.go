//go:build windows

package app

import "fmt"

type fsStat struct{ availMB int64 }

func statFS(path string) (*fsStat, error) {
	return nil, fmt.Errorf("unsupported on dev builds")
}

func portBusy(port int) bool { return false }
