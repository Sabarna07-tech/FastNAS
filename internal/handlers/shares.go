package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/fastnas/fastnas/internal/auth"
	"github.com/fastnas/fastnas/internal/database"
	"github.com/fastnas/fastnas/internal/models"
	"github.com/gofiber/fiber/v2"
)

// generateShareToken returns an unguessable, URL-safe token.
func generateShareToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateShareHandler creates a share link for one of the caller's files.
func CreateShareHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	fileUUID := c.Params("uuid")

	var file models.File
	if err := database.DB.Where("uuid = ? AND owner_id = ?", fileUUID, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "File not found"})
	}

	var req struct {
		ExpiresIn int64 `json:"expiresIn"` // seconds from now; 0 or omitted = never
	}
	_ = c.BodyParser(&req) // body is optional

	var expiresAt *time.Time
	if req.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	token, err := generateShareToken()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to generate token"})
	}

	share := models.Share{
		Token:     token,
		FileUUID:  file.UUID,
		OwnerID:   identity.ID,
		ExpiresAt: expiresAt,
	}
	if err := database.DB.Create(&share).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create share"})
	}

	return c.Status(fiber.StatusCreated).JSON(share)
}

// ListSharesHandler returns the active shares for one of the caller's files.
func ListSharesHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	fileUUID := c.Params("uuid")

	var file models.File
	if err := database.DB.Where("uuid = ? AND owner_id = ?", fileUUID, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "File not found"})
	}

	var shares []models.Share
	if err := database.DB.Where("file_uuid = ? AND owner_id = ?", fileUUID, identity.ID).
		Order("created_at desc").Find(&shares).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch shares"})
	}
	return c.JSON(shares)
}

// DeleteShareHandler revokes a share by token (scoped to the owner).
func DeleteShareHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	token := c.Params("token")

	res := database.DB.Where("token = ? AND owner_id = ?", token, identity.ID).Delete(&models.Share{})
	if res.Error != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to revoke share"})
	}
	if res.RowsAffected == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Share not found"})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// AccessShareHandler serves a shared file publicly via its token. It is not
// scoped to any owner; the unguessable token is the credential.
func AccessShareHandler(c *fiber.Ctx) error {
	token := c.Params("token")

	var share models.Share
	if err := database.DB.Where("token = ?", token).First(&share).Error; err != nil {
		return c.Status(fiber.StatusNotFound).SendString("Share not found")
	}
	if share.ExpiresAt != nil && time.Now().After(*share.ExpiresAt) {
		return c.Status(fiber.StatusGone).SendString("This link has expired")
	}

	var file models.File
	if err := database.DB.Where("uuid = ?", share.FileUUID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).SendString("File not found")
	}

	disposition := "attachment"
	if c.Query("preview") == "true" {
		disposition = "inline"
	}
	safeName := strings.NewReplacer(`"`, `%22`, `\`, `%5C`).Replace(file.Filename)
	c.Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, safeName))
	c.Set("Content-Type", file.MimeType)

	return c.SendFile(file.DiskPath)
}
