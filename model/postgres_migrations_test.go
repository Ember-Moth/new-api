package model

import (
	"testing"

	"github.com/QuantumNous/new-api/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunPostgresMainMigrationsRecordsEmbeddedSQL(t *testing.T) {
	db := testutil.OpenPostgresTestDB(t, "embedded_main_migrations")
	t.Setenv(postgresAutoCreatePerformanceIndexesEnv, "true")

	require.NoError(t, db.AutoMigrate(
		&Ability{},
		&Task{},
		&Midjourney{},
		&TopUp{},
		&QuotaData{},
		&QuotaDataDaily{},
		&QuotaDataMonthly{},
		&UserSubscription{},
		&Token{},
		&SubscriptionPlan{},
	))

	require.NoError(t, runPostgresMainMigrations(db))
	require.NoError(t, runPostgresMainMigrations(db))

	expectedIDs := []string{
		"000_schema_migrations",
		"010_tokens_model_limits_text",
		"011_subscription_plans_price_amount_decimal",
		"001_quota_rollups",
		"002_quota_rollups_backfill",
		"003_main_performance_indexes",
	}
	var count int64
	require.NoError(t, db.Table(postgresMigrationTable).
		Where("id IN ?", expectedIDs).
		Count(&count).Error)
	assert.EqualValues(t, len(expectedIDs), count)
}

func TestSplitPostgresSQLStatementsHandlesDollarQuotedBlocks(t *testing.T) {
	statements, err := splitPostgresSQLStatements(`
CREATE TABLE example (id bigint);

DO $$
BEGIN
  EXECUTE $sql$
    CREATE TABLE nested_example (
      content text DEFAULT 'a;b'
    )
  $sql$;
END $$;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_example_id ON example (id);
`)
	require.NoError(t, err)
	require.Len(t, statements, 3)
	assert.Contains(t, statements[1], "DO $$")
	assert.Contains(t, statements[2], "CREATE INDEX CONCURRENTLY")
}
