package arch

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are dependency contracts, not a check of directory names alone.
// Legacy business packages remain outside the migrated-module boundary until
// their capabilities have been moved; the completion checklist tracks them.
func TestModularDependencyBoundaries(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	const prefix = "github.com/QuantumNous/new-api/"
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "dist" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(rel)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			moduleCode := strings.HasPrefix(dir, "internal/module/")
			if moduleCode && !strings.Contains(dir, "/transport/") {
				assert.NotEqual(t, "github.com/gin-gonic/gin", imported, "%s: core module code must use context and contracts", path)
			}
			if !strings.HasPrefix(imported, prefix) {
				continue
			}
			dep := strings.TrimPrefix(imported, prefix)
			if dep == "internal/app" || strings.HasPrefix(dep, "internal/app/") {
				assert.True(t, strings.HasPrefix(dir, "cmd/") || dir == "internal/app" || strings.HasPrefix(dir, "internal/app/"), "%s: only command entrypoints may import the application composition root", path)
			}
			assert.False(t, dep == "router" || strings.HasPrefix(dep, "router/") || dep == "middleware" || strings.HasPrefix(dep, "middleware/"), "%s: use HTTP transport packages instead of removed root adapters", path)
			if moduleCode || strings.HasPrefix(dir, "internal/infra/") {
				first := strings.Split(dep, "/")[0]
				assert.NotContains(t, []string{"controller", "service", "model"}, first, "%s: migrated modules and infrastructure must not depend on legacy business packages", path)
				assert.False(t, strings.HasPrefix(dep, "internal/transport/"), "%s: application adapters must not be dependencies of modules or infrastructure", path)
			}
			if strings.HasPrefix(dir, "internal/infra/") {
				assert.False(t, strings.HasPrefix(dep, "internal/module/"), "%s: infrastructure must not depend on business modules", path)
			}
			if strings.HasPrefix(dir, "relaykit/") {
				assert.True(t, strings.HasPrefix(dep, "relaykit/"), "%s: RelayKit must stay independent of the application", path)
			}
		}
		return nil
	})
	require.NoError(t, err)
}
