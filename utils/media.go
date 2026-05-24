package utils

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/image/draw"
)

func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

var ThumbsDir string

func InitMedia(dir string) {
	ThumbsDir = dir
	os.MkdirAll(ThumbsDir, os.ModePerm)
}

func CreateLocalThumbnail(sourcePath, mimeType, ffmpegPath string) *string {
	actualMime := mimeType
	if actualMime == "" || actualMime == "application/octet-stream" {
		actualMime = mime.TypeByExtension(filepath.Ext(sourcePath))
	}

	ext := strings.ToLower(filepath.Ext(sourcePath))
	thumbName := strings.ReplaceAll(uuid.New().String(), "-", "") + ".jpg"
	thumbPath := filepath.Join(ThumbsDir, thumbName)

	if ext == ".epub" || ext == ".cbz" || actualMime == "application/epub+zip" || actualMime == "application/x-cbz" {
		if path := extractZipCover(sourcePath, thumbPath); path != nil {
			return path
		}
	}

	if strings.HasPrefix(actualMime, "image/") {
		if err := resizeImage(sourcePath, thumbPath); err == nil {
			return &thumbPath
		}
	} else if strings.HasPrefix(actualMime, "video/") {
		if ffmpegPath == "disabled" {
			return nil
		}
		cmd := exec.Command(
			ffmpegPath, "-y", "-ss", "00:00:01.000", "-i", sourcePath,
			"-vframes", "1",
			"-vf", "scale=320:-1", thumbPath,
		)
		cmd.Env = os.Environ()
		if err := cmd.Run(); err == nil {
			if _, err := os.Stat(thumbPath); err == nil {
				return &thumbPath
			}
		}
	} else if strings.HasPrefix(actualMime, "audio/") {
		if ffmpegPath == "disabled" {
			return nil
		}
		cmd := exec.Command(
			ffmpegPath, "-y", "-i", sourcePath,
			"-an", "-vframes", "1",
			"-vf", "scale=320:-1", thumbPath,
		)
		cmd.Env = os.Environ()
		if err := cmd.Run(); err == nil {
			if _, err := os.Stat(thumbPath); err == nil {
				return &thumbPath
			}
		}
	}

	return nil
}

func extractZipCover(sourcePath, thumbPath string) *string {
	r, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil
	}
	defer r.Close()

	ext := strings.ToLower(filepath.Ext(sourcePath))
	var targetFile *zip.File

	if ext == ".cbz" {
		var images []string
		imgMap := make(map[string]*zip.File)
		for _, f := range r.File {
			if f.FileInfo().IsDir() {
				continue
			}
			name := strings.ToLower(f.Name)
			if strings.HasPrefix(filepath.Base(name), ".") || strings.Contains(name, "__macosx") {
				continue
			}
			if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") ||
				strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".webp") ||
				strings.HasSuffix(name, ".gif") || strings.HasSuffix(name, ".bmp") {
				images = append(images, f.Name)
				imgMap[f.Name] = f
			}
		}
		if len(images) > 0 {
			sort.Slice(images, func(i, j int) bool {
				return NaturalLess(images[i], images[j])
			})
			targetFile = imgMap[images[0]]
		}
	} else {
		var coverCandidates []*zip.File
		var fallbackImages []*zip.File

		for _, f := range r.File {
			if f.FileInfo().IsDir() {
				continue
			}
			name := strings.ToLower(f.Name)
			baseName := filepath.Base(name)
			if strings.HasPrefix(baseName, ".") || strings.Contains(name, "__macosx") {
				continue
			}

			isImg := strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") ||
				strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".webp") ||
				strings.HasSuffix(name, ".gif") || strings.HasSuffix(name, ".bmp")

			if isImg {
				if strings.Contains(baseName, "cover") || strings.Contains(baseName, "thumbnail") || strings.Contains(baseName, "bia") {
					coverCandidates = append(coverCandidates, f)
				}
				fallbackImages = append(fallbackImages, f)
			}
		}

		if len(coverCandidates) > 0 {
			sort.Slice(coverCandidates, func(i, j int) bool {
				nameI := strings.ToLower(filepath.Base(coverCandidates[i].Name))
				nameJ := strings.ToLower(filepath.Base(coverCandidates[j].Name))
				exactI := strings.HasPrefix(nameI, "cover.")
				exactJ := strings.HasPrefix(nameJ, "cover.")
				if exactI && !exactJ {
					return true
				}
				if !exactI && exactJ {
					return false
				}
				return len(coverCandidates[i].Name) < len(coverCandidates[j].Name)
			})
			targetFile = coverCandidates[0]
		} else if len(fallbackImages) > 0 {
			sort.Slice(fallbackImages, func(i, j int) bool {
				return fallbackImages[i].Name < fallbackImages[j].Name
			})
			targetFile = fallbackImages[0]
		}
	}

	if targetFile == nil {
		return nil
	}

	rc, err := targetFile.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}

	bounds := img.Bounds()
	width := bounds.Max.X
	height := bounds.Max.Y

	if width > 320 {
		height = (height * 320) / width
		width = 320
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)

	out, err := os.Create(thumbPath)
	if err != nil {
		return nil
	}
	defer out.Close()

	if err := jpeg.Encode(out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil
	}

	return &thumbPath
}

func resizeImage(source, target string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}

	bounds := img.Bounds()
	width := bounds.Max.X
	height := bounds.Max.Y

	if width > 320 {
		height = (height * 320) / width
		width = 320
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)

	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()

	return jpeg.Encode(out, dst, &jpeg.Options{Quality: 85})
}

func HasTorrentExtension(filename string) bool {
	return strings.ToLower(filepath.Ext(filename)) == ".torrent"
}
