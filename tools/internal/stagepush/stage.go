// Package stagepush builds an allowlisted push candidate in a private workdir.
package stagepush

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

var ErrInvalid = errors.New("Android push CI candidate validation failed")

func match(pattern, value string) bool { return regexp.MustCompile(pattern).MatchString(value) }
func Stage(workdir string, input io.Reader) error {
	b, err := deploydata.ReadBounded(input, 131072)
	if err != nil || len(b) == 0 || !deploydata.ASCII(b) {
		return ErrInvalid
	}
	m, err := deploydata.Object(b)
	if err != nil {
		return ErrInvalid
	}
	for k := range m {
		if k != "projectId" && k != "serviceToken" && k != "fcm" && k != "huawei" {
			return ErrInvalid
		}
	}
	project, secret := deploydata.String(m, "projectId"), deploydata.String(m, "serviceToken")
	if !match(`^[A-Za-z0-9_-]{1,128}$`, project) || !deploydata.Printable(secret, 32, 8192, "'\\") {
		return ErrInvalid
	}
	changed := [][2]string{{"ANDROID_PUSH_ENABLED", "true"}, {"RUSTORE_PUSH_PROJECT_ID", project}, {"RUSTORE_PUSH_SERVICE_TOKEN", "'" + secret + "'"}}
	if value, exists := m["fcm"]; exists {
		fcm, ok := value.(map[string]any)
		if !ok || !deploydata.Exact(fcm, "projectId", "serviceAccountBase64") {
			return ErrInvalid
		}
		project := deploydata.String(fcm, "projectId")
		encoded := deploydata.String(fcm, "serviceAccountBase64")
		if !match(`^[a-z0-9-]{4,128}$`, project) || len(encoded) > 45000 || !match(`^[A-Za-z0-9+/]*={0,2}$`, encoded) {
			return ErrInvalid
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return ErrInvalid
		}
		credentials, err := deploydata.Object(raw)
		if err != nil || deploydata.String(credentials, "type") != "service_account" || deploydata.String(credentials, "project_id") != project || deploydata.String(credentials, "private_key") == "" || deploydata.String(credentials, "client_email") == "" || deploydata.String(credentials, "token_uri") != "https://oauth2.googleapis.com/token" {
			return ErrInvalid
		}
		changed = append(changed, [2]string{"FCM_PUSH_ENABLED", "true"}, [2]string{"FCM_PROJECT_ID", project}, [2]string{"FCM_SERVICE_ACCOUNT_BASE64", encoded})
	}
	if value, exists := m["huawei"]; exists {
		h, ok := value.(map[string]any)
		if !ok || !deploydata.Exact(h, "projectId", "clientId", "clientSecret") {
			return ErrInvalid
		}
		project, client, secret := deploydata.String(h, "projectId"), deploydata.String(h, "clientId"), deploydata.String(h, "clientSecret")
		if !match(`^[0-9]{1,32}$`, project) || !match(`^[0-9]{1,32}$`, client) || len(secret) < 16 || len(secret) > 8192 || !match(`^[A-Za-z0-9_-]+$`, secret) {
			return ErrInvalid
		}
		changed = append(changed, [2]string{"HUAWEI_PUSH_ENABLED", "true"}, [2]string{"HUAWEI_PROJECT_ID", project}, [2]string{"HUAWEI_CLIENT_ID", client}, [2]string{"HUAWEI_CLIENT_SECRET", secret})
	}
	if deploydata.Candidate(workdir, changed) != nil {
		return ErrInvalid
	}
	return nil
}
func Run(_ context.Context, args []string, _ io.Writer, stderr io.Writer) int {
	if len(args) != 1 || Stage(args[0], os.Stdin) != nil {
		fmt.Fprintln(stderr, ErrInvalid)
		return 1
	}
	return 0
}
