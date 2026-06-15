package handlers

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fastnas/fastnas/internal/auth"
	"github.com/fastnas/fastnas/internal/database"
	"github.com/fastnas/fastnas/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// maxUploadSize is the largest single file a resumable upload may declare.
const maxUploadSize = 50 * 1024 * 1024 * 1024 // 50GB

// sessionLocks serializes chunk writes and finalization per upload session, so
// a double-submitted chunk can't corrupt the temp file or the offset.
var sessionLocks sync.Map // map[string]*sync.Mutex

func sessionLock(id string) *sync.Mutex {
	m, _ := sessionLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// CreateUploadHandler starts a resumable upload session and returns its id.
func CreateUploadHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)

	var req struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		MimeType string `json:"mimeType"`
		Folder   string `json:"folder"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	name := strings.TrimSpace(req.Filename)
	if name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Filename is required"})
	}
	if req.Size < 0 || req.Size > maxUploadSize {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid file size"})
	}

	// Enforce disk-space and quota limits up front, using the declared size.
	if ok, status, msg := checkUploadAllowed(identity.ID, req.Size); !ok {
		return c.Status(status).JSON(fiber.Map{"error": msg})
	}

	folderID, err := resolveFolderID(identity.ID, req.Folder)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Destination folder not found"})
	}

	cfg := appConfig
	uploadDir := filepath.Join(cfg.DataDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to prepare upload"})
	}

	sessionUUID := uuid.New().String()
	tempPath := filepath.Join(uploadDir, sessionUUID+".part")

	// Pre-create the (empty) temp file so chunk writes have a target.
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to prepare upload"})
	}
	_ = f.Close()

	session := models.UploadSession{
		UUID:     sessionUUID,
		OwnerID:  identity.ID,
		Filename: name,
		Size:     req.Size,
		MimeType: req.MimeType,
		FolderID: folderID,
		Uploaded: 0,
		TempPath: tempPath,
	}
	if err := database.DB.Create(&session).Error; err != nil {
		_ = os.Remove(tempPath)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create upload session"})
	}

	return c.Status(fiber.StatusCreated).JSON(session)
}

// UploadStatusHandler reports the current offset of a session, used to resume.
func UploadStatusHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("id")

	var session models.UploadSession
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&session).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Upload session not found"})
	}
	return c.JSON(session)
}

// UploadChunkHandler appends a chunk at the offset given by the Upload-Offset
// header. If the offset doesn't match the server's state it returns 409 with the
// authoritative offset so the client can resync.
func UploadChunkHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("id")

	lock := sessionLock(id)
	lock.Lock()
	defer lock.Unlock()

	var session models.UploadSession
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&session).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Upload session not found"})
	}

	offset, err := strconv.ParseInt(c.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid Upload-Offset header"})
	}

	// Offset mismatch: tell the client where we actually are so it can resume.
	if offset != session.Uploaded {
		c.Set("Upload-Offset", strconv.FormatInt(session.Uploaded, 10))
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"uploaded": session.Uploaded})
	}

	body := c.Body()
	n := int64(len(body))
	if n == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Empty chunk"})
	}
	if offset+n > session.Size {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Chunk exceeds declared file size"})
	}

	f, err := os.OpenFile(session.TempPath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to open upload"})
	}
	if _, err := f.WriteAt(body, offset); err != nil {
		_ = f.Close()
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to write chunk"})
	}
	if err := f.Close(); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to flush chunk"})
	}

	session.Uploaded = offset + n
	if err := database.DB.Model(&session).Update("uploaded", session.Uploaded).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to record progress"})
	}

	c.Set("Upload-Offset", strconv.FormatInt(session.Uploaded, 10))
	return c.JSON(fiber.Map{"uploaded": session.Uploaded})
}

// CompleteUploadHandler finalizes a fully-uploaded session: it moves the temp
// file into place and creates the File metadata.
func CompleteUploadHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("id")

	lock := sessionLock(id)
	lock.Lock()
	defer lock.Unlock()

	var session models.UploadSession
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&session).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Upload session not found"})
	}

	if session.Uploaded != session.Size {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":    "Upload incomplete",
			"uploaded": session.Uploaded,
			"size":     session.Size,
		})
	}

	cfg := appConfig
	newUUID := uuid.New().String()
	finalPath := filepath.Join(cfg.DataDir, newUUID+filepath.Ext(session.Filename))

	if err := os.Rename(session.TempPath, finalPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to finalize upload"})
	}

	// Checksum the assembled file (best effort; empty on failure).
	checksum, _ := computeChecksum(finalPath)

	file := models.File{
		UUID:       newUUID,
		Filename:   session.Filename,
		Size:       session.Size,
		MimeType:   session.MimeType,
		DiskPath:   finalPath,
		OwnerID:    identity.ID,
		OwnerEmail: identity.Email,
		FolderID:   session.FolderID,
		Checksum:   checksum,
		CreatedAt:  time.Now(),
	}
	if err := database.DB.Create(&file).Error; err != nil {
		_ = os.Remove(finalPath)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save metadata"})
	}

	database.DB.Delete(&session)
	sessionLocks.Delete(id)

	return c.JSON(file)
}

// AbortUploadHandler cancels an upload and removes its temp file.
func AbortUploadHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("id")

	lock := sessionLock(id)
	lock.Lock()
	defer lock.Unlock()

	var session models.UploadSession
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&session).Error; err != nil {
		// Already gone; treat as success.
		return c.SendStatus(fiber.StatusNoContent)
	}

	_ = os.Remove(session.TempPath)
	database.DB.Delete(&session)
	sessionLocks.Delete(id)

	return c.SendStatus(fiber.StatusNoContent)
}

// CleanupStaleUploads removes upload sessions (and their temp files) that have
// not been touched within maxAge. Safe to call periodically.
func CleanupStaleUploads(maxAge time.Duration) {
	if database.DB == nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)

	var stale []models.UploadSession
	if err := database.DB.Where("updated_at < ?", cutoff).Find(&stale).Error; err != nil {
		return
	}
	for _, session := range stale {
		_ = os.Remove(session.TempPath)
		database.DB.Delete(&session)
		sessionLocks.Delete(session.UUID)
	}
}
