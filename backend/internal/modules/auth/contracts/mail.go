package contracts

// MailContent keeps the readable text alternative alongside optional HTML.
type MailContent struct {
	Text string
	HTML string
}
