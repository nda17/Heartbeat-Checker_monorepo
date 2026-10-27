package deployguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

func EnableEmail(output string) error {
	b, err := deploydata.ReadPrivate(".env", 256<<10)
	if err != nil {
		return err
	}
	lines := bytes.Split(b, []byte{'\n'})
	found := -1
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("EMAIL_NOTIFICATIONS_ENABLED=")) {
			if found >= 0 {
				return errors.New("Duplicate email notification flag")
			}
			found = i
		}
	}
	line := []byte("EMAIL_NOTIFICATIONS_ENABLED=true")
	if found >= 0 {
		lines[found] = line
	} else if len(lines[len(lines)-1]) == 0 {
		lines[len(lines)-1] = line
		lines = append(lines, []byte{})
	} else {
		lines = append(lines, line)
	}
	return deploydata.WriteExclusive(output, bytes.Join(lines, []byte{'\n'}))
}

func ValidateOwner(path string) error {
	b, err := deploydata.ReadPrivate(path, 4096)
	if err != nil || len(b) == 0 {
		return errors.New("Owner import payload validation failed")
	}
	m, err := deploydata.Object(b)
	if err != nil || !deploydata.Exact(m, "accountId", "name", "email", "passwordHash", "quota", "validUntil") || m["accountId"] != deploydata.OwnerID || m["email"] != "khv1702@gmail.com" || m["quota"] != json.Number("1000") || m["validUntil"] != "2027-10-18T00:00:00Z" {
		return errors.New("Owner import payload validation failed")
	}
	name := deploydata.String(m, "name")
	if utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 100 || strings.TrimSpace(name) != name || strings.IndexFunc(name, func(c rune) bool { return c < 32 || c == 127 }) >= 0 {
		return errors.New("Owner import payload validation failed")
	}
	if !match(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`, deploydata.String(m, "passwordHash")) {
		return errors.New("Owner import payload validation failed")
	}
	return nil
}

func OwnerSQL(payload, template, source string, output io.Writer) error {
	if !match(`^[0-9a-f]{40}$`, source) || ValidateOwner(payload) != nil {
		return deploydata.ErrInvalid
	}
	b, err := deploydata.ReadPrivate(payload, 4096)
	if err != nil {
		return err
	}
	f, err := os.Open(template)
	if err != nil {
		return deploydata.ErrInvalid
	}
	sql, err := deploydata.ReadBounded(f, 128<<10)
	f.Close()
	if err != nil {
		return err
	}
	marker := []byte("__OWNER_PAYLOAD_ROW__")
	if bytes.Count(sql, marker) != 1 {
		return errors.New("Invalid tracked owner import SQL")
	}
	row := fmt.Appendf(nil, "%s\t%x\t%s", hex.EncodeToString(b), sha256.Sum256(b), source)
	_, err = output.Write(bytes.Replace(sql, marker, row, 1))
	return err
}

func CheckBackupEnv(path string) error {
	b, err := deploydata.ReadPrivate(path, 256<<10)
	if err != nil || !utf8.Valid(b) {
		return errors.New("Invalid private backup environment")
	}
	allowed := map[string]bool{}
	for _, key := range []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_REGION", "BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY", "BACKUP_AGE_RECIPIENT", "TELEGRAM_BOT_INFORMER_TOKEN", "TELEGRAM_INTERNAL_CHAT_ID", "TELEGRAM_BACKUPS_TOPIC_ID"} {
		allowed[key] = true
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !match(`^[A-Z][A-Z0-9_]*$`, key) || !allowed[key] || seen[key] {
			return errors.New(".backup.env contains an unsupported key")
		}
		seen[key] = true
	}
	return nil
}

// Frame is the sole registry credential/stdin activation encoder for CI.
func Frame(output io.Writer, lookup func(string) string) error {
	token := lookup("GH_TOKEN")
	if len(token) < 1 || len(token) > 4096 || !match(`^[A-Za-z0-9_.-]+$`, token) {
		return errors.New("Invalid registry credential framing")
	}
	var payload any
	if lookup("ACTIVATE_YOOKASSA_TEST") == "true" || lookup("ACTIVATE_YOOKASSA_PRODUCTION") == "true" || lookup("ACTIVATE_NATIVE_SDK_TEST") == "true" {
		prefix := "YOOKASSA_PRODUCTION"
		if lookup("ACTIVATE_YOOKASSA_TEST") == "true" || lookup("ACTIVATE_NATIVE_SDK_TEST") == "true" {
			prefix = "YOOKASSA_TEST"
		}
		shop, secret := lookup(prefix+"_SHOP_ID"), lookup(prefix+"_SECRET_KEY")
		if shop == "" || secret == "" {
			return errors.New("Missing protected YooKassa configuration")
		}
		payload = map[string]any{"shopId": shop, "secretKey": secret}
	}
	huawei := lookup("ACTIVATE_HUAWEI_PUSH")
	if huawei != "" && huawei != "true" && huawei != "false" {
		return errors.New("Invalid Huawei push activation flag")
	}
	if huawei == "true" && lookup("ACTIVATE_ANDROID_PUSH") != "true" {
		return errors.New("Huawei activation requires Android push activation")
	}
	if lookup("ACTIVATE_ANDROID_PUSH") == "true" {
		project, secret := lookup("RUSTORE_PUSH_PROJECT_ID"), lookup("RUSTORE_PUSH_SERVICE_TOKEN")
		if project == "" || secret == "" {
			return errors.New("Missing protected RuStore push configuration")
		}
		fcmProject, fcmCredentials := lookup("FCM_PROJECT_ID"), lookup("FCM_SERVICE_ACCOUNT_BASE64")
		if fcmProject == "" || fcmCredentials == "" {
			return errors.New("Missing protected Firebase push configuration")
		}
		push := map[string]any{"projectId": project, "serviceToken": secret, "fcm": map[string]any{"projectId": fcmProject, "serviceAccountBase64": fcmCredentials}}
		if huawei == "true" {
			project, client, secret := lookup("HUAWEI_PROJECT_ID"), lookup("HUAWEI_CLIENT_ID"), lookup("HUAWEI_CLIENT_SECRET")
			if !match(`^[0-9]{1,32}$`, project) || !match(`^[0-9]{1,32}$`, client) || len(secret) < 16 || len(secret) > 8192 || !match(`^[A-Za-z0-9_-]+$`, secret) {
				return errors.New("Missing or invalid protected Huawei push configuration")
			}
			push["huawei"] = map[string]any{"projectId": project, "clientId": client, "clientSecret": secret}
		}
		payload = push
	}
	// Validate/encode before writing any frame, so failures never leak a partial token.
	var b []byte
	var err error
	if payload != nil {
		b, err = json.Marshal(payload)
		if err != nil {
			return deploydata.ErrInvalid
		}
	}
	if _, err = io.WriteString(output, token+"\n"); err != nil {
		return err
	}
	if b != nil {
		_, err = output.Write(b)
	}
	return err
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := run(ctx, args, os.Stdin, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "Deployment data check failed: "+err.Error())
		return 1
	}
	return 0
}
func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return deploydata.ErrInvalid
	}
	switch args[0] {
	case "stdin-empty":
		if len(args) != 1 {
			return deploydata.ErrInvalid
		}
		b, err := deploydata.ReadBounded(input, 0)
		if err != nil || len(b) != 0 {
			return errors.New("Unexpected deployment input")
		}
		return nil
	case "email-enable":
		if len(args) != 2 {
			return deploydata.ErrInvalid
		}
		return EnableEmail(args[1])
	case "owner-validate":
		if len(args) != 2 {
			return deploydata.ErrInvalid
		}
		return ValidateOwner(args[1])
	case "owner-sql":
		if len(args) != 4 {
			return deploydata.ErrInvalid
		}
		return OwnerSQL(args[1], args[2], args[3], output)
	case "activation-check":
		if len(args) != 10 && len(args) != 11 {
			return deploydata.ErrInvalid
		}
		v := make([]bool, 9)
		for i, s := range args[2:] {
			if s != "true" && s != "false" {
				return deploydata.ErrInvalid
			}
			v[i] = s == "true"
		}
		return ValidateEnv(args[1], Activation{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7], v[8]})
	case "backup-env-check":
		if len(args) != 2 {
			return deploydata.ErrInvalid
		}
		return CheckBackupEnv(args[1])
	case "frame":
		if len(args) != 1 {
			return deploydata.ErrInvalid
		}
		return Frame(output, os.Getenv)
	case "compose-check":
		if len(args) != 1 {
			return deploydata.ErrInvalid
		}
		return CheckCompose(input)
	case "compose-s3-env":
		if len(args) != 2 {
			return deploydata.ErrInvalid
		}
		return WriteS3Env(input, args[1])
	case "compose-field":
		if len(args) != 2 {
			return deploydata.ErrInvalid
		}
		c, err := ReadCompose(input)
		if err != nil {
			return err
		}
		value := ""
		switch args[1] {
		case "backup.image":
			value = c["backup"].Image
		case "backup.source":
			value = c["backup"].Environment["BACKUP_SOURCE_SHA"]
		default:
			return deploydata.ErrInvalid
		}
		if value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return deploydata.ErrInvalid
		}
		_, err = fmt.Fprintln(output, value)
		return err
	case "probe-smoke":
		if len(args) != 1 {
			return deploydata.ErrInvalid
		}
		if err := ProbeSmoke(ctx, input, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Probe public GET and metadata rejection passed")
		return err
	case "json-check":
		if len(args) != 2 {
			return deploydata.ErrInvalid
		}
		f, err := os.Open(args[1])
		if err != nil {
			return deploydata.ErrInvalid
		}
		defer f.Close()
		b, err := deploydata.ReadBounded(f, 1<<20)
		if err != nil {
			return err
		}
		_, err = deploydata.Object(b)
		return err
	default:
		return deploydata.ErrInvalid
	}
}
