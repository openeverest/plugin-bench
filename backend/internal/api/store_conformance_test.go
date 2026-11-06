package api_test

import (
	"testing"

	"github.com/openeverest/plugin-bench/backend/internal/api"
	"github.com/openeverest/plugin-bench/backend/internal/api/storetest"
)

func TestMemoryRunStoreConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) api.RunStore { return api.NewRunStore() })
}
