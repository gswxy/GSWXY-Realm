//go:build !windows

package clientdata

import (
	"fmt"
	"syscall"
)

func checkDiskSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < need {
		return fmt.Errorf("磁盘空间不足: 需要 %d MB，剩余 %d MB", need>>20, free>>20)
	}
	return nil
}
