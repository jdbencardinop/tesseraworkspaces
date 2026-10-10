//go:build !darwin && !linux

package internal

import "errors"

func managedRenameNoReplaceFD(oldDirFD int, oldName string, newDirFD int, newName string) error {
	return errors.New("managed template exclusive rename is unsupported on this platform")
}
