package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/internal/migration/schema"
	"github.com/QuantumNous/new-api/internal/module/system"
	"github.com/QuantumNous/new-api/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMaintenanceTaskPersistsCallbackFailure(t *testing.T) {
	db, err := testdb.Open(t, &gorm.Config{})
	require.NoError(t, err)
	dbConn, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, schema.UpPostgres(dbConn))
	require.NoError(t, schema.UpPostgres(dbConn))
	cache := testdb.UseCache(t)

	tasks := system.New(system.Dependencies{
		Cache:    cache,
		DB:       db,
		NodeName: "maintenance-test",
		Master:   true,
	})
	called := make(chan struct{}, 1)
	maintenanceErr := errors.New("maintenance pass failed")
	RegisterMaintenanceTasks(tasks, MaintenanceWorkloads{
		AuthArtifactCleanup: func(context.Context) error {
			called <- struct{}{}
			return maintenanceErr
		},
	})

	created, err := tasks.CreateSystemTask(t.Context(), SystemTaskTypeAuthArtifactCleanup, nil, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tasks.StartSystemTaskRunner(ctx)

	var completed *system.SystemTask
	require.Eventually(t, func() bool {
		completed, err = tasks.GetSystemTaskByTaskID(t.Context(), created.TaskID)
		return err == nil && completed != nil && completed.Status == system.SystemTaskStatusFailed
	}, 10*time.Second, 20*time.Millisecond)

	select {
	case <-called:
	default:
		require.FailNow(t, "maintenance callback was not invoked")
	}
	assert.Equal(t, maintenanceErr.Error(), completed.Error)
	assert.Empty(t, completed.Result)
}
