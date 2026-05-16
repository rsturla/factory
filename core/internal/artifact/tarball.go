package artifact

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CreateTarGz packages directory into tar.gz archive.
// Returns io.Reader with compressed data and total size.
func CreateTarGz(srcDir string) (io.Reader, int64, error) {
	// Create buffer for tar.gz data
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		// Create gzip writer
		gw := gzip.NewWriter(pw)
		defer gw.Close()

		// Create tar writer
		tw := tar.NewWriter(gw)
		defer tw.Close()

		// Limits for resource protection
		const maxFiles = 10000
		const maxFileSize = 100 * 1024 * 1024     // 100MB per file
		const maxTotalSize = 1 * 1024 * 1024 * 1024 // 1GB total
		var fileCount int
		var totalSize int64

		// Walk directory tree
		err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Skip directories (tar stores files only, dirs created implicitly)
			if info.IsDir() {
				return nil
			}

			// Skip symlinks (security: prevent following to sensitive files)
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}

			// Enforce file count limit
			fileCount++
			if fileCount > maxFiles {
				return fmt.Errorf("too many files in directory: %d (max %d)", fileCount, maxFiles)
			}

			// Enforce per-file size limit
			if info.Size() > maxFileSize {
				return fmt.Errorf("file too large: %s (%d bytes, max %d)", path, info.Size(), maxFileSize)
			}

			// Enforce total size limit
			totalSize += info.Size()
			if totalSize > maxTotalSize {
				return fmt.Errorf("total directory size exceeds limit: %d bytes (max %d)", totalSize, maxTotalSize)
			}

			// Only process regular files
			if !info.Mode().IsRegular() {
				return nil
			}

			// Create tar header
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return fmt.Errorf("create tar header: %w", err)
			}

			// Set relative path (strip srcDir prefix)
			relPath, err := filepath.Rel(srcDir, path)
			if err != nil {
				return fmt.Errorf("get relative path: %w", err)
			}
			header.Name = relPath

			// Write header
			if err := tw.WriteHeader(header); err != nil {
				return fmt.Errorf("write tar header: %w", err)
			}

			// Write file content
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open file: %w", err)
			}
			defer f.Close()

			if _, err := io.Copy(tw, f); err != nil {
				return fmt.Errorf("write tar content: %w", err)
			}

			return nil
		})

		if err != nil {
			pw.CloseWithError(err)
		}
	}()

	// Note: We can't know final size until compression complete
	// Return -1 for size, caller will measure during upload
	return pr, -1, nil
}

// ExtractTarGz unpacks tar.gz archive to destination directory.
func ExtractTarGz(r io.Reader, destDir string) error {
	// Create gzip reader
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("create gzip reader: %w", err)
	}
	defer gr.Close()

	// Create tar reader
	tr := tar.NewReader(gr)

	// Limits for resource protection
	const maxFiles = 10000
	const maxFileSize = 100 * 1024 * 1024       // 100MB per file
	const maxTotalSize = 1 * 1024 * 1024 * 1024 // 1GB total
	const maxPathDepth = 100
	var fileCount int
	var totalSize int64

	// Extract files
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}

		// Enforce file count limit
		fileCount++
		if fileCount > maxFiles {
			return fmt.Errorf("too many files in tarball: %d (max %d)", fileCount, maxFiles)
		}

		// Enforce file size limit
		if header.Size > maxFileSize {
			return fmt.Errorf("file too large in tarball: %s (%d bytes, max %d)", header.Name, header.Size, maxFileSize)
		}

		// Enforce total size limit
		totalSize += header.Size
		if totalSize > maxTotalSize {
			return fmt.Errorf("tarball total size exceeds limit: %d bytes (max %d)", totalSize, maxTotalSize)
		}

		// Enforce path depth limit
		if strings.Count(header.Name, "/") > maxPathDepth {
			return fmt.Errorf("path too deep in tarball: %s", header.Name)
		}

		// Only extract regular files (skip symlinks, devices, etc.)
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			continue
		}

		// Skip directories (we create them as needed)
		if header.Typeflag == tar.TypeDir {
			continue
		}

		// Validate path (prevent directory traversal and absolute paths)
		cleanPath := filepath.Clean(header.Name)
		if strings.Contains(cleanPath, "..") || filepath.IsAbs(cleanPath) || strings.Contains(header.Name, "\x00") {
			return fmt.Errorf("invalid path in tarball: %s", header.Name)
		}

		// Build destination path
		destPath := filepath.Join(destDir, cleanPath)

		// Double-check the result stays within destDir (zip slip protection)
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("path traversal detected in tarball: %s", header.Name)
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("create parent dirs: %w", err)
		}

		// Create file with safe permissions (not from tarball)
		f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("create file: %w", err)
		}

		// Copy content
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return fmt.Errorf("write file content: %w", err)
		}
		f.Close()
	}

	return nil
}

// ReadFilesFromTarGz extracts files from tar.gz into memory map.
// Returns map[path]content for in-memory processing.
func ReadFilesFromTarGz(r io.Reader) (map[string][]byte, error) {
	files := make(map[string][]byte)

	// Create gzip reader
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("create gzip reader: %w", err)
	}
	defer gr.Close()

	// Create tar reader
	tr := tar.NewReader(gr)

	// Read files
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar header: %w", err)
		}

		// Validate path
		if strings.Contains(header.Name, "..") {
			return nil, fmt.Errorf("invalid path in tarball: %s", header.Name)
		}

		// Read file content into memory
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read file content: %w", err)
		}

		files[header.Name] = content
	}

	return files, nil
}
