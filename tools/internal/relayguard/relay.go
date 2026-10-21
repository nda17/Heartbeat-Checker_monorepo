// Package relayguard checks the bounded public upstream error contract.
package relayguard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/nda17/Heartbeat-Checker_monorepo/tools/internal/deploydata"
)

func Validate(input io.Reader, status int) error {
	b, err := deploydata.ReadBounded(input, 64<<10)
	if err != nil {
		return err
	}
	m, err := deploydata.Object(b)
	if err != nil || m["ok"] != false || m["error_code"] != json.Number(strconv.Itoa(status)) {
		return deploydata.ErrInvalid
	}
	return nil
}
func Run(_ context.Context, args []string, _ io.Writer, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "Invalid relay response arguments")
		return 1
	}
	status, err := strconv.Atoi(args[1])
	if err != nil || status < 100 || status > 599 {
		fmt.Fprintln(stderr, "Invalid relay response status")
		return 1
	}
	f, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "Cannot read relay response")
		return 1
	}
	defer f.Close()
	if Validate(f, status) != nil {
		fmt.Fprintln(stderr, "Telegram relay response is not the upstream API contract")
		return 1
	}
	return 0
}
