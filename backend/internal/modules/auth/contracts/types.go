package contracts

// Principal is the authenticated identity passed between modules.
type Principal struct {
	AccountID string `json:"accountId"`
	SessionID string `json:"sessionId"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}
