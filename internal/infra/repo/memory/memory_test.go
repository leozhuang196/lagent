package memory_test

import (
	"testing"

	"github.com/eternityx/lagent/internal/infra/repo"
	"github.com/eternityx/lagent/internal/infra/repo/memory"
	"github.com/eternityx/lagent/internal/infra/repo/repotest"
)

func TestContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) *repo.Store { return memory.NewStore(nil) })
}
