---
name: computer-use
description: Controla aplicativos e janelas do computador com desktop_snapshot e desktop_action.
---

Use `desktop_snapshot` e `desktop_action` para operar aplicativos e janelas do computador.

Regras:
- Se o controle do computador estiver desligado ou as ferramentas `desktop_*` estiverem indisponíveis, não tente contornar: avise o usuário para ligar em `/config` → **Controle do computador** (ou `localControl.desktop.enabled=true`).
- Siga o ciclo **ler → agir → ler de novo**: capture o estado com `desktop_snapshot`, execute uma ação com `desktop_action` e leia novamente com `desktop_snapshot` para confirmar o resultado.
- Use sempre **refs qualificadas** (`@<snapshot_id>:eN`, nunca `@eN` sem prefixo). Em `STALE_REF` ou `AMBIGUOUS_TARGET`, faça novo `desktop_snapshot` (ou informe `window_id`).
- Só faça **retry** quando `error.disposition.retry == "safe"`.
- Só use `headed=true` com **permissão explícita** do usuário (nunca como fallback após falha headless).
- Só tire **screenshot** quando a árvore de acessibilidade não bastar.
- Para o guia completo de comandos e parâmetros, execute `desktop_action` com `command="skills"` e `args=["get","desktop"]`.
