package model

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLockForUpdateEmitsRowLock(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{DryRun: true})
	require.NoError(t, err)
	var rows []Redemption
	query := lockForUpdate(db).Where("id = ?", 1).Find(&rows)
	assert.Contains(t, query.Statement.SQL.String(), "FOR UPDATE")
}
