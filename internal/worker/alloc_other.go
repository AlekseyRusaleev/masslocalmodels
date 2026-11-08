//go:build !windows

package worker

import "os"

func allocatedFileBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
