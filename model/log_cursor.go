package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrInvalidLogCursor = errors.New("invalid log cursor")

type logPosition struct {
	Version   int                 `json:"v"`
	Scope     string              `json:"s"`
	Database  common.DatabaseType `json:"d"`
	CreatedAt int64               `json:"t"`
	ID        int                 `json:"i"`
	RequestID string              `json:"r,omitempty"`
	EventID   string              `json:"e,omitempty"`
}

// LogCursorPage carries opaque pagination state before display IDs and private
// metadata are removed from user-visible rows. Cursors cannot expose real IDs.
type LogCursorPage struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
	scope      string
	after      *logPosition
}

func logCursorCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("new-api/log-cursor/v1:" + common.CryptoSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func NewLogCursorPage(encoded, scope string) (*LogCursorPage, error) {
	page := &LogCursorPage{scope: scope}
	if encoded == "" {
		return page, nil
	}
	if len(encoded) > 2048 {
		return nil, ErrInvalidLogCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrInvalidLogCursor
	}
	aead, err := logCursorCipher()
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrInvalidLogCursor
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
	if err != nil {
		return nil, ErrInvalidLogCursor
	}
	var position logPosition
	if common.Unmarshal(plain, &position) != nil || position.Version != 1 || position.Scope != scope || position.Database != common.LogDatabaseType() || position.CreatedAt < 0 || position.ID < 0 {
		return nil, ErrInvalidLogCursor
	}
	page.after = &position
	return page, nil
}

func selectLogCursorPage(query *gorm.DB, limit int, page *LogCursorPage) ([]*Log, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("log page size is out of range")
	}
	if page.after != nil {
		position := page.after
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			query = query.Where("(logs.created_at, logs.request_id, logs.event_id) < (?, ?, ?)", position.CreatedAt, position.RequestID, position.EventID)
		} else {
			query = query.Where("(logs.created_at, logs.id) < (?, ?)", position.CreatedAt, position.ID)
		}
	}
	order := "logs.created_at DESC, logs.id DESC"
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		order = "logs.created_at DESC, logs.request_id DESC, logs.event_id DESC"
	}
	var logs []*Log
	if err := query.Order(order).Limit(limit + 1).Find(&logs).Error; err != nil {
		return nil, err
	}
	page.HasMore = len(logs) > limit
	if !page.HasMore {
		return logs, nil
	}
	logs = logs[:limit]
	last := logs[len(logs)-1]
	position := logPosition{Version: 1, Scope: page.scope, Database: common.LogDatabaseType(), CreatedAt: last.CreatedAt, ID: last.Id, RequestID: last.RequestId, EventID: last.EventID}
	plain, err := common.Marshal(position)
	if err != nil {
		return nil, err
	}
	aead, err := logCursorCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	page.NextCursor = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil))
	return logs, nil
}
