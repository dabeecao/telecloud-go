//go:build !windows

package utils

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func GetDiskSpace(path string) (total, free uint64, err error) {
	// Try a sequence of paths to be as resilient as possible, especially for Termux/Android
	var stat unix.Statfs_t
	
	paths := []string{path, "."}
	if wd, wdErr := os.Getwd(); wdErr == nil {
		paths = append(paths, wd)
	}
	
	// Termux-specific fallback
	if runtime.GOOS == "android" || os.Getenv("TERMUX_VERSION") != "" {
		if home := os.Getenv("HOME"); home != "" {
			paths = append(paths, home)
		}
	}
	
	paths = append(paths, "/")

	for _, p := range paths {
		if p == "" {
			continue
		}
		err = unix.Statfs(p, &stat)
		if err == nil {
			break
		}
	}

	if err != nil {
		return 0, 0, err
	}

	// Calculate sizes using uint64 to prevent overflow
	// stat.Blocks and stat.Bsize types vary by platform (int64/uint64/uint32)
	// We use Bavail (available to non-root) instead of Bfree (total free) for accuracy.
	total = uint64(stat.Blocks) * uint64(stat.Bsize)
	free = uint64(stat.Bavail) * uint64(stat.Bsize)
	return total, free, nil
}
