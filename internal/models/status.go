package models

type OrderStatus string

const (
	OrderPending             OrderStatus = "pending"
	OrderInProgress          OrderStatus = "in_progress"
	OrderPaymentRequested    OrderStatus = "payment_requested"
	OrderPaymentVerification OrderStatus = "payment_verification"
	OrderCompleted           OrderStatus = "completed"
	OrderRevisionNeeded      OrderStatus = "revision_needed"
	OrderCancelled           OrderStatus = "cancelled"
)

type PaymentStatus string

const (
	PaymentRequested      PaymentStatus = "requested"
	PaymentProofSubmitted PaymentStatus = "proof_submitted"
	PaymentApproved       PaymentStatus = "approved"
	PaymentRejected       PaymentStatus = "rejected"
)
