//go:build darwin

package internal

import "golang.org/x/sys/unix"

func managedRenameNoReplaceFD(oldDirFD int, oldName string, newDirFD int, newName string) error {
	return unix.RenameatxNp(oldDirFD, oldName, newDirFD, newName, unix.RENAME_EXCL)
}
