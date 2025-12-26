package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/fastnas/fastnas/internal/config"
	"github.com/fastnas/fastnas/internal/database"
	"github.com/fastnas/fastnas/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func UploadHandler(c *fiber.Ctx) error {
	// Parse the multipart form file
	filePayload, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Failed to parse file",
		})
	}

	// Generate UUID
	newUUID := uuid.New().String()

	// Determine disk path
	cfg := config.Load()
	diskFilename := newUUID + filepath.Ext(filePayload.Filename)
	diskPath := filepath.Join(cfg.DataDir, diskFilename)

	// Save file (Stream to disk)
	if err := c.SaveFile(filePayload, diskPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to save file",
		})
	}

	// Save metadata
	newFile := models.File{
		UUID:      newUUID,
		Filename:  filePayload.Filename,
		Size:      filePayload.Size,
		MimeType:  filePayload.Header.Get("Content-Type"),
		DiskPath:  diskPath,
		CreatedAt: time.Now(),
	}

	if err := database.DB.Create(&newFile).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to save metadata",
		})
	}

	return c.JSON(newFile)
}

func ListFilesHandler(c *fiber.Ctx) error {
	var files []models.File
	// Provide newest first
	if err := database.DB.Order("created_at desc").Find(&files).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch files",
		})
	}
	return c.JSON(files)
}

func DownloadHandler(c *fiber.Ctx) error {
	id := c.Params("uuid")
	var file models.File
	if err := database.DB.Where("uuid = ?", id).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "File not found",
		})
	}

	disposition := "attachment"
	if c.Query("preview") == "true" {
		disposition = "inline"
	}

	c.Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, file.Filename))
	c.Set("Content-Type", file.MimeType)

	return c.SendFile(file.DiskPath)
}

func DeleteFileHandler(c *fiber.Ctx) error {
	id := c.Params("uuid")
	var file models.File

	// 1. Find file in DB
	if err := database.DB.Where("uuid = ?", id).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "File not found",
		})
	}

	// 2. Remove from Disk
	// We ignore error if file doesn't exist on disk, just clean up DB
	if err := os.Remove(file.DiskPath); err != nil && !os.IsNotExist(err) {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to delete file from disk: " + err.Error(),
		})
	}

	// 3. Remove from DB
	if err := database.DB.Delete(&file).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to delete file from database",
		})
	}

	return c.SendStatus(fiber.StatusOK)
}

func ThumbnailHandler(c *fiber.Ctx) error {
	id := c.Params("uuid")
	cfg := config.Load()
	thumbDir := filepath.Join(cfg.DataDir, "thumbs")
	thumbPath := filepath.Join(thumbDir, id+".jpg")

	// 1. Check Cache
	if _, err := os.Stat(thumbPath); err == nil {
		return c.SendFile(thumbPath)
	}

	// 2. Resolve Original File
	var file models.File
	if err := database.DB.Where("uuid = ?", id).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).SendString("File not found")
	}

	// Only process images
	if !strings.HasPrefix(file.MimeType, "image/") {
		return c.Status(fiber.StatusBadRequest).SendString("Not an image")
	}

	// 3. Generate Thumbnail
	if err := os.MkdirAll(thumbDir, 0755); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Cache error")
	}

	// Open original
	src, err := imaging.Open(file.DiskPath, imaging.AutoOrientation(true))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to open image")
	}

	// Resize (Fill 200x200)
	dst := imaging.Fill(src, 200, 200, imaging.Center, imaging.Lanczos)

	// Save to cache
	if err := imaging.Save(dst, thumbPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save thumbnail")
	}

	return c.SendFile(thumbPath)
}
