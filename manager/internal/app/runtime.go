package app

import "runtime"

func runtimeIsLinux() bool { return runtime.GOOS == "linux" }
func runtimeArch() string  { return runtime.GOARCH }
