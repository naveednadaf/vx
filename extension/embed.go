// Package extension embeds the Chrome MV3 extension so `vx ext install`
// can materialize it without needing the source tree.
package extension

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed manifest.json background.js popup.html popup.css popup.js options.html options.js icons
var files embed.FS

// Extract writes the embedded extension to dest.
func Extract(dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	count := 0
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dest, path), 0o755)
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dest, path), data, 0o644); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("extract extension: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("embedded extension is empty")
	}
	return nil
}
