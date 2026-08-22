//go:build linux

package fileperm

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

const plan9SuperMagic = 0x01021997

// Chmod keeps POSIX permissions strict, except on Docker Desktop's Windows
// 9p bind mounts where host ACLs apply and non-root chmod is unsupported.
func Chmod(path string, mode fs.FileMode) error {
	err := os.Chmod(path, mode)
	if err == nil {
		return nil
	}
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) == nil && ignoreUnsupportedChmod(err, stat.Type) {
		return nil
	}
	return err
}

func ignoreUnsupportedChmod(err error, filesystemType int64) bool {
	return filesystemType == plan9SuperMagic && errors.Is(err, syscall.EPERM)
}
