package transfer

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// localFile is an Item plus where it lives on the sender's disk.
type localFile struct {
	Item
	Src string
}

// BuildManifest expands files and folders into a flat list. A folder keeps
// its own name as the top-level directory on the receiver. Only regular files
// are sent; symlinks, devices and empty directories are skipped.
func BuildManifest(paths []string) ([]localFile, error) {
	var out []localFile
	seen := map[string]bool{}
	add := func(src, rel string, info fs.FileInfo) error {
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			return fmt.Errorf("two items would both be named %q", rel)
		}
		seen[rel] = true
		out = append(out, localFile{
			Item: Item{ID: len(out), Path: rel, Size: info.Size(), Mtime: info.ModTime().Unix()},
			Src:  src,
		})
		return nil
	}

	for _, p := range paths {
		p = filepath.Clean(p)
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			if err := add(p, filepath.Base(p), info); err != nil {
				return nil, err
			}
			continue
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s: not a regular file or folder", p)
		}
		root := filepath.Base(p)
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(p, path)
			if err != nil {
				return err
			}
			return add(path, filepath.Join(root, rel), fi)
		})
		if err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nothing to send")
	}
	return out, nil
}
