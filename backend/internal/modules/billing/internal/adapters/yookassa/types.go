package yookassa

type paymentResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Paid   bool   `json:"paid"`
	Test   *bool  `json:"test"`
	Amount struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`
	Recipient struct {
		AccountID string `json:"account_id"`
	} `json:"recipient"`
	Metadata struct {
		OrderID string `json:"order_id"`
	} `json:"metadata"`
	Confirmation struct {
		Type            string `json:"type"`
		ConfirmationURL string `json:"confirmation_url"`
	} `json:"confirmation"`
	PaymentMethod struct {
		Type string `json:"type"`
	} `json:"payment_method"`
}
