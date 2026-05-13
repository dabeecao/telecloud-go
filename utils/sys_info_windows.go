//go:build windows

package utils

import (
	"golang.org/x/sys/windows"
)

func GetDiskSpace(path string) (total, free uint64, err error) {
	// For Windows, path can be a drive letter like "C:\" or a directory.
	// If empty or invalid, GetDiskFreeSpaceEx defaults to the current drive.
	
	if path == "" || path == "." || path == "/" {
		path = "."
	}

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}

	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	err = windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalNumberOfBytes, &totalNumberOfFreeBytes)
	if err != nil {
		// Fallback to current directory
		pathPtr, _ = windows.UTF16PtrFromString(".")
		err = windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalNumberOfBytes, &totalNumberOfFreeBytes)
		if err != nil {
			return 0, 0, err
		}
	}

	return totalNumberOfBytes, freeBytesAvailable, nil
}
