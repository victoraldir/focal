package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Media is a photo or rendered video in the media directory.
type Media struct {
	// Path is relative to the media directory, with forward slashes.
	Path    string    `json:"path"`
	URL     string    `json:"url"`
	Kind    string    `json:"kind"` // "photo" or "video"
	Session string    `json:"session,omitempty"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

const maxMediaListing = 1000

func mediaKind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png":
		return "photo"
	case ".mp4", ".mov", ".m4v":
		return "video"
	}
	return ""
}

func newMedia(root, path string, info fs.FileInfo) (Media, bool) {
	kind := mediaKind(path)
	if kind == "" || strings.HasPrefix(filepath.Base(path), ".") {
		return Media{}, false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return Media{}, false
	}
	rel = filepath.ToSlash(rel)
	session := ""
	if dir, _, ok := strings.Cut(rel, "/"); ok {
		session = dir
	}
	return Media{Path: rel, URL: "/media/" + rel, Kind: kind, Session: session, Size: info.Size(), ModTime: info.ModTime()}, true
}

// listMedia returns photos and videos in root and its immediate
// subdirectories (one per timelapse session), newest first.
func listMedia(root string) ([]Media, error) {
	var out []Media
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			if path != root && strings.Count(filepath.ToSlash(strings.TrimPrefix(path, root)), "/") > 1 {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if m, ok := newMedia(root, path, info); ok {
			out = append(out, m)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].ModTime.After(out[j].ModTime)
		}
		return out[i].Path > out[j].Path
	})
	if len(out) > maxMediaListing {
		out = out[:maxMediaListing]
	}
	return out, err
}
