package config

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

// Public project identifiers are shared with API; sending credentials load only
// in the worker and never enter the frontend or Android build.
type PushConfig struct {
	AndroidPushEnabled      bool
	RuStorePushProjectID    string
	RuStorePushServiceToken string
	FCMPushEnabled          bool
	FCMProjectID            string
	FCMServiceAccountBase64 string
	HuaweiPushEnabled       bool
	HuaweiProjectID         string
	HuaweiClientID          string
	HuaweiClientSecret      string
}

func (c PushConfig) PushProviders() []string {
	result := []string{}
	if !c.AndroidPushEnabled {
		return result
	}
	if c.FCMPushEnabled {
		result = append(result, "fcm")
	}
	if c.HuaweiPushEnabled {
		result = append(result, "huawei")
	}
	if c.RuStorePushProjectID != "" {
		result = append(result, "rustore")
	}
	return result
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
	if cfg.FCMPushEnabled, err = integrationBool("FCM_PUSH_ENABLED"); err != nil {
		return cfg, err
	}
	if cfg.HuaweiPushEnabled, err = integrationBool("HUAWEI_PUSH_ENABLED"); err != nil {
		return cfg, err
	}
	if cfg.FCMPushEnabled {
		cfg.FCMProjectID = strings.TrimSpace(os.Getenv("FCM_PROJECT_ID"))
		if len(cfg.FCMProjectID) < 4 || len(cfg.FCMProjectID) > 128 || strings.Trim(cfg.FCMProjectID, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return cfg, errors.New("invalid FCM_PROJECT_ID")
		}
	}
	if cfg.HuaweiPushEnabled {
		cfg.HuaweiProjectID = strings.TrimSpace(os.Getenv("HUAWEI_PROJECT_ID"))
		if cfg.HuaweiProjectID == "" || len(cfg.HuaweiProjectID) > 32 || strings.Trim(cfg.HuaweiProjectID, "0123456789") != "" {
			return cfg, errors.New("invalid HUAWEI_PROJECT_ID")
		}
	}
	if !worker {
		return cfg, nil
	}
	cfg.RuStorePushServiceToken = os.Getenv("RUSTORE_PUSH_SERVICE_TOKEN")
	if len(cfg.RuStorePushServiceToken) < 32 || len(cfg.RuStorePushServiceToken) > 8192 || strings.ContainsAny(cfg.RuStorePushServiceToken, " \t\r\n") {
		return cfg, errors.New("RUSTORE_PUSH_SERVICE_TOKEN is required by the Android push worker")
	}
	if cfg.FCMPushEnabled {
		cfg.FCMServiceAccountBase64 = os.Getenv("FCM_SERVICE_ACCOUNT_BASE64")
		raw, err := base64.StdEncoding.Strict().DecodeString(cfg.FCMServiceAccountBase64)
		if err != nil || len(raw) < 128 || len(raw) > 32768 {
			return cfg, errors.New("FCM_SERVICE_ACCOUNT_BASE64 is required by the Android push worker")
		}
	}
	if cfg.HuaweiPushEnabled {
		cfg.HuaweiClientID = os.Getenv("HUAWEI_CLIENT_ID")
		cfg.HuaweiClientSecret = os.Getenv("HUAWEI_CLIENT_SECRET")
		if cfg.HuaweiClientID == "" || len(cfg.HuaweiClientID) > 32 || strings.Trim(cfg.HuaweiClientID, "0123456789") != "" || len(cfg.HuaweiClientSecret) < 16 || len(cfg.HuaweiClientSecret) > 8192 || strings.ContainsAny(cfg.HuaweiClientSecret, " \t\r\n") {
			return cfg, errors.New("Huawei sending credentials are required by the Android push worker")
		}
	}
	return cfg, nil
}
