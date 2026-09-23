package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sowp/internal/auth"
)

type OrderHandler struct {
	db       *sql.DB
	sessions *auth.SessionStore
}

func NewOrderHandler(db *sql.DB, sessions *auth.SessionStore) *OrderHandler {
	return &OrderHandler{db: db, sessions: sessions}
}

type serviceResponse struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Description  string   `json:"description,omitempty"`
	Requirements []string `json:"requirements"`
}

type createOrderRequest struct {
	ServiceID   int64  `json:"service_id"`
	Description string `json:"description"`
}

type orderResponse struct {
	ID             int64  `json:"id"`
	OrderNumber    string `json:"order_number"`
	Service        string `json:"service"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	PaymentStatus  string `json:"payment_status,omitempty"`
	HasDeliverable bool   `json:"has_deliverable"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func (handler *OrderHandler) Services(writer http.ResponseWriter, request *http.Request) {
	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT id, name, slug, COALESCE(description, '')
		FROM services
		WHERE is_active = 1
		ORDER BY name`)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load services")
		return
	}
	defer rows.Close()

	services := make([]serviceResponse, 0)
	for rows.Next() {
		var service serviceResponse
		if err := rows.Scan(&service.ID, &service.Name, &service.Slug, &service.Description); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not read services")
			return
		}
		requirementRows, err := handler.db.QueryContext(request.Context(), `
			SELECT label
			FROM service_requirements
			WHERE service_id = ?
			ORDER BY sort_order, id`, service.ID)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "could not load service requirements")
			return
		}
		service.Requirements = make([]string, 0)
		for requirementRows.Next() {
			var requirement string
			if err := requirementRows.Scan(&requirement); err != nil {
				requirementRows.Close()
				writeError(writer, http.StatusInternalServerError, "could not read service requirements")
				return
			}
			service.Requirements = append(service.Requirements, requirement)
		}
		if err := requirementRows.Err(); err != nil {
			requirementRows.Close()
			writeError(writer, http.StatusInternalServerError, "could not read service requirements")
			return
		}
		requirementRows.Close()
		services = append(services, service)
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read services")
		return
	}
	writeJSON(writer, http.StatusOK, services)
}

func (handler *OrderHandler) Create(writer http.ResponseWriter, request *http.Request) {
	userID, authenticated := handler.sessions.UserID(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}

	var input createOrderRequest
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64*1024))
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.Description = strings.TrimSpace(input.Description)
	if input.ServiceID < 1 || input.Description == "" || len(input.Description) > 10000 {
		writeError(writer, http.StatusBadRequest, "service_id and description are required; description must be at most 10000 characters")
		return
	}

	var serviceExists bool
	if err := handler.db.QueryRowContext(request.Context(),
		`SELECT EXISTS (SELECT 1 FROM services WHERE id = ? AND is_active = 1)`, input.ServiceID,
	).Scan(&serviceExists); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not validate service")
		return
	}
	if !serviceExists {
		writeError(writer, http.StatusBadRequest, "selected service is not available")
		return
	}

	orderNumber := fmt.Sprintf("SOWP-%s-%d", time.Now().UTC().Format("20060102150405"), time.Now().UTC().UnixNano()%1000000)
	result, err := handler.db.ExecContext(request.Context(), `
		INSERT INTO orders (order_number, customer_id, service_id, description)
		VALUES (?, ?, ?, ?)`, orderNumber, userID, input.ServiceID, input.Description)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not create order")
		return
	}
	orderID, _ := result.LastInsertId()
	writeJSON(writer, http.StatusCreated, map[string]any{
		"id":           orderID,
		"order_number": orderNumber,
		"status":       "pending",
	})
}

func (handler *OrderHandler) ListMine(writer http.ResponseWriter, request *http.Request) {
	userID, authenticated := handler.sessions.UserID(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}

	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT orders.id, orders.order_number, services.name, orders.description,
		       orders.status, COALESCE(payments.status, ''),
		       EXISTS (SELECT 1 FROM files WHERE files.order_id = orders.id AND files.file_kind = 'final_deliverable'),
		       orders.created_at, orders.updated_at
		FROM orders
		JOIN services ON services.id = orders.service_id
		LEFT JOIN payments ON payments.order_id = orders.id
		WHERE orders.customer_id = ?
		ORDER BY orders.created_at DESC`, userID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load orders")
		return
	}
	defer rows.Close()

	orders := make([]orderResponse, 0)
	for rows.Next() {
		var order orderResponse
		var deliverable int
		if err := rows.Scan(&order.ID, &order.OrderNumber, &order.Service, &order.Description,
			&order.Status, &order.PaymentStatus, &deliverable, &order.CreatedAt, &order.UpdatedAt); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not read orders")
			return
		}
		order.HasDeliverable = deliverable == 1
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read orders")
		return
	}
	writeJSON(writer, http.StatusOK, orders)
}
