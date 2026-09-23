package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"sowp/internal/auth"
	"sowp/internal/database"
	"sowp/internal/storage"
)

func TestManualPaymentWorkflow(t *testing.T) {
	db, err := database.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(context.Background(), db, filepath.Join("..", "..", "migrations", "001_initial.sql")); err != nil {
		t.Fatal(err)
	}
	if err := database.SeedDefaultServices(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	passwordHash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	customerID := insertTestUser(t, db, "customer@example.com", "customer", passwordHash)
	adminID := insertTestUser(t, db, "admin@example.com", "admin", passwordHash)
	var serviceID int64
	if err := db.QueryRow(`SELECT id FROM services ORDER BY id LIMIT 1`).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO orders (order_number, customer_id, service_id, description) VALUES (?, ?, ?, ?)`, "TEST-ORDER-1", customerID, serviceID, "Test order")
	if err != nil {
		t.Fatal(err)
	}
	orderID, _ := result.LastInsertId()

	sessions := auth.NewSessionStore(db, false)
	store := storage.NewFileStore(filepath.Join(t.TempDir(), "storage"), 15*1024*1024)
	handler := NewPaymentHandler(db, sessions, store)
	adminToken := createTestSession(t, sessions, adminID)
	customerToken := createTestSession(t, sessions, customerID)

	requestBody := bytes.NewBufferString(`{"amount_cents":25000,"instructions":"Lipa kupitia namba ya biashara 123456."}`)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/orders/1/payment-request", requestBody)
	request.SetPathValue("orderID", strconv.FormatInt(orderID, 10))
	request.AddCookie(&http.Cookie{Name: "sowp_session", Value: adminToken})
	recorder := httptest.NewRecorder()
	handler.CreateRequest(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create payment request status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var uploadBody bytes.Buffer
	writer := multipart.NewWriter(&uploadBody)
	part, err := writer.CreateFormFile("file", "payment.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "%PDF-1.4\n%%EOF")
	if err := writer.WriteField("customer_reference", "M-PESA-ABC123XYZ"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	proofRequest := httptest.NewRequest(http.MethodPost, "/api/orders/1/payment-proof", &uploadBody)
	proofRequest.SetPathValue("orderID", strconv.FormatInt(orderID, 10))
	proofRequest.Header.Set("Content-Type", writer.FormDataContentType())
	proofRequest.AddCookie(&http.Cookie{Name: "sowp_session", Value: customerToken})
	proofRecorder := httptest.NewRecorder()
	handler.UploadProof(proofRecorder, proofRequest)
	if proofRecorder.Code != http.StatusCreated {
		t.Fatalf("upload proof status = %d, body = %s", proofRecorder.Code, proofRecorder.Body.String())
	}

	decisionRequest := httptest.NewRequest(http.MethodPost, "/api/admin/orders/1/payment", bytes.NewBufferString(`{"approved":true}`))
	decisionRequest.SetPathValue("orderID", strconv.FormatInt(orderID, 10))
	decisionRequest.AddCookie(&http.Cookie{Name: "sowp_session", Value: adminToken})
	decisionRecorder := httptest.NewRecorder()
	handler.Decide(decisionRecorder, decisionRequest)
	if decisionRecorder.Code != http.StatusOK {
		t.Fatalf("decide payment status = %d, body = %s", decisionRecorder.Code, decisionRecorder.Body.String())
	}

	var paymentStatus, orderStatus, customerReference string
	if err := db.QueryRow(`SELECT payments.status, orders.status, payments.customer_reference FROM payments JOIN orders ON orders.id = payments.order_id WHERE payments.order_id = ?`, orderID).Scan(&paymentStatus, &orderStatus, &customerReference); err != nil {
		t.Fatal(err)
	}
	if paymentStatus != "approved" || orderStatus != "in_progress" {
		t.Fatalf("unexpected final states: payment=%s order=%s", paymentStatus, orderStatus)
	}
	if customerReference != "M-PESA-ABC123XYZ" {
		t.Fatalf("customer reference = %q, want M-PESA-ABC123XYZ", customerReference)
	}

	var deliverableBody bytes.Buffer
	deliverableWriter := multipart.NewWriter(&deliverableBody)
	deliverablePart, err := deliverableWriter.CreateFormFile("file", "final-report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(deliverablePart, "%PDF-1.4\nfinal report\n%%EOF")
	if err := deliverableWriter.Close(); err != nil {
		t.Fatal(err)
	}
	deliverableRequest := httptest.NewRequest(http.MethodPost, "/api/admin/orders/1/deliverable", &deliverableBody)
	deliverableRequest.SetPathValue("orderID", strconv.FormatInt(orderID, 10))
	deliverableRequest.Header.Set("Content-Type", deliverableWriter.FormDataContentType())
	deliverableRequest.AddCookie(&http.Cookie{Name: "sowp_session", Value: adminToken})
	deliverableRecorder := httptest.NewRecorder()
	fileHandler := NewFileHandler(db, sessions, store)
	fileHandler.UploadFinalDeliverable(deliverableRecorder, deliverableRequest)
	if deliverableRecorder.Code != http.StatusCreated {
		t.Fatalf("upload deliverable status = %d, body = %s", deliverableRecorder.Code, deliverableRecorder.Body.String())
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/orders/1/deliverable", nil)
	downloadRequest.SetPathValue("orderID", strconv.FormatInt(orderID, 10))
	downloadRequest.AddCookie(&http.Cookie{Name: "sowp_session", Value: customerToken})
	downloadRecorder := httptest.NewRecorder()
	fileHandler.DownloadDeliverable(downloadRecorder, downloadRequest)
	if downloadRecorder.Code != http.StatusOK || !bytes.Contains(downloadRecorder.Body.Bytes(), []byte("final report")) {
		t.Fatalf("download deliverable status = %d, body = %s", downloadRecorder.Code, downloadRecorder.Body.String())
	}

	if err := db.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if orderStatus != "completed" {
		t.Fatalf("order status after deliverable = %s, want completed", orderStatus)
	}
	var fileCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM files WHERE order_id = ? AND file_kind = 'payment_proof'`, orderID).Scan(&fileCount); err != nil {
		t.Fatal(err)
	}
	if fileCount != 1 {
		t.Fatalf("payment proof file count = %d, want 1", fileCount)
	}
}

func insertTestUser(t *testing.T, db *sql.DB, email, role, passwordHash string) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO users (full_name, email, password_hash, role) VALUES (?, ?, ?, ?)`, role, email, passwordHash, role)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func createTestSession(t *testing.T, sessions *auth.SessionStore, userID int64) string {
	t.Helper()
	token, _, err := sessions.Create(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
