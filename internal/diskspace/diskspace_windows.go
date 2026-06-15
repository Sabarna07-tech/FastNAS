//go:build windows

package diskspace

import "golang.org/x/sys/windows"

// Available returns the number of bytes available to the caller on the volume
// containing path.
func Available(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeToCaller, &total, &totalFree); err != nil {
		return 0, err
	}
	return freeToCaller, nil
}
