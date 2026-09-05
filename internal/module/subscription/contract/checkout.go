package contract

type CheckoutInput struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
}
