//go:build linux

package fileperm

import (
	"os"
	"syscall"
	"testing"
)

func TestIgnoreUnsupportedChmodOnlyForPlan9EPERM(t *testing.T) {
	permissionError := &os.PathError{Op: "chmod", Path: "/data", Err: syscall.EPERM}
	if !ignoreUnsupportedChmod(permissionError, plan9SuperMagic) {
		t.Fatal("9p EPERM should use host ACLs")
	}
	if ignoreUnsupportedChmod(permissionError, 0xef53) {
		t.Fatal("ext4 EPERM must remain fatal")
	}
	accessError := &os.PathError{Op: "chmod", Path: "/data", Err: syscall.EACCES}
	if ignoreUnsupportedChmod(accessError, plan9SuperMagic) {
		t.Fatal("only the observed unsupported chmod error may be ignored")
	}
}
