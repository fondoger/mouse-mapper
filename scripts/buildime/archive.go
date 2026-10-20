package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	maximumExpandedSize = int64(4 << 30)
	maximumEntrySize    = int64(512 << 20)
	maximumEntries      = 100000
)

type archiveLink struct {
	name   string
	target string
	hard   bool
}

type extractor struct {
	root    string
	top     string
	total   int64
	entries int
	links   []archiveLink
}

func archivePath(root, top, name string) (string, error) {
	name = strings.TrimSuffix(name, "/")
	if name == "" || len(name) > 4096 || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) || path.Clean(name) != name {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	parts := strings.Split(name, "/")
	if parts[0] != top {
		return "", fmt.Errorf("archive path is outside expected top directory %q: %q", top, name)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part {
			return "", fmt.Errorf("unsafe archive component %q", part)
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
			return "", fmt.Errorf("reserved Windows archive component %q", part)
		}
	}
	result := filepath.Join(root, filepath.FromSlash(name))
	if !strictlyWithin(result, root) {
		return "", fmt.Errorf("archive path escapes extraction root: %q", name)
	}
	return result, nil
}

func (e *extractor) entry(name string, size int64) (string, error) {
	e.entries++
	if size < 0 || size > maximumEntrySize || e.entries > maximumEntries || e.total > maximumExpandedSize-size {
		return "", errors.New("compiler archive exceeds extraction limits")
	}
	e.total += size
	return archivePath(e.root, e.top, name)
}

// No file may be written through a symlink created by another archive entry.
func (e *extractor) directory(directory string) error {
	if directory == e.root {
		return nil
	}
	if !strictlyWithin(directory, e.root) {
		return fmt.Errorf("archive directory escapes extraction root: %s", directory)
	}
	rel, err := filepath.Rel(e.root, directory)
	if err != nil {
		return err
	}
	current := e.root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive directory is not a real directory: %s", current)
		}
	}
	return nil
}

func (e *extractor) regular(destination string, size int64, mode os.FileMode, source io.Reader) error {
	if err := e.directory(filepath.Dir(destination)); err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm()&0755|0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(source, size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		return fmt.Errorf("archive entry size mismatch for %s", destination)
	}
	return nil
}

func extractZip(archive, root, top string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	e := extractor{root: root, top: top}
	for _, f := range r.File {
		if f.UncompressedSize64 > uint64(maximumEntrySize) {
			return errors.New("ZIP entry exceeds extraction size limit")
		}
		destination, err := e.entry(f.Name, int64(f.UncompressedSize64))
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := e.directory(destination); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("unsupported ZIP entry type: %s", f.Name)
		}
		reader, err := f.Open()
		if err != nil {
			return err
		}
		err = e.regular(destination, int64(f.UncompressedSize64), f.Mode(), reader)
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func extractXZ(ctx context.Context, archive, root, top string) error {
	xz, err := exec.LookPath("xz")
	if err != nil {
		return errors.New("extracting the Linux compiler requires the distribution's xz-utils package (xz)")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, xz, "-dc", "--", archive)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	reader, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := extractTar(reader, root, top)
	if extractErr == nil {
		// Read the decompressor through its checksum and bounded TAR padding;
		// waiting with an unread pipe can otherwise deadlock on trailing bytes.
		var trailing int64
		trailing, extractErr = io.Copy(io.Discard, io.LimitReader(reader, (1<<20)+1))
		if extractErr == nil && trailing > 1<<20 {
			extractErr = errors.New("compiler TAR has excessive trailing data")
		}
	}
	if extractErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		return fmt.Errorf("decompress compiler: %w: %s", waitErr, stderr.String())
	}
	return nil
}

func extractTar(reader io.Reader, root, top string) error {
	t := tar.NewReader(reader)
	e := extractor{root: root, top: top}
	for {
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		destination, err := e.entry(h.Name, h.Size)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := e.directory(destination); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := e.regular(destination, h.Size, os.FileMode(h.Mode), t); err != nil {
				return err
			}
		case tar.TypeLink, tar.TypeSymlink:
			e.links = append(e.links, archiveLink{name: h.Name, target: h.Linkname, hard: h.Typeflag == tar.TypeLink})
		default:
			return fmt.Errorf("unsupported TAR entry type %d: %s", h.Typeflag, h.Name)
		}
	}
	return e.finishLinks()
}

func (e *extractor) finishLinks() error {
	// Regular files are complete before links are created. Link destinations must
	// have real directory parents, and all resolved symlinks must remain in top.
	for _, hard := range []bool{true, false} {
		for _, link := range e.links {
			if link.hard != hard {
				continue
			}
			destination, err := archivePath(e.root, e.top, link.name)
			if err != nil {
				return err
			}
			if err := e.directory(filepath.Dir(destination)); err != nil {
				return err
			}
			if link.target == "" || strings.ContainsAny(link.target, "\\:\x00") || path.IsAbs(link.target) {
				return fmt.Errorf("unsafe archive link %s -> %s", link.name, link.target)
			}
			targetName := link.target
			if !hard {
				targetName = path.Join(path.Dir(link.name), link.target)
			}
			target, err := archivePath(e.root, e.top, targetName)
			if err != nil {
				return err
			}
			if hard {
				info, err := os.Lstat(target)
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("hard link target is not a regular file: %s", targetName)
				}
				if err := os.Link(target, destination); err != nil {
					return err
				}
			} else if err := os.Symlink(filepath.FromSlash(link.target), destination); err != nil {
				return err
			}
		}
	}
	for _, link := range e.links {
		if link.hard {
			continue
		}
		destination, err := archivePath(e.root, e.top, link.name)
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(destination)
		if err != nil {
			return fmt.Errorf("resolve compiler link %s: %w", link.name, err)
		}
		resolvedTop, err := filepath.EvalSymlinks(filepath.Join(e.root, e.top))
		if err != nil {
			return err
		}
		if !strictlyWithin(resolved, resolvedTop) {
			return fmt.Errorf("compiler link escapes its top directory: %s", link.name)
		}
	}
	return nil
}
