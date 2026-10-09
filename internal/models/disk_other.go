//go:build !unix

package models

import "errors"

// DiskUsage is only needed on the llama host (Linux).
func DiskUsage(path string) (Disk, error) { return Disk{Path: path}, errors.ErrUnsupported }
