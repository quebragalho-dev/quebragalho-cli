# Changelog

O que mudou em cada versão do qg, contado em português simples para quem usa
o CLI. A versão mais nova está no topo.

## 0.0.22 — 2026-11-18

### Novidades

- Releases assinadas: a partir desta versão, cada arquivo publicado vem com uma assinatura Sigstore. Os instaladores conferem a assinatura quando o `cosign` está instalado no computador e, sem ele, seguem conferindo o SHA-256 como antes. O `docs/INSTALL.md` mostra como verificar à mão.
- `qg exec --resume` envia o histórico de verdade da conversa retomada quando ele cabe na janela de contexto do modelo.
- Ditado por voz sem rede: os modelos locais de reconhecimento em tempo real rodam dentro do próprio qg, sem abrir um servidor no computador.
- A statusline personalizada mostra loops ativos, o download do ditado e a atualização disponível.

### Mudanças

- O ambiente isolado bloqueia a leitura de credenciais e tokens (como `~/.ssh`, `~/.aws`, `~/.config/gh` e `~/.npmrc`), mantendo legíveis os plugins, especialistas e comandos do qg.
- Comandos do git que só leem passam a conferir a configuração do repositório antes de rodar sem pedir aprovação, e pedem aprovação quando o repositório configura hooks que executariam programas.
- O aviso de `--auto high` sem isolamento deixa claro que o modelo terá acesso total.
- No Windows, o isolamento ficou mais completo: as pastas `.qg` e `.agents` ficam protegidas mesmo quando ainda não existem, o `qg sandbox setup` vale para qualquer pasta, e o aviso de setup pendente voltou a aparecer.
- Gravar cada passo da sessão ficou cerca de 3 vezes mais rápido, sem perder a proteção contra queda de energia.
- O painel do `/autocompact` avisa quando o limite escolhido fica abaixo de 50 mil tokens.
- Depois de resumir a conversa, a barra lateral e a retomada mostram o contexto que realmente sobrou, e o `/goal` conta os tokens do resumo.
- No Windows, o comando da statusline termina junto com o qg.

### Correções

- Clicar com o mouse nos seletores não faz mais a lista rolar.
- Segurar Espaço para ditar funciona mesmo quando o teclado repete a tecla devagar.
- Voltar no histórico (`rewind`) não repete mais a numeração dos eventos da sessão.
- Um processo em segundo plano encerrado ao mesmo tempo em que termina sozinho não fica mais com dois estados finais.
- Ao abrir um novo pedido de permissão, o anterior é negado em vez de ficar pendurado.
- No Windows: o qg acha os programas instalados fora de `C:\Program Files` e explica como encontrar a janela de apps da Microsoft Store depois de abri-los.

## 0.0.21 — 2026-11-17

### Novidades

- Controle do navegador: o qg abre sites, pesquisa, clica, preenche formulários e lê páginas por você, num navegador que fica escondido. Ele usa o Chrome, Chromium, Brave ou Edge que você já tem; se não houver nenhum, baixa um navegador uma única vez (no Linux ARM64, explica como instalar o Chromium do sistema). Quando um site pede login ou CAPTCHA, a janela aparece para você mesmo entrar: o qg nunca pede sua senha pelo chat. Ligue ou desligue em `/config` → "Controle do navegador", e use `/browser-use` para pedir direto.
- Controle do computador: o qg vê e usa as janelas de outros programas (clicar, digitar, ler o que está na tela). Ligue ou desligue em `/config` → "Controle do computador", e use `/computer-use` para pedir direto. No Windows, se faltar o Visual C++ da Microsoft, o qg explica como instalar.
- O qg baixa os componentes do controle do navegador e do computador no primeiro uso e confere que cada arquivo é exatamente o esperado antes de rodar.
- `/config` novo: um painel só, com busca, todo em português, que cabe em qualquer tamanho de terminal.
- `/style` em português, com seletor, e a escolha fica salva para as próximas vezes.

### Mudanças

- `/goal` mais barato e mais rápido com o mesmo acerto: nos nossos testes, cada meta custou de 11% a 25% menos e terminou até 39% antes, porque o modelo encerra na mesma vez em que conclui e não refaz verificações já feitas.
- A linha "Continuando meta" mostra só a primeira linha do objetivo, e um objetivo com mais de 4.000 caracteres é recusado com explicação em português.
- O limite de turnos por execução subiu de 80 para 400.
- Saíram os comandos `/fast`, `/turns` e `/profile`, e a opção `--exec-profile` do `qg exec`.
- O resumo feito pelo `/compact` entra no contador de uso e custo.

### Correções

- `/compact` não falha mais com resumo vazio.
- `/goal --tokens N` mantém as quebras de linha do objetivo.

## 0.0.20 — 2026-11-16

### Novidades

- `/voice`: ditado por voz segurando Espaço. Rode `/voice` uma vez e o qg oferece baixar o reconhecimento de fala (487 MB, com barra de progresso); depois disso o modo de voz liga sozinho e continua ligado nas próximas vezes. Segure Espaço por 2 segundos, fale e solte: o texto aparece na caixa enquanto você fala e nada é enviado sem Enter. O reconhecimento roda no seu computador, entende português e mais 24 idiomas, e sua voz não é enviada a lugar nenhum.
- No macOS e no Windows o ditado grava pelo próprio qg, sem instalar outro programa. Ao ligar, o qg testa o microfone e, se ele não entrega som, mostra onde liberar a permissão.
- `/autocompact`: você escolhe a partir de quantos tokens o qg resume a conversa sozinho, por exemplo `/autocompact 400k`, ou `off` para desligar. Sem argumento, abre um painel para escolher. O valor fica salvo e vale também para o `qg exec`.
- No Windows, quando o ambiente isolado não consegue iniciar, o qg explica o motivo e o que fazer.

### Mudanças

- Atualização e instaladores só aceitam downloads por HTTPS; um endereço sem HTTPS é recusado.
- No Windows, a saída de programas que escrevem em UTF-16 aparece legível, em vez de caracteres trocados.
- Arquivos de sessão e de configuração são gravados de forma mais segura: uma queda de energia ou um arquivo travado por outro programa não deixa mais o arquivo pela metade.
- Servidores MCP que demoram a responder na abertura continuam sendo tentados em segundo plano.

### Correções

- Ctrl+C durante uma gravação de voz cancela o ditado, em vez de apagar o texto digitado.
- `/clear`, `/spec`, retomar e voltar no histórico são recusados enquanto a conversa está sendo resumida, e `/clear` é recusado dentro do `/btw`; antes podiam deixar a conversa em estado estranho.
- Cliques com o mouse acertam a linha certa no seletor de modelos e nas mensagens com anexo, e a rolagem dentro da conversa de um subagente parou de pular.
- A preferência de modelo usado para resumir a conversa (`preferences.compactionModel`) voltou a ser respeitada.
- Comandos do git com opções que executam outros programas passam a pedir aprovação.
- Uma resposta interrompida no `qg exec` e no modo JSON termina com erro claro, em vez de parecer concluída.
- No Windows: o qg acessa arquivos em outro disco quando você autoriza, e o instalador avisa quando o arquivo de verificação vem vazio.
- Uma remoção do qg interrompida no meio é desfeita na próxima execução, sem deixar a instalação quebrada.

## 0.0.19 — 2026-11-14

### Novidades

- Mensagens na fila: o que você envia enquanto o modelo trabalha entra logo depois da ação atual, sem esperar a resposta terminar. Cada mensagem aparece no histórico, esmaecida, até ser lida, e ↑ edita a última. `Ctrl+Enter` ou `/agora` manda na hora; Esc duas vezes devolve a fila para a caixa de texto.
- `/statusline`: você descreve a barra de status que quer e o modelo monta, com prévia antes de salvar. Dá para escolher os campos (modelo, contexto, cache, custo, branch e outros) ou usar um script seu, que recebe os dados da sessão e roda com tempo limite, sem deixar processo sobrando.
- Subagentes visíveis: cada subagente ganha uma linha com tempo, tokens e custo, atualizada ao vivo. Enter abre a conversa dele, e dá para pará-lo dali.
- `/mcp`: mostra o estado de cada servidor MCP, o motivo de uma falha e as ferramentas de cada um. Na configuração, cada servidor aceita `enabled_tools`, `disabled_tools`, tempo limite próprio e variáveis de ambiente. Servidores declarados no projeto pedem sua aprovação uma vez antes de rodar.
- Skills na caixa de texto: Enter numa skill do menu coloca a skill no texto, sem enviar, e dá para juntar várias na mesma mensagem.
- Skills do ambiente: o qg reconhece sozinho onde está rodando e deixa a skill desse ambiente sempre à vista do modelo. O `qg doctor` mostra o que encontrou.
- Cache à vista: a barra de status e a aba de uso mostram quanto da conversa veio do cache do provedor.

### Mudanças

- Conversas mais baratas: quando o projeto muda no meio da conversa, o qg manda só o que mudou e mantém igual o começo das mensagens, para o provedor aproveitar o cache. Numa conversa real de 7 turnos, cada chamada saiu pela metade do preço e a conversa terminou 30% menor.
- Erros do provedor: o qg tenta de novo sozinho até 10 vezes, com contagem regressiva na tela, e Esc cancela. Uma resposta cortada no meio é descartada e refeita, em vez de ficar pela metade.
- MCP mais estável: um servidor que cai reconecta sozinho, uma chamada nunca roda duas vezes, e um servidor fora do ar não trava mais a abertura do qg; ele segue tentando em segundo plano.
- Diff no topo: os números do diff e do PR atualizam logo depois de cada ferramenta, e sem PR aberto o diff é contra a branch principal do repositório.
- Saídas muito grandes de ferramentas são conferidas bem mais rápido na hora de esconder segredos, sem deixar nenhum passar.
- Go atualizado, com as falhas de segurança conhecidas corrigidas.

### Correções

- No Ubuntu 24.04 e em outros Linux que bloqueiam o ambiente isolado, o qg percebe isso ao abrir e avisa uma vez na tela que os comandos rodam sem isolamento; o `qg doctor` explica como resolver. Antes, os comandos falhavam com erro de rede ou de permissão sem dizer o motivo.

## 0.0.18 — 2026-11-11

### Novidades

- Retomada exata de sessão: `qg --continue`, `qg --resume`, `-c` e `/resume` voltam exatamente para onde a conversa parou, sem resumo, sem compactar e sem chamar o modelo. Ao sair, o qg mostra `Para continuar: qg --continue <id>`; sem id, o `--continue` pega a sessão mais recente da pasta e o `--resume` abre a lista. Vale depois de fechar o terminal, de um kill, de cancelar com Esc e de trocar de modelo: o modelo, o esforço e o provedor voltam como estavam, e o turno seguinte continua do histórico real. Se a conversa mudou desde o último salvamento, o qg avisa em português o que recuperou (por exemplo `Turno interrompido recuperado: 3 passos restaurados`).
- Proteção contra dois terminais na mesma sessão: se a sessão já está aberta em outro terminal, o segundo qg recusa em português dizendo o PID (`Esta sessão está aberta em outro terminal (PID 123). Feche lá ou use /fork.`) em vez de misturar as duas conversas. Vale para o TUI e para o `qg exec --resume`; o `--fork` continua liberado, porque só lê a sessão original. No Windows, a recusa agora também mostra o PID do terminal que segura a sessão (antes só mostrava no Linux e no macOS).
- Animação de trabalho com palavras que mudam: enquanto o modelo trabalha, a palavra troca a cada 3 segundos entre Quebrando o Galho, Lapidando, Cozinhando 🔥, Trabalhando, Pensando, Dando um jeito, No capricho, Fuçando e Caprichando (sorteada a cada resposta). Com movimento reduzido, fica `Trabalhando` fixo.

### Desempenho

- `/resume` mais rápido em sessões com muitos arquivos: a escolha da cor do código agora usa um índice por extensão em vez de varrer todos os padrões a cada arquivo. Numa sessão de 82 turnos, a primeira tela cai de 0,40 s para 0,25 s, com ~20% menos CPU. Nenhuma cor muda.
- Desenho da conversa com menos alocações: a chave do cache de linhas e de alturas não copia mais o texto inteiro de cada linha (acerto no cache passa de 10 alocações para 0) e a animação de trabalho foi memorizada (130 para 29 alocações por quadro). Na tela, nada muda.

## 0.0.17 — 2026-11-06

### Mudanças

- O aviso de versão nova aparece já na primeira vez que você abre o qg depois de uma versão sair. Ao abrir, se a última verificação tiver mais de 10 minutos, o qg pergunta se há versão nova e espera no máximo 1,5 segundo; se a internet estiver lenta ou fora, ele abre normal e verifica em segundo plano. A verificação agora acontece a cada 10 minutos, não mais a cada hora.

## 0.0.16 — 2026-11-06

### Novidades

- Velocidade de tokens: enquanto o modelo escreve, a borda de cima da caixa de texto mostra a velocidade ao lado do nome do modelo, por exemplo `● 60 tok/s · modelo`; ao terminar, o número fica apagado. O recibo de cada resposta mostra a velocidade no lugar da contagem de tokens.
- Tela inicial nova: um cartão com o mascote, `quebragalho.dev`, o slogan "democratizando a ia. ♥" e os detalhes da sessão (pasta, branch, modelo e versão). O `qg --version` mostra o nome em letras grandes, ou em texto em terminal estreito. O tema escuro ganhou um lima novo e cinzas novos.

### Mudanças

- O pacote do npm agora se chama `@quebragalho/cli`. Quem instalou pelo nome antigo, `@quebragalho-dev/cli`, passa para o nome novo sozinho ao atualizar (`qg upgrade` ou o aviso ao abrir); a chave salva e as configurações continuam. Para instalar do zero: `npm install -g @quebragalho/cli`.

### Correções

- Sessões longas não travam mais: rolar, passar o mouse e digitar voltam a responder na hora. Antes, em sessões com centenas de mensagens, uma letra digitada podia demorar quase 2 minutos para aparecer.

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
