package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"sowp/internal/auth"
)

type AdminHandler struct {
	db       *sql.DB
	sessions *auth.SessionStore
}

func NewAdminHandler(db *sql.DB, sessions *auth.SessionStore) *AdminHandler {
	return &AdminHandler{db: db, sessions: sessions}
}

type adminOrderResponse struct {
	ID             int64  `json:"id"`
	OrderNumber    string `json:"order_number"`
	CustomerID     int64  `json:"customer_id"`
	Customer       string `json:"customer"`
	Service        string `json:"service"`
	Status         string `json:"status"`
	Description    string `json:"description"`
	HasDeliverable bool   `json:"has_deliverable"`
	CompletedAt    string `json:"completed_at,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type paymentMethodResponse struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	AccountName   string `json:"account_name"`
	AccountNumber string `json:"account_number"`
	Instructions  string `json:"instructions,omitempty"`
	IsActive      bool   `json:"is_active"`
}

type paymentMethodInput struct {
	Name          string `json:"name"`
	AccountName   string `json:"account_name"`
	AccountNumber string `json:"account_number"`
	Instructions  string `json:"instructions"`
}

func (handler *AdminHandler) PaymentMethods(writer http.ResponseWriter, request *http.Request) {
	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT id, name, account_name, account_number, COALESCE(instructions, ''), is_active
		FROM payment_methods
		WHERE is_active = 1
		ORDER BY name`)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load payment methods")
		return
	}
	defer rows.Close()
	handler.writePaymentMethods(writer, rows)
}

func (handler *AdminHandler) AdminPaymentMethods(writer http.ResponseWriter, request *http.Request) {
	if !handler.requireAdmin(writer, request) {
		return
	}
	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT id, name, account_name, account_number, COALESCE(instructions, ''), is_active
		FROM payment_methods ORDER BY is_active DESC, name`)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load payment methods")
		return
	}
	defer rows.Close()
	handler.writePaymentMethods(writer, rows)
}

func (handler *AdminHandler) CreatePaymentMethod(writer http.ResponseWriter, request *http.Request) {
	adminID, ok := handler.adminID(writer, request)
	if !ok {
		return
	}
	input, valid := decodePaymentMethod(writer, request)
	if !valid {
		return
	}
	result, err := handler.db.ExecContext(request.Context(), `
		INSERT INTO payment_methods (name, account_name, account_number, instructions, created_by)
		VALUES (?, ?, ?, ?, ?)`, input.Name, input.AccountName, input.AccountNumber, input.Instructions, adminID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not create payment method")
		return
	}
	id, _ := result.LastInsertId()
	writeJSON(writer, http.StatusCreated, map[string]any{"id": id, "message": "payment method created"})
}

func (handler *AdminHandler) UpdatePaymentMethod(writer http.ResponseWriter, request *http.Request) {
	if _, ok := handler.adminID(writer, request); !ok {
		return
	}
	methodID, ok := parsePaymentMethodID(writer, request)
	if !ok {
		return
	}
	input, valid := decodePaymentMethod(writer, request)
	if !valid {
		return
	}
	result, err := handler.db.ExecContext(request.Context(), `
		UPDATE payment_methods
		SET name = ?, account_name = ?, account_number = ?, instructions = ?
		WHERE id = ?`, input.Name, input.AccountName, input.AccountNumber, input.Instructions, methodID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not update payment method")
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		writeError(writer, http.StatusNotFound, "payment method not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"id": methodID, "message": "payment method updated"})
}

func (handler *AdminHandler) DeactivatePaymentMethod(writer http.ResponseWriter, request *http.Request) {
	if _, ok := handler.adminID(writer, request); !ok {
		return
	}
	methodID, ok := parsePaymentMethodID(writer, request)
	if !ok {
		return
	}
	result, err := handler.db.ExecContext(request.Context(),
		`UPDATE payment_methods SET is_active = 0 WHERE id = ?`, methodID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not deactivate payment method")
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		writeError(writer, http.StatusNotFound, "payment method not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"id": methodID, "message": "payment method deactivated"})
}

func (handler *AdminHandler) writePaymentMethods(writer http.ResponseWriter, rows *sql.Rows) {
	methods := make([]paymentMethodResponse, 0)
	for rows.Next() {
		var method paymentMethodResponse
		var active int
		if err := rows.Scan(&method.ID, &method.Name, &method.AccountName, &method.AccountNumber, &method.Instructions, &active); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not read payment methods")
			return
		}
		method.IsActive = active == 1
		methods = append(methods, method)
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read payment methods")
		return
	}
	writeJSON(writer, http.StatusOK, methods)
}

func decodePaymentMethod(writer http.ResponseWriter, request *http.Request) (paymentMethodInput, bool) {
	var input paymentMethodInput
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64*1024)).Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	input.AccountName = strings.TrimSpace(input.AccountName)
	input.AccountNumber = strings.TrimSpace(input.AccountNumber)
	input.Instructions = strings.TrimSpace(input.Instructions)
	if input.Name == "" || input.AccountName == "" || input.AccountNumber == "" {
		writeError(writer, http.StatusBadRequest, "name, account_name, and account_number are required")
		return input, false
	}
	return input, true
}

func (handler *AdminHandler) adminID(writer http.ResponseWriter, request *http.Request) (int64, bool) {
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

func parsePaymentMethodID(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	methodID, err := strconv.ParseInt(request.PathValue("methodID"), 10, 64)
	if err != nil || methodID < 1 {
		writeError(writer, http.StatusBadRequest, "invalid payment method id")
		return 0, false
	}
	return methodID, true
}

func (handler *AdminHandler) Orders(writer http.ResponseWriter, request *http.Request) {
	if !handler.requireAdmin(writer, request) {
		return
	}
	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT orders.id, orders.order_number, orders.customer_id, users.full_name,
		       services.name, orders.status, orders.description,
		       EXISTS (SELECT 1 FROM files WHERE files.order_id = orders.id AND files.file_kind = 'final_deliverable'),
		       COALESCE(orders.completed_at, ''), orders.created_at
		FROM orders
		JOIN users ON users.id = orders.customer_id
		JOIN services ON services.id = orders.service_id
		ORDER BY orders.created_at DESC, orders.id DESC`)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load admin orders")
		return
	}
	defer rows.Close()

	orders := make([]adminOrderResponse, 0)
	for rows.Next() {
		var order adminOrderResponse
		var deliverable int
		if err := rows.Scan(&order.ID, &order.OrderNumber, &order.CustomerID, &order.Customer,
			&order.Service, &order.Status, &order.Description, &deliverable,
			&order.CompletedAt, &order.CreatedAt); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not read admin orders")
			return
		}
		order.HasDeliverable = deliverable == 1
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read admin orders")
		return
	}
	writeJSON(writer, http.StatusOK, orders)
}

func (handler *AdminHandler) Analytics(writer http.ResponseWriter, request *http.Request) {
	if !handler.requireAdmin(writer, request) {
		return
	}
	var totalOrders, completedOrders, activeOrders, pendingOrders, awaitingVerification, verifiedPayments int64
	var revenueCents int64
	if err := handler.db.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM orders`).Scan(&totalOrders); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not calculate analytics")
		return
	}
	if err := handler.db.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM orders WHERE status = 'completed'`).Scan(&completedOrders); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not calculate analytics")
		return
	}
	if err := handler.db.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM orders WHERE status NOT IN ('completed', 'cancelled')`).Scan(&activeOrders); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not calculate analytics")
		return
	}
	if err := handler.db.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM orders WHERE status = 'pending'`).Scan(&pendingOrders); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not calculate analytics")
		return
	}
	if err := handler.db.QueryRowContext(request.Context(), `SELECT COUNT(*) FROM orders WHERE status = 'payment_verification'`).Scan(&awaitingVerification); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not calculate analytics")
		return
	}
	if err := handler.db.QueryRowContext(request.Context(), `
		SELECT COUNT(*), COALESCE(SUM(amount_cents), 0)
		FROM payments WHERE status = 'approved'`).Scan(&verifiedPayments, &revenueCents); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not calculate revenue")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"total_orders":          totalOrders,
		"completed_orders":      completedOrders,
		"active_orders":         activeOrders,
		"pending_orders":        pendingOrders,
		"awaiting_verification": awaitingVerification,
		"verified_payments":     verifiedPayments,
		"revenue_cents":         revenueCents,
		"currency":              "TZS",
	})
}

func (handler *AdminHandler) requireAdmin(writer http.ResponseWriter, request *http.Request) bool {
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
