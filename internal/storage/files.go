package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const DefaultMaxUploadBytes int64 = 15 * 1024 * 1024

type SavedFile struct {
	OriginalName string
	StoredName   string
	StoredPath   string
	MimeType     string
	Size         int64
	SHA256       string
}

type FileStore struct {
	root    string
	maxSize int64
}

func NewFileStore(root string, maxSize int64) *FileStore {
	if maxSize <= 0 {
		maxSize = DefaultMaxUploadBytes
	}
	return &FileStore{root: root, maxSize: maxSize}
}

func (store *FileStore) MaxRequestBytes() int64 {
	return store.maxSize*5 + 1024*1024
}

func (store *FileStore) SaveSupportingDocument(header *multipart.FileHeader) (SavedFile, error) {
	return store.Save(header, "supporting")
}

func (store *FileStore) SavePaymentProof(header *multipart.FileHeader) (SavedFile, error) {
	return store.Save(header, "payment-proofs")
}

func (store *FileStore) Save(header *multipart.FileHeader, category string) (SavedFile, error) {
	if header == nil || header.Size <= 0 {
		return SavedFile{}, fmt.Errorf("file is required")
	}
	if header.Size > store.maxSize {
		return SavedFile{}, fmt.Errorf("file exceeds maximum size")
	}

	originalName := filepath.Base(header.Filename)
	extension := strings.ToLower(filepath.Ext(originalName))
	if !allowedExtension(extension) {
		return SavedFile{}, fmt.Errorf("unsupported file type")
	}

	file, err := header.Open()
	if err != nil {
		return SavedFile{}, fmt.Errorf("open upload: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, 512))
	if err != nil {
		return SavedFile{}, fmt.Errorf("read upload header: %w", err)
	}
	if len(content) == 0 {
		return SavedFile{}, fmt.Errorf("file is empty")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return SavedFile{}, fmt.Errorf("rewind upload: %w", err)
	}

	mimeType := http.DetectContentType(content)
	if !allowedContent(extension, mimeType) {
		return SavedFile{}, fmt.Errorf("file content does not match its extension")
	}

	storedName, err := randomName(extension)
	if err != nil {
		return SavedFile{}, err
	}
	if category != "supporting" && category != "payment-proofs" && category != "deliverables" {
		return SavedFile{}, fmt.Errorf("invalid storage category")
	}
	directory := filepath.Join(store.root, category)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return SavedFile{}, fmt.Errorf("create upload directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return SavedFile{}, fmt.Errorf("create temporary upload: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		temporary.Close()
		os.Remove(temporaryPath)
	}

	hash := sha256.New()
	writer := io.MultiWriter(temporary, hash)
	size, err := io.Copy(writer, io.LimitReader(file, store.maxSize+1))
	if err != nil {
		cleanup()
		return SavedFile{}, fmt.Errorf("store upload: %w", err)
	}
	if size == 0 || size > store.maxSize {
		cleanup()
		return SavedFile{}, fmt.Errorf("file exceeds maximum size")
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return SavedFile{}, fmt.Errorf("close temporary upload: %w", err)
	}

	finalPath := filepath.Join(directory, storedName)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		os.Remove(temporaryPath)
		return SavedFile{}, fmt.Errorf("finalize upload: %w", err)
	}
	return SavedFile{
		OriginalName: originalName,
		StoredName:   storedName,
		StoredPath:   filepath.ToSlash(filepath.Join(category, storedName)),
		MimeType:     mimeType,
		Size:         size,
		SHA256:       hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func (store *FileStore) Delete(storedPath string) error {
	cleanPath := filepath.Clean(storedPath)
	fullPath := filepath.Join(store.root, cleanPath)
	rootPath, err := filepath.Abs(store.root)
	if err != nil {
		return err
	}
	absolutePath, err := filepath.Abs(fullPath)
	if err != nil {
		return err
	}
	if absolutePath != rootPath && !strings.HasPrefix(absolutePath, rootPath+string(os.PathSeparator)) {
		return fmt.Errorf("invalid stored path")
	}
	return os.Remove(absolutePath)
}

func (store *FileStore) Open(storedPath string) (*os.File, error) {
	cleanPath := filepath.Clean(storedPath)
	fullPath := filepath.Join(store.root, cleanPath)
	rootPath, err := filepath.Abs(store.root)
	if err != nil {
		return nil, err
	}
	absolutePath, err := filepath.Abs(fullPath)
	if err != nil {
		return nil, err
	}
	if absolutePath != rootPath && !strings.HasPrefix(absolutePath, rootPath+string(os.PathSeparator)) {
		return nil, fmt.Errorf("invalid stored path")
	}
	return os.Open(absolutePath)
}

func allowedExtension(extension string) bool {
	switch extension {
	case ".pdf", ".docx", ".png", ".jpg", ".jpeg":
		return true
	default:
		return false
	}
}

func allowedContent(extension, mimeType string) bool {
	switch extension {
	case ".pdf":
		return mimeType == "application/pdf"
	case ".docx":
		return mimeType == "application/zip" || mimeType == "application/octet-stream"
	case ".png":
		return mimeType == "image/png"
	case ".jpg", ".jpeg":
		return mimeType == "image/jpeg"
	default:
		return false
	}
}

func randomName(extension string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate stored filename: %w", err)
	}
	return hex.EncodeToString(bytes) + extension, nil
}
