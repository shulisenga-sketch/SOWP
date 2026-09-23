package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sowp/internal/auth"
	"sowp/internal/config"
	"sowp/internal/database"
	"sowp/internal/handlers"
	"sowp/internal/storage"
)

func main() {
	cfg := config.Load()
	databaseSource := cfg.DatabasePath
	if cfg.DatabaseDriver == "mysql" {
		databaseSource = cfg.DatabaseDSN
	}
	db, err := database.Open(cfg.DatabaseDriver, databaseSource)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := database.Migrate(context.Background(), db, cfg.MigrationPath); err != nil {
		log.Fatal(err)
	}
	if err := database.SeedDefaultServices(context.Background(), db); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	sessions := auth.NewSessionStore(db, false)
	authHandler := handlers.NewAuthHandler(db, sessions)
	orderHandler := handlers.NewOrderHandler(db, sessions)
	commentHandler := handlers.NewCommentHandler(db, sessions)
	notificationHandler := handlers.NewNotificationHandler(db, sessions)
	adminHandler := handlers.NewAdminHandler(db, sessions)
	fileStore := storage.NewFileStore(cfg.StorageRoot, cfg.MaxUploadBytes)
	fileHandler := handlers.NewFileHandler(db, sessions, fileStore)
	paymentHandler := handlers.NewPaymentHandler(db, sessions, fileStore)
	mux.HandleFunc("POST /api/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/auth/login", authHandler.Login)
	mux.HandleFunc("POST /api/auth/logout", authHandler.Logout)
	mux.HandleFunc("GET /api/auth/me", authHandler.Me)
	mux.HandleFunc("GET /api/services", orderHandler.Services)
	mux.HandleFunc("GET /api/payment-methods", adminHandler.PaymentMethods)
	mux.HandleFunc("POST /api/orders", orderHandler.Create)
	mux.HandleFunc("GET /api/orders", orderHandler.ListMine)
	mux.HandleFunc("GET /api/orders/{orderID}/payment", paymentHandler.Details)
	mux.HandleFunc("GET /api/orders/{orderID}/comments", commentHandler.List)
	mux.HandleFunc("POST /api/orders/{orderID}/comments", commentHandler.Create)
	mux.HandleFunc("GET /api/notifications", notificationHandler.List)
	mux.HandleFunc("POST /api/notifications/{notificationID}/read", notificationHandler.MarkRead)
	mux.HandleFunc("GET /api/admin/orders", adminHandler.Orders)
	mux.HandleFunc("GET /api/admin/analytics", adminHandler.Analytics)
	mux.HandleFunc("GET /api/admin/payment-methods", adminHandler.AdminPaymentMethods)
	mux.HandleFunc("POST /api/admin/payment-methods", adminHandler.CreatePaymentMethod)
	mux.HandleFunc("PUT /api/admin/payment-methods/{methodID}", adminHandler.UpdatePaymentMethod)
	mux.HandleFunc("DELETE /api/admin/payment-methods/{methodID}", adminHandler.DeactivatePaymentMethod)
	mux.HandleFunc("GET /api/admin/orders/{orderID}/files", fileHandler.ListAdminOrderFiles)
	mux.HandleFunc("GET /api/admin/orders/{orderID}/files/{fileID}", fileHandler.PreviewAdminOrderFile)
	mux.HandleFunc("POST /api/orders/{orderID}/files", fileHandler.UploadSupportingDocument)
	mux.HandleFunc("POST /api/admin/orders/{orderID}/deliverable", fileHandler.UploadFinalDeliverable)
	mux.HandleFunc("GET /api/orders/{orderID}/deliverable", fileHandler.DownloadDeliverable)
	mux.HandleFunc("POST /api/admin/orders/{orderID}/payment-request", paymentHandler.CreateRequest)
	mux.HandleFunc("POST /api/orders/{orderID}/payment-proof", paymentHandler.UploadProof)
	mux.HandleFunc("POST /api/admin/orders/{orderID}/payment", paymentHandler.Decide)
	mux.HandleFunc("GET /", portalHandler)

	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           requestLogger(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
	}()

	log.Printf("SOWP server listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func healthHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
}

func portalHandler(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	http.ServeFile(writer, request, "web/index.html")
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(writer, request)
		log.Printf("%s %s %s", request.Method, request.URL.Path, time.Since(started))
	})
}
