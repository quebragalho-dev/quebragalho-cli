---
name: browser-use
description: Controla o navegador com browser_open, browser_snapshot e browser_click/type/press/action.
---

Use as ferramentas `browser_*` para navegar e operar páginas da web.

Regras:
- Se o controle do navegador estiver desligado ou as ferramentas `browser_*` estiverem indisponíveis, não tente contornar: avise o usuário para ligar em `/config` → **Controle do navegador** (ou `localControl.browser.enabled=true`).
- Siga o ciclo: `browser_open` → `browser_snapshot` → agir pelo ref (`browser_click`, `browser_type`, `browser_press`, `browser_action`) → ler de novo com `browser_snapshot`.
- Os refs mudam depois de navegar ou de qualquer alteração na página: tire um novo `browser_snapshot`.
- Se um clique falhar por elemento coberto (ex.: banner de cookies), feche o banner primeiro e tente de novo.
- Só use `headed` (`headed=true`) se o usuário pedir para ver a janela ou para login/CAPTCHA.
- Nunca peça senha nem dados de pagamento ao usuário pelo chat; para login, abra com `headed=true` e deixe a pessoa digitar.
- Se faltar navegador, execute `browser_install` uma vez (no Linux ARM64, peça para o usuário instalar o Chromium do sistema).
- Só tire screenshot (`capture_artifact` com `action="browser_screenshot"`) quando o texto não bastar.
- Para o guia completo de comandos, execute `browser_action` com `command="skills"` e `args=["get","core"]`.
