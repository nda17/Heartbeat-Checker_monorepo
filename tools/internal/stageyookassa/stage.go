// Package stageyookassa stages only the selected reviewed billing credentials.
package stageyookassa

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

var ErrInvalid = errors.New("YooKassa CI candidate validation failed")

// Stage's optional client is a test transport seam; production uses verified TLS,
// no environment proxies and no redirects to another credential recipient.
func Stage(ctx context.Context, workdir, mode string, input io.Reader, client *http.Client) error {
	if mode != "test" && mode != "production" {
		return ErrInvalid
	}
	b, err := deploydata.ReadBounded(input, 2048)
	if err != nil || len(b) == 0 || !deploydata.ASCII(b) {
		return ErrInvalid
	}
	m, err := deploydata.Object(b)
	if err != nil || !deploydata.Exact(m, "shopId", "secretKey") {
		return ErrInvalid
	}
	shop, secret := deploydata.String(m, "shopId"), deploydata.String(m, "secretKey")
	if !regexp.MustCompile(`^[0-9]{1,32}$`).MatchString(shop) || !deploydata.Printable(secret, 16, 512, "'\\") || (mode == "test" && !strings.HasPrefix(secret, "test_")) || (mode == "production" && strings.HasPrefix(secret, "test_")) {
		return ErrInvalid
	}
	if mode == "production" && verify(ctx, shop, secret, client) != nil {
		return ErrInvalid
	}
	changed := [][2]string{{"BILLING_PROVIDER", "yookassa"}, {"YOOKASSA_MODE", mode}}
	if mode == "test" {
		changed = append(changed, [2]string{"BILLING_TEST_ACCOUNT_IDS", deploydata.OwnerID}, [2]string{"YOOKASSA_TEST_SHOP_ID", shop}, [2]string{"YOOKASSA_TEST_SECRET_KEY", "'" + secret + "'"})
	} else {
		changed = append(changed, [2]string{"YOOKASSA_PRODUCTION_SHOP_ID", shop}, [2]string{"YOOKASSA_PRODUCTION_SECRET_KEY", "'" + secret + "'"})
	}
	if deploydata.Candidate(workdir, changed) != nil {
		return ErrInvalid
	}
	return nil
}

func verify(ctx context.Context, shop, secret string, client *http.Client) error {
	if client == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		client = &http.Client{Transport: t}
	}
	c := *client
	c.Timeout = 8 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.yookassa.ru/v3/me", nil)
	if err != nil {
		return ErrInvalid
	}
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(shop+":"+secret)))
	response, err := c.Do(r)
	if err != nil {
		return ErrInvalid
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrInvalid
	}
	b, err := deploydata.ReadBounded(response.Body, 64<<10)
	if err != nil {
		return ErrInvalid
	}
	m, err := deploydata.Object(b)
	if err != nil {
		return ErrInvalid
	}
	account := deploydata.String(m, "account_id")
	if n, ok := m["account_id"].(json.Number); ok {
		account = string(n)
	}
	if account != shop || m["test"] != false || m["status"] != "enabled" {
		return ErrInvalid
	}
	return nil
}
func Run(ctx context.Context, args []string, _ io.Writer, stderr io.Writer) int {
	mode := "test"
	if len(args) == 2 {
		mode = args[1]
	}
	if len(args) < 1 || len(args) > 2 || Stage(ctx, args[0], mode, os.Stdin, nil) != nil {
		fmt.Fprintln(stderr, ErrInvalid)
		return 1
	}
	return 0
}
