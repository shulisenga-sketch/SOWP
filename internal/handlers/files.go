package handlers

import (
	"database/sql"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"

	"sowp/internal/auth"
	"sowp/internal/notifications"
	"sowp/internal/storage"
)

type FileHandler struct {
	db       *sql.DB
	sessions *auth.SessionStore
	store    *storage.FileStore
}

const maxSupportingDocuments = 5

type orderFileResponse struct {
	ID           int64  `json:"id"`
	OriginalName string `json:"original_name"`
	FileKind     string `json:"file_kind"`
	MimeType     string `json:"mime_type"`
	FileSize     int64  `json:"file_size"`
	CreatedAt    string `json:"created_at"`
}

func NewFileHandler(db *sql.DB, sessions *auth.SessionStore, store *storage.FileStore) *FileHandler {
	return &FileHandler{db: db, sessions: sessions, store: store}
}

func (handler *FileHandler) UploadSupportingDocument(writer http.ResponseWriter, request *http.Request) {
	userID, authenticated := handler.sessions.UserID(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}

	orderID, err := strconv.ParseInt(request.PathValue("orderID"), 10, 64)
	if err != nil || orderID < 1 {
		writeError(writer, http.StatusBadRequest, "invalid order id")
		return
	}

	var exists bool
	if err := handler.db.QueryRowContext(request.Context(),
		`SELECT EXISTS (SELECT 1 FROM orders WHERE id = ? AND customer_id = ?)`, orderID, userID,
	).Scan(&exists); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not validate order")
		return
	}
	if !exists {
		writeError(writer, http.StatusNotFound, "order not found")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, handler.store.MaxRequestBytes())
	if err := request.ParseMultipartForm(handler.store.MaxRequestBytes()); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid multipart upload or file is too large")
		return
	}
	fileHeaders := allFiles(request, "file")
	if len(fileHeaders) == 0 {
		writeError(writer, http.StatusBadRequest, "at least one file is required")
		return
	}
	if len(fileHeaders) > maxSupportingDocuments {
		writeError(writer, http.StatusBadRequest, "you can upload at most 5 supporting documents")
		return
	}

	savedFiles := make([]storage.SavedFile, 0, len(fileHeaders))
	cleanup := func() {
		for _, saved := range savedFiles {
			_ = handler.store.Delete(saved.StoredPath)
		}
	}
	for _, fileHeader := range fileHeaders {
		saved, err := handler.store.SaveSupportingDocument(fileHeader)
		if err != nil {
			cleanup()
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		savedFiles = append(savedFiles, saved)
	}

	tx, err := handler.db.BeginTx(request.Context(), nil)
	if err != nil {
		cleanup()
		writeError(writer, http.StatusInternalServerError, "could not save file metadata")
		return
	}
	defer tx.Rollback()
	for _, saved := range savedFiles {
		if _, err := tx.ExecContext(request.Context(), `
			INSERT INTO files (order_id, uploaded_by, file_kind, original_name,
			                   stored_name, stored_path, mime_type, file_size, sha256)
			VALUES (?, ?, 'supporting_document', ?, ?, ?, ?, ?, ?)`,
			orderID, userID, saved.OriginalName, saved.StoredName, saved.StoredPath,
			saved.MimeType, saved.Size, saved.SHA256); err != nil {
			cleanup()
			writeError(writer, http.StatusInternalServerError, "could not save file metadata")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		cleanup()
		writeError(writer, http.StatusInternalServerError, "could not save file metadata")
		return
	}

	uploadedFiles := make([]map[string]any, 0, len(savedFiles))
	for _, saved := range savedFiles {
		uploadedFiles = append(uploadedFiles, map[string]any{
			"original_name": saved.OriginalName,
			"size":          saved.Size,
			"mime_type":     saved.MimeType,
		})
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"files": uploadedFiles})
}

func (handler *FileHandler) UploadFinalDeliverable(writer http.ResponseWriter, request *http.Request) {
	role, adminID, authorized := handler.sessions.Role(request.Context(), request)
	if !authorized {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	if role != "admin" {
		writeError(writer, http.StatusForbidden, "admin access is required")
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}

	var exists bool
	if err := handler.db.QueryRowContext(request.Context(),
		`SELECT EXISTS (SELECT 1 FROM orders WHERE id = ?)`, orderID).Scan(&exists); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not validate order")
		return
	}
	if !exists {
		writeError(writer, http.StatusNotFound, "order not found")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, handler.store.MaxRequestBytes())
	if err := request.ParseMultipartForm(handler.store.MaxRequestBytes()); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid multipart upload or file is too large")
		return
	}
	saved, err := handler.store.Save(firstFile(request, "file"), "deliverables")
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := handler.db.BeginTx(request.Context(), nil)
	if err != nil {
		_ = handler.store.Delete(saved.StoredPath)
		writeError(writer, http.StatusInternalServerError, "could not save deliverable")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(request.Context(), `
		INSERT INTO files (order_id, uploaded_by, file_kind, original_name,
		                   stored_name, stored_path, mime_type, file_size, sha256)
		VALUES (?, ?, 'final_deliverable', ?, ?, ?, ?, ?, ?)`,
		orderID, adminID, saved.OriginalName, saved.StoredName, saved.StoredPath,
		saved.MimeType, saved.Size, saved.SHA256); err != nil {
		_ = handler.store.Delete(saved.StoredPath)
		writeError(writer, http.StatusInternalServerError, "could not save deliverable metadata")
		return
	}
	if _, err := tx.ExecContext(request.Context(), `
		UPDATE orders
		SET status = CASE WHEN EXISTS (
			SELECT 1 FROM payments WHERE payments.order_id = orders.id AND payments.status = 'approved'
		) THEN 'completed' ELSE 'in_progress' END,
		completed_at = CASE WHEN EXISTS (
			SELECT 1 FROM payments WHERE payments.order_id = orders.id AND payments.status = 'approved'
		) THEN CURRENT_TIMESTAMP ELSE NULL END
		WHERE id = ?`, orderID); err != nil {
		_ = handler.store.Delete(saved.StoredPath)
		writeError(writer, http.StatusInternalServerError, "could not update order status")
		return
	}
	if err := tx.Commit(); err != nil {
		_ = handler.store.Delete(saved.StoredPath)
		writeError(writer, http.StatusInternalServerError, "could not save deliverable")
		return
	}
	var orderStatus string
	if err := handler.db.QueryRowContext(request.Context(), `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&orderStatus); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read order status")
		return
	}
	var customerID int64
	_ = handler.db.QueryRowContext(request.Context(), `SELECT customer_id FROM orders WHERE id = ?`, orderID).Scan(&customerID)
	notificationTitle := "Deliverable uploaded"
	notificationBody := "A deliverable has been uploaded for your order."
	if orderStatus == "completed" {
		notificationTitle = "Work completed"
		notificationBody = "Your completed work is ready to download."
	}
	_ = notifications.Create(request.Context(), handler.db, customerID, orderID, "deliverable", notificationTitle, notificationBody)
	writeJSON(writer, http.StatusCreated, map[string]any{
		"order_id":      orderID,
		"original_name": saved.OriginalName,
		"status":        orderStatus,
	})
}

func (handler *FileHandler) DownloadDeliverable(writer http.ResponseWriter, request *http.Request) {
	userID, authenticated := handler.sessions.UserID(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}

	var originalName, storedPath, mimeType string
	err := handler.db.QueryRowContext(request.Context(), `
		SELECT files.original_name, files.stored_path, files.mime_type
		FROM files
		JOIN orders ON orders.id = files.order_id
		JOIN payments ON payments.order_id = orders.id
		WHERE files.order_id = ?
		  AND files.file_kind = 'final_deliverable'
		  AND orders.customer_id = ?
		  AND payments.status = 'approved'
		ORDER BY files.created_at DESC
		LIMIT 1`, orderID, userID).Scan(&originalName, &storedPath, &mimeType)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(writer, http.StatusNotFound, "deliverable is not available")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not load deliverable")
		return
	}

	file, err := handler.store.Open(storedPath)
	if err != nil {
		writeError(writer, http.StatusNotFound, "deliverable file is missing")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not inspect deliverable")
		return
	}
	writer.Header().Set("Content-Type", mimeType)
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", originalName))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, originalName, info.ModTime(), file)
}

func (handler *FileHandler) ListAdminOrderFiles(writer http.ResponseWriter, request *http.Request) {
	if !handler.requireAdmin(writer, request) {
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}
	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT id, original_name, file_kind, mime_type, file_size, created_at
		FROM files WHERE order_id = ? ORDER BY created_at DESC, id DESC`, orderID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load order files")
		return
	}
	defer rows.Close()
	files := make([]orderFileResponse, 0)
	for rows.Next() {
		var file orderFileResponse
		if err := rows.Scan(&file.ID, &file.OriginalName, &file.FileKind, &file.MimeType, &file.FileSize, &file.CreatedAt); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not read order files")
			return
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read order files")
		return
	}
	writeJSON(writer, http.StatusOK, files)
}

func (handler *FileHandler) PreviewAdminOrderFile(writer http.ResponseWriter, request *http.Request) {
	if !handler.requireAdmin(writer, request) {
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}
	fileID, err := strconv.ParseInt(request.PathValue("fileID"), 10, 64)
	if err != nil || fileID < 1 {
		writeError(writer, http.StatusBadRequest, "invalid file id")
		return
	}
	var originalName, storedPath, mimeType string
	if err := handler.db.QueryRowContext(request.Context(), `
		SELECT original_name, stored_path, mime_type
		FROM files WHERE id = ? AND order_id = ?`, fileID, orderID).Scan(&originalName, &storedPath, &mimeType); err != nil {
		if err == sql.ErrNoRows {
			writeError(writer, http.StatusNotFound, "file not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not load file")
		return
	}
	file, err := handler.store.Open(storedPath)
	if err != nil {
		writeError(writer, http.StatusNotFound, "stored file is missing")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not inspect file")
		return
	}
	disposition := "attachment"
	if mimeType == "application/pdf" || mimeType == "image/png" || mimeType == "image/jpeg" {
		disposition = "inline"
	}
	writer.Header().Set("Content-Type", mimeType)
	writer.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, originalName))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(writer, request, originalName, info.ModTime(), file)
}

func (handler *FileHandler) requireAdmin(writer http.ResponseWriter, request *http.Request) bool {
	role, _, authenticated := handler.sessions.Role(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return false
	}
	if role != "admin" {
		writeError(writer, http.StatusForbidden, "admin access is required")
		return false
	}
	return true
}

func firstFile(request *http.Request, field string) *multipart.FileHeader {
	files := allFiles(request, field)
	if len(files) == 0 {
		return nil
	}
	return files[0]
}

func allFiles(request *http.Request, field string) []*multipart.FileHeader {
	if request.MultipartForm == nil {
		return nil
	}
	return request.MultipartForm.File[field]
}
