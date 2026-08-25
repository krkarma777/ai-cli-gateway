package configstore

import "os"

func nativeReplaceStoreFile(
	directory *os.File,
	root *os.Root,
	source *os.File,
	oldName string,
	newName string,
) error {
	if directory == nil || root == nil || source == nil || oldName == "" || newName == "" {
		return ErrStore
	}
	return nativeRenameReplace(directory, root, source, oldName, newName)
}
