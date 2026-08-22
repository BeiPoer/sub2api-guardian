//go:build !linux

package fileperm

import (
	"io/fs"
	"os"
)

func Chmod(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode)
}
