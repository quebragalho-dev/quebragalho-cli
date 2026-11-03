# Changelog

O que mudou em cada versão do qg, contado em português simples para quem usa
o CLI. A versão mais nova está no topo.

## 0.0.11 — 2026-11-02

### Novidades

- O qg avisa no rodapé quando sai uma versão nova e oferece atualizar ao abrir.
- No modo sem confirmação, o qg libera leitura, escrita, busca e rede sem pedir aprovações no meio do caminho.
- No macOS, quando um programa tenta acessar a rede, o qg pede permissão em vez de falhar com erro estranho de DNS.
- Binário nativo para Windows ARM64: quem usa Windows em ARM não precisa mais da emulação do build x64.
- O pet saiu do QG. Quem usava pode apagar à mão a pasta `pets/` dentro da pasta de config do qg.
- Interface em português: ajuda, configuração, permissões, gestão e instalação falam pt-BR. Comandos e opções continuam em inglês.
- Apertar Enter também avança a tela de boas-vindas da configuração inicial (antes só a barra de espaço avançava).

### Correções

- O qg não congela mais ao abrir quando o serviço de mensagens entre sessões falha (por exemplo, com caminho de pasta muito longo).
- O modelo agora recebe o histórico real da conversa entre turnos.
- As retomadas de sessão (/resume) mantêm a última resposta e quem falou.
- Tratamento de chaves e inicialização de language servers mais seguro.
- Tabela de preços do gateway atualizada: o custo mostrado está certo, e modelo sem preço aparece como sem preço.
- Quando o modelo não está liberado para a conta, o qg diz que o modelo está indisponível e sugere escolher outro, em vez de erro de chave.

### Desempenho

- No gateway do QG, a continuação da conversa reaproveita o início do pedido, e o custo de entrada cai em média ~60%.
- A busca (grep) pula arquivos binários, gastando menos tokens.
- A geração do título da sessão usa raciocínio mínimo, com no máximo 2 tentativas.
- Enquanto a resposta chega, o qg usa menos CPU: a média cai de 10,9% para 8,2%, e o pico cai de 17,4% para 11,2%.

### Distribuição

- Instalação sem erro EBADENGINE no Node 18 ao 24: o agent-browser ficou na versão 0.27.0, que não exige Node 24.
- Guia de instalação explica o bloqueio do PowerShell ao `qg.ps1` e as saídas (`qg.cmd` ou liberar scripts locais).

## 0.0.10 — 2026-10-28

### Novidades

- Kits para programadores (SDKs) em TypeScript e Python: dá para usar o qg dentro de outros programas.
- Os kits estão publicados no npm e no PyPI.
- O uso de cada chamada agora informa o custo, e permissões negadas aparecem no resultado para quem usa os kits.

## 0.0.9 — 2026-10-28

### Mudanças

- Ajuste interno no processo de publicação (sem mudança para quem usa).

## 0.0.8 — 2026-10-28

### Mudanças

- Ajuste interno no processo de publicação (sem mudança para quem usa).

## 0.0.7 — 2026-10-28

### Correções

- Corrige a instalação no Windows pelo arquivo .zip, que vinha com arquivos auxiliares quebrados.

## 0.0.6 — 2026-10-28

### Novidades

- Entrar com a conta Quebra Galho pelo navegador no primeiro uso (com opção sem navegador).
- Comando /logout no TUI e `qg logout` no terminal.

### Correções

- Ajustes no login pelo navegador: link sempre completo e colar direto no campo.
- Atualização de segurança na base do qg.

### Mudanças

- O instalador e o `qg update` voltam a baixar direto do repositório oficial em vez do espelho.

## 0.0.5 — 2026-10-12

### Correções

- O instalador do Windows e o `qg update` passam a baixar do espelho público por padrão.

## 0.0.4 — 2026-10-10

### Novidades

- O instalador mostra o progresso do download e sugere o que fazer se falhar; se a pasta não permitir escrita, ele tenta outra sozinho.

### Mudanças

- A barra de contexto ficou mais limpa e a troca de provedores saiu da interface.

## 0.0.3 — 2026-10-10

### Novidades

- Painel /plan e melhorias nas telas de esforço, preços e uso.

### Correções

- Links de instalação corrigidos para o espelho público, com o README explicando a instalação por ele.

## 0.0.2 — 2026-10-10

### Novidades

- Visual novo no terminal: símbolo Q, paleta Quebra Galho e bichinhos (pets).
- Painéis /effort, /model e /usage, barra de estado com custo e contexto, e melhorias no campo de mensagem.
- Instalação passa a usar o espelho público, no install.sh e no pacote npm.

### Correções

- Acerto na largura da interface, que quebrava em alguns terminais.

## 0.0.1 — 2026-10-09

### Novidades

- Nasce o qg: assistente de programação no terminal, com login por chave e tela de boas-vindas.
- Conta Quebra Galho com entrada pelo navegador.
- Instalador que coloca o qg no PATH e pacote npm @quebragalho-dev/cli.
