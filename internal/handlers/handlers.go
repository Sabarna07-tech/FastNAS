package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/fastnas/fastnas/internal/auth"
	"github.com/fastnas/fastnas/internal/config"
	"github.com/fastnas/fastnas/internal/database"
	"github.com/fastnas/fastnas/internal/diskspace"
	"github.com/fastnas/fastnas/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// appConfig holds the configuration injected once at startup, so handlers don't
// re-read the environment on every request.
var appConfig = config.Load()

// Configure injects the application configuration used by all handlers. It must
// be called once during startup before the server begins serving requests.
func Configure(cfg *config.Config) {
	appConfig = cfg
}

// breadcrumb is a single hop on the path from the root to the current folder.
type breadcrumb struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// listingResponse is the payload returned by ListFilesHandler.
type listingResponse struct {
	Folder      *models.Folder  `json:"folder"`      // current folder (nil at root or while searching)
	Breadcrumbs []breadcrumb    `json:"breadcrumbs"` // root -> current folder
	Folders     []models.Folder `json:"folders"`     // subfolders of the current folder
	Files       []models.File   `json:"files"`       // files on the current page
	Query       string          `json:"query"`       // active search query, if any
	Page        int             `json:"page"`
	Limit       int             `json:"limit"`
	Total       int64           `json:"total"`      // total files matching the query/folder
	TotalPages  int             `json:"totalPages"` // total pages of files
}

// HealthHandler reports service liveness, including database connectivity.
func HealthHandler(c *fiber.Ctx) error {
	if err := database.Ping(); err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status": "unavailable",
			"error":  "database unreachable",
		})
	}
	return c.JSON(fiber.Map{"status": "ok"})
}

// MeHandler returns the resolved identity for the current request, so the
// frontend can show who is logged in.
func MeHandler(c *fiber.Ctx) error {
	return c.JSON(auth.Get(c))
}

// StatsHandler returns aggregate usage for the current user across all folders.
func StatsHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)

	var result struct {
		TotalFiles int64
		TotalSize  int64
	}
	if err := database.DB.Model(&models.File{}).
		Where("owner_id = ?", identity.ID).
		Select("COUNT(*) AS total_files, COALESCE(SUM(size), 0) AS total_size").
		Scan(&result).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to compute stats",
		})
	}

	return c.JSON(fiber.Map{
		"totalFiles": result.TotalFiles,
		"totalSize":  result.TotalSize,
	})
}

func UploadHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)

	// Resolve the destination folder (empty = root).
	folderID, err := resolveFolderID(identity.ID, c.FormValue("folder"))
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Destination folder not found",
		})
	}

	// Parse the multipart form file
	filePayload, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Failed to parse file",
		})
	}

	// Enforce disk-space and quota limits before writing anything.
	if ok, status, msg := checkUploadAllowed(identity.ID, filePayload.Size); !ok {
		return c.Status(status).JSON(fiber.Map{"error": msg})
	}

	// Generate UUID
	newUUID := uuid.New().String()

	// Determine disk path
	cfg := appConfig
	diskFilename := newUUID + filepath.Ext(filePayload.Filename)
	diskPath := filepath.Join(cfg.DataDir, diskFilename)

	// Save file (Stream to disk)
	if err := c.SaveFile(filePayload, diskPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to save file",
		})
	}

	// Checksum the stored file (best effort; empty on failure).
	checksum, _ := computeChecksum(diskPath)

	// Save metadata
	newFile := models.File{
		UUID:       newUUID,
		Filename:   filePayload.Filename,
		Size:       filePayload.Size,
		MimeType:   filePayload.Header.Get("Content-Type"),
		DiskPath:   diskPath,
		OwnerID:    identity.ID,
		OwnerEmail: identity.Email,
		FolderID:   folderID,
		Checksum:   checksum,
		CreatedAt:  time.Now(),
	}

	if err := database.DB.Create(&newFile).Error; err != nil {
		// Best-effort cleanup of the orphaned file on disk.
		_ = os.Remove(diskPath)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to save metadata",
		})
	}

	return c.JSON(newFile)
}

// CreateFolderHandler creates a new folder for the current user, optionally
// nested under a parent folder.
func CreateFolderHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)

	var req struct {
		Name   string `json:"name"`
		Parent string `json:"parent"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Folder name is required",
		})
	}
	if len(name) > 255 || strings.ContainsAny(name, `/\`) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid folder name",
		})
	}

	// Resolve and validate the parent folder (empty = root).
	parentID, err := resolveFolderID(identity.ID, req.Parent)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Parent folder not found",
		})
	}

	folder := models.Folder{
		UUID:      uuid.New().String(),
		Name:      name,
		OwnerID:   identity.ID,
		ParentID:  parentID,
		CreatedAt: time.Now(),
	}
	if err := database.DB.Create(&folder).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to create folder",
		})
	}

	return c.JSON(folder)
}

// DeleteFolderHandler removes a folder and everything beneath it: all
// descendant folders and the files they contain (including disk + thumbnails).
func DeleteFolderHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")

	// Verify the folder exists and is owned by the caller.
	var root models.Folder
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&root).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Folder not found",
		})
	}

	// Collect the whole subtree of folder UUIDs (breadth-first).
	subtree := []string{root.UUID}
	queue := []string{root.UUID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		var children []models.Folder
		if err := database.DB.Where("owner_id = ? AND parent_id = ?", identity.ID, current).Find(&children).Error; err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to enumerate folder contents",
			})
		}
		for _, child := range children {
			subtree = append(subtree, child.UUID)
			queue = append(queue, child.UUID)
		}
	}

	// Remove the files contained anywhere in the subtree from disk first.
	// Unscoped() so files already in the trash are purged too (the folder, and
	// therefore the only path back to them, is going away permanently).
	var files []models.File
	if err := database.DB.Unscoped().Where("owner_id = ? AND folder_id IN ?", identity.ID, subtree).Find(&files).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to enumerate files",
		})
	}
	cfg := appConfig
	for _, f := range files {
		removeFileArtifacts(cfg, f)
	}

	// Delete file + folder rows (hard delete — folder deletion is permanent).
	if err := database.DB.Unscoped().Where("owner_id = ? AND folder_id IN ?", identity.ID, subtree).Delete(&models.File{}).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to delete files",
		})
	}
	if err := database.DB.Where("owner_id = ? AND uuid IN ?", identity.ID, subtree).Delete(&models.Folder{}).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to delete folders",
		})
	}

	return c.SendStatus(fiber.StatusOK)
}

func ListFilesHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)

	query := strings.TrimSpace(c.Query("q"))
	folderUUID := c.Query("folder")
	page, limit, offset := parsePagination(c)

	resp := listingResponse{
		Breadcrumbs: []breadcrumb{},
		Folders:     []models.Folder{},
		Files:       []models.File{},
		Query:       query,
		Page:        page,
		Limit:       limit,
	}

	// applyFilters scopes a fresh query to the owner and the active
	// folder/search. Passing a new *gorm.DB each call avoids condition reuse.
	applyFilters := func(tx *gorm.DB) *gorm.DB {
		tx = tx.Where("owner_id = ?", identity.ID)
		switch {
		case query != "":
			// Search mode: match filenames across all folders.
			tx = tx.Where("filename LIKE ?", "%"+query+"%")
		case folderUUID == "":
			tx = tx.Where("folder_id IS NULL")
		default:
			tx = tx.Where("folder_id = ?", folderUUID)
		}
		return tx
	}

	// In browse mode, resolve the current folder, its breadcrumbs and subfolders.
	if query == "" {
		current, err := resolveFolder(identity.ID, folderUUID)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "Folder not found",
			})
		}
		resp.Folder = current
		resp.Breadcrumbs = buildBreadcrumbs(identity.ID, current)

		folderQuery := database.DB.Where("owner_id = ?", identity.ID)
		if folderUUID == "" {
			folderQuery = folderQuery.Where("parent_id IS NULL")
		} else {
			folderQuery = folderQuery.Where("parent_id = ?", folderUUID)
		}
		if err := folderQuery.Order("name ASC").Find(&resp.Folders).Error; err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to fetch folders",
			})
		}
	}

	// Total file count for pagination.
	if err := applyFilters(database.DB.Model(&models.File{})).Count(&resp.Total).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to count files",
		})
	}
	resp.TotalPages = int((resp.Total + int64(limit) - 1) / int64(limit))

	// Page of files, newest first.
	if err := applyFilters(database.DB.Model(&models.File{})).
		Order("created_at desc").Limit(limit).Offset(offset).Find(&resp.Files).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch files",
		})
	}

	return c.JSON(resp)
}

func DownloadHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")
	var file models.File
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "File not found",
		})
	}

	disposition := "attachment"
	if c.Query("preview") == "true" {
		disposition = "inline"
	}

	// Escape quotes/backslashes so a crafted filename can't break the header.
	safeName := strings.NewReplacer(`"`, `%22`, `\`, `%5C`).Replace(file.Filename)
	c.Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, safeName))
	c.Set("Content-Type", file.MimeType)

	return c.SendFile(file.DiskPath)
}

// DeleteFileHandler moves a file to the trash (soft delete). The bytes stay on
// disk so the file can be restored; permanent removal happens via the trash.
func DeleteFileHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")
	var file models.File

	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "File not found",
		})
	}

	// Soft delete: GORM sets deleted_at; normal listings now exclude it.
	if err := database.DB.Delete(&file).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to delete file",
		})
	}

	return c.SendStatus(fiber.StatusOK)
}

// TrashListHandler lists the caller's soft-deleted files.
func TrashListHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	var files []models.File
	if err := database.DB.Unscoped().
		Where("owner_id = ? AND deleted_at IS NOT NULL", identity.ID).
		Order("deleted_at desc").Find(&files).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch trash"})
	}
	return c.JSON(files)
}

// RestoreFileHandler brings a soft-deleted file back from the trash.
func RestoreFileHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")

	var file models.File
	if err := database.DB.Unscoped().Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "File not found"})
	}
	if err := database.DB.Unscoped().Model(&file).Update("deleted_at", nil).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to restore file"})
	}
	return c.SendStatus(fiber.StatusOK)
}

// PermanentDeleteHandler permanently removes one trashed file (disk + metadata).
func PermanentDeleteHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")

	var file models.File
	if err := database.DB.Unscoped().Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "File not found"})
	}
	removeFileArtifacts(appConfig, file)
	if err := database.DB.Unscoped().Delete(&file).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete file"})
	}
	return c.SendStatus(fiber.StatusOK)
}

// EmptyTrashHandler permanently removes all of the caller's trashed files.
func EmptyTrashHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)

	var files []models.File
	if err := database.DB.Unscoped().
		Where("owner_id = ? AND deleted_at IS NOT NULL", identity.ID).Find(&files).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to empty trash"})
	}
	for _, f := range files {
		removeFileArtifacts(appConfig, f)
	}
	if err := database.DB.Unscoped().
		Where("owner_id = ? AND deleted_at IS NOT NULL", identity.ID).Delete(&models.File{}).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to empty trash"})
	}
	return c.SendStatus(fiber.StatusOK)
}

// UpdateFileHandler renames and/or moves a file. Both fields are optional;
// a present "folder" of "" moves the file to the root.
func UpdateFileHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")

	var req struct {
		Filename *string `json:"filename"`
		Folder   *string `json:"folder"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	var file models.File
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "File not found"})
	}

	updates := map[string]interface{}{}
	if req.Filename != nil {
		name := strings.TrimSpace(*req.Filename)
		if name == "" || len(name) > 255 || strings.ContainsAny(name, `/\`) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid filename"})
		}
		updates["filename"] = name
	}
	if req.Folder != nil {
		folderID, err := resolveFolderID(identity.ID, *req.Folder)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Destination folder not found"})
		}
		updates["folder_id"] = folderID // nil moves to root
	}
	if len(updates) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Nothing to update"})
	}

	if err := database.DB.Model(&file).Updates(updates).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update file"})
	}

	database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&file)
	return c.JSON(file)
}

// UpdateFolderHandler renames and/or moves a folder. Moving is guarded against
// cycles (a folder may not be moved into itself or one of its descendants).
func UpdateFolderHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")

	var req struct {
		Name   *string `json:"name"`
		Parent *string `json:"parent"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	var folder models.Folder
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&folder).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Folder not found"})
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 255 || strings.ContainsAny(name, `/\`) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid folder name"})
		}
		updates["name"] = name
	}
	if req.Parent != nil {
		if *req.Parent == folder.UUID {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Cannot move a folder into itself"})
		}
		parentID, err := resolveFolderID(identity.ID, *req.Parent)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Destination folder not found"})
		}
		if parentID != nil && isDescendant(identity.ID, folder.UUID, *parentID) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Cannot move a folder into its own subfolder"})
		}
		updates["parent_id"] = parentID // nil moves to root
	}
	if len(updates) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Nothing to update"})
	}

	if err := database.DB.Model(&folder).Updates(updates).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update folder"})
	}

	database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&folder)
	return c.JSON(folder)
}

// ListAllFoldersHandler returns every folder owned by the caller (flat), used
// by the client to build a destination picker for moves.
func ListAllFoldersHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	var folders []models.Folder
	if err := database.DB.Where("owner_id = ?", identity.ID).Order("name ASC").Find(&folders).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch folders"})
	}
	return c.JSON(folders)
}

func ThumbnailHandler(c *fiber.Ctx) error {
	identity := auth.Get(c)
	id := c.Params("uuid")
	cfg := appConfig
	thumbDir := filepath.Join(cfg.DataDir, "thumbs")
	cachePath := thumbPath(cfg, id)

	// 1. Resolve Original File (scoped to owner)
	var file models.File
	if err := database.DB.Where("uuid = ? AND owner_id = ?", id, identity.ID).First(&file).Error; err != nil {
		return c.Status(fiber.StatusNotFound).SendString("File not found")
	}

	// 2. Check Cache (only after confirming ownership)
	if _, err := os.Stat(cachePath); err == nil {
		return c.SendFile(cachePath)
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
	if err := imaging.Save(dst, cachePath); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save thumbnail")
	}

	return c.SendFile(cachePath)
}

// --- helpers ---

// parsePagination reads page/limit query params and clamps them to sane bounds.
func parsePagination(c *fiber.Ctx) (page, limit, offset int) {
	page, _ = strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ = strconv.Atoi(c.Query("limit", strconv.Itoa(defaultPageLimit)))
	if limit < 1 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	return page, limit, (page - 1) * limit
}

// resolveFolder loads a folder owned by the user. An empty UUID resolves to the
// root (nil folder, nil error).
func resolveFolder(ownerID, folderUUID string) (*models.Folder, error) {
	if folderUUID == "" {
		return nil, nil
	}
	var folder models.Folder
	if err := database.DB.Where("uuid = ? AND owner_id = ?", folderUUID, ownerID).First(&folder).Error; err != nil {
		return nil, err
	}
	return &folder, nil
}

// resolveFolderID validates a folder UUID and returns a pointer suitable for a
// File.FolderID / Folder.ParentID field (nil for the root).
func resolveFolderID(ownerID, folderUUID string) (*string, error) {
	folder, err := resolveFolder(ownerID, folderUUID)
	if err != nil {
		return nil, err
	}
	if folder == nil {
		return nil, nil
	}
	return &folder.UUID, nil
}

// buildBreadcrumbs walks up the parent chain from the current folder to the
// root, returning the path in root-first order.
func buildBreadcrumbs(ownerID string, current *models.Folder) []breadcrumb {
	crumbs := []breadcrumb{}
	node := current
	for i := 0; node != nil && i < 100; i++ {
		crumbs = append([]breadcrumb{{UUID: node.UUID, Name: node.Name}}, crumbs...)
		if node.ParentID == nil {
			break
		}
		var parent models.Folder
		if err := database.DB.Where("uuid = ? AND owner_id = ?", *node.ParentID, ownerID).First(&parent).Error; err != nil {
			break
		}
		node = &parent
	}
	return crumbs
}

// isDescendant reports whether candidate is the same as, or nested anywhere
// beneath, ancestor in the owner's folder tree. Used to reject cyclic moves.
func isDescendant(ownerID, ancestor, candidate string) bool {
	current := candidate
	for i := 0; current != "" && i < 1000; i++ {
		if current == ancestor {
			return true
		}
		var f models.Folder
		if err := database.DB.Select("parent_id").
			Where("uuid = ? AND owner_id = ?", current, ownerID).First(&f).Error; err != nil {
			return false
		}
		if f.ParentID == nil {
			return false
		}
		current = *f.ParentID
	}
	return false
}

// checkUploadAllowed enforces the disk-space guard and the per-user quota
// before a file of the given size is accepted. It returns (ok, status, message);
// when ok is false the caller should reply with status and message.
func checkUploadAllowed(ownerID string, size int64) (bool, int, string) {
	// Disk-space guard: the file must fit, leaving at least MinFreeBytes free.
	// If we can't read free space (e.g. unsupported FS), fail open.
	if avail, err := diskspace.Available(appConfig.DataDir); err == nil {
		if int64(avail) < size+appConfig.MinFreeBytes {
			return false, fiber.StatusInsufficientStorage, "Not enough free disk space"
		}
	}

	// Per-user quota (counts trashed files too, since they still occupy disk).
	if appConfig.UserQuota > 0 {
		var used int64
		database.DB.Unscoped().Model(&models.File{}).
			Where("owner_id = ?", ownerID).
			Select("COALESCE(SUM(size), 0)").Scan(&used)
		if used+size > appConfig.UserQuota {
			return false, fiber.StatusForbidden, "Storage quota exceeded"
		}
	}

	return true, 0, ""
}

// computeChecksum returns the hex-encoded SHA-256 of the file at path,
// streaming it so memory stays bounded regardless of file size.
func computeChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// thumbPath returns the on-disk cache path for a file's thumbnail.
func thumbPath(cfg *config.Config, fileUUID string) string {
	return filepath.Join(cfg.DataDir, "thumbs", fileUUID+".jpg")
}

// removeFileArtifacts deletes a file's payload and cached thumbnail from disk
// (best effort).
func removeFileArtifacts(cfg *config.Config, f models.File) {
	_ = os.Remove(f.DiskPath)
	_ = os.Remove(thumbPath(cfg, f.UUID))
}
