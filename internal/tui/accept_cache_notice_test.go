package tui

import (
	"testing"
)

// Decisão: o aviso de versão cede espaço, nunca toma o do contexto. Em toda
// largura em que a linha sem aviso mostra o contexto, a mesma linha com aviso
// pendente mostra o contexto no mesmo nível, com e sem cache visível.
func TestAcceptCacheNoticeNeverHidesContext(t *testing.T) {
	for _, branch := range []string{acceptCacheBranch, "dev"} {
		for _, cwd := range []string{"/workspace/qg", "/workspace/projects/quebra-galho/cli/internal"} {
			quiet := acceptCacheTUIModel(t, "0.16995")
			quiet.gitBranch = branch
			quiet.cwd = cwd
			quiet = acceptCacheCall(quiet, acceptT0, acceptCacheModelID, 340_000, 329_800, 10)
			noticed := quiet
			noticed.updateAvailable = "0.0.11"
			for width := 260; width >= 20; width-- {
				without := plainRender(t, quiet.statusLine(width))
				with := plainRender(t, noticed.statusLine(width))
				if acceptStatusLevels(with)["contexto"] < acceptStatusLevels(without)["contexto"] {
					t.Fatalf("branch %q, cwd %q, largura %d: o aviso pendente escondeu o contexto\nsem aviso: %q\ncom aviso: %q", branch, cwd, width, without, with)
				}
			}
		}
	}
}
