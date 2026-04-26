package utils

import (
	"bytes"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/image/draw"
)

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

	thumbName := strings.ReplaceAll(uuid.New().String(), "-", "") + ".jpg"
	thumbPath := filepath.Join(ThumbsDir, thumbName)

	if strings.HasPrefix(actualMime, "image/") {
		if err := resizeImage(sourcePath, thumbPath); err == nil {
			return &thumbPath
		}
	} else if strings.HasPrefix(actualMime, "video/") {
		if ffmpegPath == "disabled" {
			return nil
		}
		cmd := exec.Command(
			ffmpegPath, "-y", "-i", sourcePath,
			"-ss", "00:00:00.000", "-vframes", "1",
			"-vf", "scale=320:-1", thumbPath,
		)
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
		if err := cmd.Run(); err == nil {
			if _, err := os.Stat(thumbPath); err == nil {
				return &thumbPath
			}
		}
	}

	return nil
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
