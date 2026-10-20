package config

import (
	"errors"
	"os"
	"strings"
)

// PushConfig separates the public project identifier from worker credentials.
type PushConfig struct {
	AndroidPushEnabled      bool
	RuStorePushProjectID    string
	RuStorePushServiceToken string
}

func loadPush(worker bool) (PushConfig, error) {
	var cfg PushConfig
	var err error
	if cfg.AndroidPushEnabled, err = integrationBool("ANDROID_PUSH_ENABLED"); err != nil {
		return cfg, err
	}
	if !cfg.AndroidPushEnabled {
		return cfg, nil
	}
	cfg.RuStorePushProjectID = strings.TrimSpace(os.Getenv("RUSTORE_PUSH_PROJECT_ID"))
	if len(cfg.RuStorePushProjectID) < 1 || len(cfg.RuStorePushProjectID) > 128 || strings.Trim(cfg.RuStorePushProjectID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return cfg, errors.New("RUSTORE_PUSH_PROJECT_ID is required for Android push")
	}
	if worker {
		cfg.RuStorePushServiceToken = os.Getenv("RUSTORE_PUSH_SERVICE_TOKEN")
		if len(cfg.RuStorePushServiceToken) < 32 || len(cfg.RuStorePushServiceToken) > 8192 || strings.ContainsAny(cfg.RuStorePushServiceToken, " \t\r\n") {
			return cfg, errors.New("RUSTORE_PUSH_SERVICE_TOKEN is required by the Android push worker")
		}
	}
	return cfg, nil
}
