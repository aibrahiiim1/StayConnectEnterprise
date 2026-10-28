//go:build !unix

package localkeys

import "os"

func ownerOK(os.FileInfo) bool { return true }

func openNoFollow(path string) (*os.File, error) { return os.Open(path) }
