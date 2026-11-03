# Changelog

O que mudou em cada versão do qg, contado em português simples para quem usa
o CLI. A versão mais nova está no topo.

## 0.0.15 — 2026-11-03

### Novidades

- Medidor de contexto ao vivo: a borda de baixo da caixa de texto enche conforme o modelo usa o contexto, verde até 59%, âmbar até 84% e vermelho a partir de 85%. A linha de status mostra o uso, por exemplo `30% · 300k/1m`. O tamanho do contexto vem do catálogo de cada modelo. O nome do modelo foi para a borda de cima.
- `/model` novo: abre já buscando, separa Favoritos, Recentes e Catálogo, e mostra o tamanho do contexto e o preço de cada modelo. O modelo marcado mostra o que sabe fazer e o preço de saída. Como as letras vão para a busca, "só nesta sessão" passou para Alt+s e favoritar para Alt+f. Tab abre o nível.
- `/effort` novo: os níveis aparecem em escada, com pontos e uma frase curta em cada um, e só aparecem os níveis que o modelo aceita.

### Mudanças

- `/clear` agora começa uma conversa nova, como o `/new`; a anterior continua no `/resume`. Antes ele só limpava a tela, e o modelo seguia com a conversa inteira.
- Trocar de modelo mantém a conversa, e o medidor passa a mostrar o uso contra o tamanho do modelo novo.

## 0.0.14 — 2026-11-03

### Correções

- No Linux, o qg encontra o ambiente isolado mesmo quando instalado pelo npm ou numa pasta fora do PATH. Antes, nesses casos, os comandos do agente rodavam sem isolamento (só com o pedido de permissão), e o `qg doctor` dizia que o ambiente isolado não estava disponível.
- O `qg doctor` diz qual peça do ambiente isolado falta no Linux e como resolver.
- A resposta do modelo não trava mais no Linux e no Windows quando o provedor não fecha a conexão no fim.
- No Windows, atualizar o qg instalado pelo npm não deixa mais uma pasta de ~200 MB sobrando. Quem está na 0.0.13 ou antes ainda passa pelo jeito antigo uma última vez.
- `qg cron list --json`, `qg trust list --json` e `qg trust remove --json` agora devolvem JSON de verdade; o prompt das tarefas agendadas aparece com segredos escondidos.

### Interface em português

- Motivos dos pedidos de permissão, linhas de negado, cartões do /model, /profile e /effort, e saída de hooks, plugins, context e especialistas agora em português. A explicação de um passo do plano também vem em português.

## 0.0.13 — 2026-11-03

### Correções

- Quem instalou o qg pelo npm numa pasta própria agora recebe a atualização nessa pasta; antes o qg dizia que tinha atualizado, mas continuava na versão antiga.
- O qg só diz que atualizou depois de conferir a versão nova; se não der para atualizar, mostra o comando para fazer à mão.
- Mensagens da atualização em português.

Quem instalou pelo npm numa pasta própria e está na 0.0.11 ou na 0.0.12: para chegar à 0.0.13, rode `npm install -g --prefix "<pasta>" @quebragalho-dev/cli@latest`. A atualização automática dessas versões ainda usa o jeito antigo.

## 0.0.12 — 2026-11-03

### Correções

- Atualizar o qg pelo script de instalação com o qg aberto não quebra mais a instalação (macOS, Linux, Windows); se algo falhar, a versão anterior continua instalada.

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
