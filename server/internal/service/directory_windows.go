package service

import "syscall"

func getWindowsDiskDrives() []string {
	var drives []string

	kernel32 := syscall.MustLoadDLL("kernel32.dll")
	getLogicalDrives := kernel32.MustFindProc("GetLogicalDrives")

	ret, _, _ := getLogicalDrives.Call()
	if ret == 0 {
		return drives
	}

	for i := 0; i < 26; i++ {
		if ret&(1<<uint(i)) != 0 {
			letter := string('A' + rune(i))
			drives = append(drives, letter+":")
		}
	}

	return drives
}
