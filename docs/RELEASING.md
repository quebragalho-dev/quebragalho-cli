# Como lançar uma versão

Roteiro para quem prepara a release (humano ou agente). Sem ele, o workflow
de release falha de propósito.

1. Atualize a versão no `package.json` (ex.: `0.0.11`).
2. No MESMO commit, escreva a seção da versão no topo do `CHANGELOG.md`:
   - Título exatamente `## 0.0.11 — 2026-11-02` (travessão "—", data do tag).
   - Conte o que muda para quem USA o CLI, em português simples: sem nomes de
     PR, autores, hashes ou termos internos.
   - Agrupe em "Novidades", "Correções" e "Mudanças", só os grupos com itens.
   - Cubra tudo de `git log v0.0.10..HEAD`, resumindo o que for bastidor.
   - Confira localmente: `scripts/check-changelog.sh 0.0.11` tem que sair 0.
3. Abra o PR, revise, faça merge no `main`.
4. Crie o tag `v0.0.11` no commit do merge e envie (`git push origin v0.0.11`).

O workflow `release-artifacts.yml` roda `scripts/check-changelog.sh` no tag e
falha sem a seção, para o link do changelog nunca abrir vazio. A automação
antiga de changelog foi removida: ela reescreveria o arquivo em inglês com
títulos de PR.
