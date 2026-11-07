package runner

import (
	"context"
	"errors"

	"github.com/nektos/act/pkg/common"
)

func (rc *RunContext) startTartEnvironment() common.Executor {
	return func(_ context.Context) error {
		return errors.New("tart platforms are not supported yet")
	}
}
