package instances

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/module/system/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/bytedance/gopkg/util/gopool"
)

type ReportConfig struct {
	OptionsVersion func() string
	Node           common.NodeIdentity
	Version        string
	StartedAt      int64
	Resources      func() contract.SystemInstanceResources
}

type Reporter struct {
	registry *Registry
	config   ReportConfig
	master   bool
	once     sync.Once
	done     chan struct{}
}

func NewReporter(registry *Registry, config ReportConfig, master bool) *Reporter {
	return &Reporter{registry: registry, config: config, master: master, done: make(chan struct{})}
}

func (r *Reporter) StartSystemInstanceReporter(ctx context.Context) <-chan struct{} {
	r.once.Do(func() {
		gopool.Go(func() {
			defer close(r.done)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				if err := r.ReportCurrentSystemInstance(ctx); err != nil {
					logger.LogWarn(ctx, fmt.Sprintf("system instance report failed: %v", err))
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	})
	return r.done
}

func (r *Reporter) ReportCurrentSystemInstance(ctx context.Context) error {
	identity := r.config.Node
	hostname, hostnameErr := os.Hostname()
	if strings.TrimSpace(identity.Name) == "" {
		if hostnameErr != nil || strings.TrimSpace(hostname) == "" {
			return fmt.Errorf("system instance node name is empty")
		}
		identity.Name = hostname
		identity.Source = common.NodeNameSourceHostname
		identity.ManuallyConfigured = false
		identity.ShouldConfigureManually = true
	}
	resources := contract.SystemInstanceResources{}
	if r.config.Resources != nil {
		resources = r.config.Resources()
	}
	info := contract.SystemInstanceInfo{
		SchemaVersion: 1,
		Node:          identity,
		Role: contract.SystemInstanceRoleInfo{
			IsMaster: r.master,
		},
		Runtime: contract.SystemInstanceRuntimeInfo{
			Version:   r.config.Version,
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
			StartedAt: r.config.StartedAt,
		},
		Host: contract.SystemInstanceHostInfo{
			Hostname: hostname,
		},
		Resources: resources,
	}
	if r.config.OptionsVersion != nil {
		info.Extra = map[string]any{"options_version": r.config.OptionsVersion()}
	}
	return r.registry.UpsertSystemInstance(ctx, identity.Name, info, r.config.StartedAt, common.GetTimestamp())
}
