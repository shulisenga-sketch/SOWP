package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"sowp/internal/auth"
	"sowp/internal/notifications"
	"sowp/internal/storage"
)

type PaymentHandler struct {
	db       *sql.DB
	sessions *auth.SessionStore
	store    *storage.FileStore
}

func NewPaymentHandler(db *sql.DB, sessions *auth.SessionStore, store *storage.FileStore) *PaymentHandler {
	return &PaymentHandler{db: db, sessions: sessions, store: store}
}

type paymentRequest struct {
	AmountCents     int64  `json:"amount_cents"`
	Instructions    string `json:"instructions"`
	PaymentMethodID int64  `json:"payment_method_id"`
}

type paymentDecision struct {
	Approved        bool   `json:"approved"`
	RejectionReason string `json:"rejection_reason"`
}

func (handler *PaymentHandler) CreateRequest(writer http.ResponseWriter, request *http.Request) {
	adminID, authorized := handler.requireAdmin(writer, request)
	if !authorized {
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}

	var input paymentRequest
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64*1024)).Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.Instructions = strings.TrimSpace(input.Instructions)
	if input.AmountCents <= 0 || input.Instructions == "" || len(input.Instructions) > 5000 {
		writeError(writer, http.StatusBadRequest, "positive amount_cents and payment instructions are required")
		return
	}
	var customerID int64
	if err := handler.db.QueryRowContext(request.Context(), `SELECT customer_id FROM orders WHERE id = ?`, orderID).Scan(&customerID); err != nil {
		if err == sql.ErrNoRows {
			writeError(writer, http.StatusNotFound, "order not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not validate order")
		return
	}
	var paymentMethodValue any
	if input.PaymentMethodID > 0 {
		var methodExists bool
		if err := handler.db.QueryRowContext(request.Context(), `
			SELECT EXISTS (SELECT 1 FROM payment_methods WHERE id = ? AND is_active = 1)`, input.PaymentMethodID,
		).Scan(&methodExists); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not validate payment method")
			return
		}
		if !methodExists {
			writeError(writer, http.StatusBadRequest, "selected payment method is not active")
			return
		}
		paymentMethodValue = input.PaymentMethodID
	}

	tx, err := handler.db.BeginTx(request.Context(), nil)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not start payment request")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(request.Context(), `
		INSERT INTO payments (order_id, amount_cents, instructions, requested_by, payment_method_id)
		VALUES (?, ?, ?, ?, ?)`, orderID, input.AmountCents, input.Instructions, adminID, paymentMethodValue); err != nil {
		writeError(writer, http.StatusConflict, "a payment request already exists for this order")
		return
	}
	if _, err := tx.ExecContext(request.Context(),
		`UPDATE orders SET status = 'payment_requested' WHERE id = ?`, orderID); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not update order status")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not save payment request")
		return
	}
	_ = notifications.Create(request.Context(), handler.db, customerID, orderID, "payment_request", "Payment requested", input.Instructions)
	writeJSON(writer, http.StatusCreated, map[string]any{
		"order_id":          orderID,
		"amount_cents":      input.AmountCents,
		"currency":          "TZS",
		"payment_method_id": input.PaymentMethodID,
		"status":            "requested",
	})
}

func (handler *PaymentHandler) Details(writer http.ResponseWriter, request *http.Request) {
	role, userID, authenticated := handler.sessions.Role(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}
	var amountCents, methodID int64
	var currency, instructions, status, customerReference string
	var methodName, accountName, accountNumber, methodInstructions sql.NullString
	query := `
		SELECT payments.amount_cents, payments.currency, payments.instructions, payments.status,
		       COALESCE(payments.customer_reference, ''),
		       COALESCE(payment_methods.id, 0), payment_methods.name, payment_methods.account_name,
		       payment_methods.account_number, payment_methods.instructions
		FROM payments
		JOIN orders ON orders.id = payments.order_id
		LEFT JOIN payment_methods ON payment_methods.id = payments.payment_method_id
		WHERE payments.order_id = ? AND (orders.customer_id = ? OR ? = 'admin')`
	if err := handler.db.QueryRowContext(request.Context(), query, orderID, userID, role).Scan(
		&amountCents, &currency, &instructions, &status, &customerReference, &methodID, &methodName,
		&accountName, &accountNumber, &methodInstructions); err != nil {
		if err == sql.ErrNoRows {
			writeError(writer, http.StatusNotFound, "payment request not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not load payment request")
		return
	}
	response := map[string]any{
		"order_id": orderID, "amount_cents": amountCents, "currency": currency,
		"instructions": instructions, "status": status, "customer_reference": customerReference,
	}
	if methodID > 0 {
		response["payment_method"] = map[string]any{
			"id": methodID, "name": methodName.String, "account_name": accountName.String,
			"account_number": accountNumber.String, "instructions": methodInstructions.String,
		}
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *PaymentHandler) UploadProof(writer http.ResponseWriter, request *http.Request) {
	userID, authenticated := handler.sessions.UserID(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}

	var paymentStatus string
	if err := handler.db.QueryRowContext(request.Context(), `
		SELECT payments.status
		FROM payments
		JOIN orders ON orders.id = payments.order_id
		WHERE payments.order_id = ? AND orders.customer_id = ?`, orderID, userID).Scan(&paymentStatus); err != nil {
		if err == sql.ErrNoRows {
			writeError(writer, http.StatusNotFound, "payment request not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not load payment request")
		return
	}
	if paymentStatus != "requested" && paymentStatus != "rejected" {
		writeError(writer, http.StatusConflict, "payment proof cannot be submitted in the current state")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, handler.store.MaxRequestBytes())
	if err := request.ParseMultipartForm(handler.store.MaxRequestBytes()); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid multipart upload or file is too large")
		return
	}
	customerReference := strings.TrimSpace(request.FormValue("customer_reference"))
	if len(customerReference) > 200 {
		writeError(writer, http.StatusBadRequest, "customer_reference must be at most 200 characters")
		return
	}
	fileHeader := firstFile(request, "file")
	if customerReference == "" && fileHeader == nil {
		writeError(writer, http.StatusBadRequest, "customer_reference or payment proof file is required")
		return
	}

	var saved storage.SavedFile
	var err error
	if fileHeader != nil {
		saved, err = handler.store.SavePaymentProof(fileHeader)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
	}

	tx, err := handler.db.BeginTx(request.Context(), nil)
	if err != nil {
		if fileHeader != nil {
			_ = handler.store.Delete(saved.StoredPath)
		}
		writeError(writer, http.StatusInternalServerError, "could not save payment proof")
		return
	}
	defer tx.Rollback()
	if fileHeader != nil {
		if _, err := tx.ExecContext(request.Context(), `
			INSERT INTO files (order_id, uploaded_by, file_kind, original_name,
			                   stored_name, stored_path, mime_type, file_size, sha256)
			VALUES (?, ?, 'payment_proof', ?, ?, ?, ?, ?, ?)`,
			orderID, userID, saved.OriginalName, saved.StoredName, saved.StoredPath,
			saved.MimeType, saved.Size, saved.SHA256); err != nil {
			_ = handler.store.Delete(saved.StoredPath)
			writeError(writer, http.StatusInternalServerError, "could not save payment proof metadata")
			return
		}
	}
	if _, err := tx.ExecContext(request.Context(), `
		UPDATE payments
		SET status = 'proof_submitted', customer_reference = ?, proof_submitted_at = CURRENT_TIMESTAMP
		WHERE order_id = ?`, customerReference, orderID); err != nil {
		if fileHeader != nil {
			_ = handler.store.Delete(saved.StoredPath)
		}
		writeError(writer, http.StatusInternalServerError, "could not update payment status")
		return
	}
	if _, err := tx.ExecContext(request.Context(),
		`UPDATE orders SET status = 'payment_verification' WHERE id = ?`, orderID); err != nil {
		if fileHeader != nil {
			_ = handler.store.Delete(saved.StoredPath)
		}
		writeError(writer, http.StatusInternalServerError, "could not update order status")
		return
	}
	if err := tx.Commit(); err != nil {
		if fileHeader != nil {
			_ = handler.store.Delete(saved.StoredPath)
		}
		writeError(writer, http.StatusInternalServerError, "could not save payment proof")
		return
	}
	_ = notifications.CreateForAdmins(request.Context(), handler.db, orderID, "payment_proof", "Payment proof submitted", "A customer submitted payment proof for review.")
	writeJSON(writer, http.StatusCreated, map[string]any{
		"order_id":           orderID,
		"customer_reference": customerReference,
		"status":             "proof_submitted",
	})
}

func (handler *PaymentHandler) Decide(writer http.ResponseWriter, request *http.Request) {
	adminID, authorized := handler.requireAdmin(writer, request)
	if !authorized {
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}

	var input paymentDecision
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64*1024)).Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !input.Approved && strings.TrimSpace(input.RejectionReason) == "" {
		writeError(writer, http.StatusBadRequest, "rejection_reason is required when payment is rejected")
		return
	}
	var customerID int64
	if err := handler.db.QueryRowContext(request.Context(), `SELECT customer_id FROM orders WHERE id = ?`, orderID).Scan(&customerID); err != nil {
		if err == sql.ErrNoRows {
			writeError(writer, http.StatusNotFound, "order not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not validate order")
		return
	}

	tx, err := handler.db.BeginTx(request.Context(), nil)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not verify payment")
		return
	}
	defer tx.Rollback()
	status := "rejected"
	orderStatus := "revision_needed"
	if input.Approved {
		status = "approved"
		orderStatus = "in_progress"
		var hasDeliverable bool
		if err := tx.QueryRowContext(request.Context(), `
			SELECT EXISTS (SELECT 1 FROM files WHERE order_id = ? AND file_kind = 'final_deliverable')`, orderID,
		).Scan(&hasDeliverable); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not inspect deliverable")
			return
		}
		if hasDeliverable {
			orderStatus = "completed"
		}
	}
	result, err := tx.ExecContext(request.Context(), `
		UPDATE payments
		SET status = ?, verified_by = ?, rejection_reason = ?, verified_at = CURRENT_TIMESTAMP
		WHERE order_id = ? AND status = 'proof_submitted'`,
		status, adminID, strings.TrimSpace(input.RejectionReason), orderID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not update payment")
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		writeError(writer, http.StatusConflict, "payment proof must be submitted before it can be verified")
		return
	}
	if _, err := tx.ExecContext(request.Context(),
		`UPDATE orders SET status = ?, completed_at = CASE WHEN ? = 'completed' THEN CURRENT_TIMESTAMP ELSE NULL END WHERE id = ?`, orderStatus, orderStatus, orderID); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not update order")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not verify payment")
		return
	}
	title := "Payment approved"
	body := "Your payment has been approved."
	if !input.Approved {
		title = "Payment rejected"
		body = strings.TrimSpace(input.RejectionReason)
	}
	_ = notifications.Create(request.Context(), handler.db, customerID, orderID, "payment_decision", title, body)
	writeJSON(writer, http.StatusOK, map[string]any{
		"order_id": orderID,
		"status":   status,
	})
}

func (handler *PaymentHandler) requireAdmin(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	role, userID, authenticated := handler.sessions.Role(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return 0, false
	}
	if role != "admin" {
		writeError(writer, http.StatusForbidden, "admin access is required")
		return 0, false
	}
	return userID, true
}

func parseOrderID(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	orderID, err := strconv.ParseInt(request.PathValue("orderID"), 10, 64)
	if err != nil || orderID < 1 {
		writeError(writer, http.StatusBadRequest, "invalid order id")
		return 0, false
	}
	return orderID, true
}
