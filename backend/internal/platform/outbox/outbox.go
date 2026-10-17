package outbox

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ID creates an opaque UUID without coupling module identities to another module.
func ID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func Append(ctx context.Context, tx pgx.Tx, id, aggregateID, accountID, kind string, payload any, processed bool) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox.events(id, aggregate_id, account_id, type, payload, processed_at)
 VALUES ($1,$2,$3,$4,$5,CASE WHEN $6 THEN now() ELSE NULL END)`, id, aggregateID, accountID, kind, data, processed)
	return err
}

func Cleanup(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM outbox.events WHERE id IN
 (SELECT id FROM outbox.events WHERE processed_at IS NOT NULL AND processed_at < now()-interval '90 days' ORDER BY processed_at,id LIMIT 500)`)
	return err
}
