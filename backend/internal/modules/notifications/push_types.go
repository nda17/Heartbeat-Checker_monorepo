package notifications

import "time"

type PushInstallationStatus struct {
	ID                  string     `json:"id"`
	Provider            string     `json:"provider"`
	DeviceLabel         string     `json:"deviceLabel"`
	Status              string     `json:"status"`
	BindingGeneration   int64      `json:"bindingGeneration"`
	RegistrationVersion int64      `json:"registrationVersion"`
	LeaseExpiresAt      *time.Time `json:"leaseExpiresAt"`
	LastPairingID       *string    `json:"lastPairingId"`
	LastSeenAt          time.Time  `json:"lastSeenAt"`
}

type PushPairing struct {
	ID               string    `json:"id"`
	ExpiresAt        time.Time `json:"expiresAt"`
	VerificationCode string    `json:"verificationCode"`
	ConfirmationURL  string    `json:"confirmationUrl"`
	Status           string    `json:"status"`
	DeviceLabel      string    `json:"deviceLabel,omitempty"`
	AppVersion       string    `json:"appVersion,omitempty"`
}

type PushPairingInstallation struct {
	ID         string `json:"id"`
	Credential string `json:"credential,omitempty"`
}

type PushPairingResult struct {
	Pairing      PushPairing             `json:"pairing"`
	Installation PushPairingInstallation `json:"installation"`
}

type PushRoute struct {
	InstallationID string `json:"installationId"`
	Enabled        bool   `json:"enabled"`
	Ready          bool   `json:"ready"`
	Version        int64  `json:"version"`
}

type PutPushRoute struct {
	Enabled bool  `json:"enabled"`
	Version int64 `json:"version"`
}

type PushDeliveryContent struct {
	DeliveryID string    `json:"deliveryId"`
	Kind       string    `json:"kind"`
	Sequence   int64     `json:"sequence"`
	MonitorID  string    `json:"monitorId"`
	IncidentID string    `json:"incidentId"`
	OccurredAt time.Time `json:"occurredAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
}

type PushPairingInput struct {
	CodeChallenge string `json:"codeChallenge"`
	DeviceLabel   string `json:"deviceLabel"`
	AppVersion    string `json:"appVersion"`
}

type PushExchangeInput struct {
	CodeVerifier string `json:"codeVerifier"`
	Provider     string `json:"provider"`
	Token        string `json:"token"`
	Permission   string `json:"permission"`
	AppVersion   string `json:"appVersion"`
}

type PushRegistrationInput struct {
	RegistrationVersion int64   `json:"registrationVersion"`
	Token               *string `json:"token,omitempty"`
	Permission          string  `json:"permission"`
}
