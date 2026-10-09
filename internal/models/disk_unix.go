//go:build unix

package models

import "syscall"

// DiskUsage returns filesystem usage for path.
func DiskUsage(path string) (Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Disk{Path: path}, err
	}
	bs := uint64(st.Bsize)
	d := Disk{Path: path, Total: st.Blocks * bs, Free: st.Bavail * bs}
	d.Used = d.Total - st.Bfree*bs
	return d, nil
}
