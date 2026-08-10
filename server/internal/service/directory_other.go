//go:build !windows

package service

func getWindowsDiskDrives() []string {
	return nil
}
