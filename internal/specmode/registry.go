package specmode

import (
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func RegisterDraftTools(registry *tools.Registry, workspaceRoot string, now func() time.Time) {
	if registry == nil {
		return
	}
	registry.Register(NewSubmitTool(workspaceRoot, now))
}
