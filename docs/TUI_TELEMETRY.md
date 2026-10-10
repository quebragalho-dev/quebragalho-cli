# Telemetria da TUI

A statusline mostra velocidade de geração, contexto, nível de effort, branch e diretório. O recibo de cada chamada mostra o modelo, o total de tokens e o custo. Os totais usam separador de milhar pt-BR, como `12.635 tok`. `/usage` mantém o detalhamento por chamada e conta.

## Contexto

Um `context_length` positivo recebido pelo discovery do provedor ativo tem prioridade. Quando o catálogo QG não fornece esse valor, a TUI aplica a política de fallback documentada abaixo, autorizada para o QG.

| ID do modelo QG | Fallback em tokens |
| --- | ---: |
| `grok-4.7` | 500.000 |
| Demais modelos QG | 1.000.000 |

A statusline e o painel `/usage` mostram `(fallback/docs)` quando usam essa política. Esses limites são referências locais de exibição, não valores live nem garantia da API. Não alteram o planejamento ou a compactação de contexto. Outros provedores sem informação continuam com contexto desconhecido.

## Velocidade

`tk/s` usa tokens de saída reais reportados pelo provedor, divididos pelo tempo de streaming observado desde o primeiro conteúdo, reasoning ou argumento de ferramenta. O denominador tem piso de um segundo. Tokens de entrada e cache não entram na velocidade. Snapshots cumulativos substituem a medição anterior, sem somar chamadas nem gerar cobranças adicionais.

A velocidade só atualiza quando o provedor envia um snapshot de uso. Se ele envia uso apenas no fim, a taxa aparece apenas no fim. Sem tokens de saída ou duração mensurável, a taxa fica oculta. A TUI não deduz velocidade de caracteres ou chunks. Uma nova chamada limpa a medição anterior; retomar uma sessão não recria velocidade histórica.

## Effort

Um nível escolhido pelo usuário é uma preferência local. Um default anunciado pelo discovery aparece com `(API)`. Sem escolha ou default conhecido, a statusline mostra `n/d`.

A opção local `auto` omite `reasoning_effort` na requisição. O booleano `capabilities.reasoning` não confirma níveis individuais. Para modelos QG que anunciam reasoning, o painel permite a preferência local `low`, `medium`, `high`, `xhigh` ou `max`, com aviso de suporte não confirmado quando a API não publica níveis.

## Permissões

`--dangerously-skip-permissions` é o nome oficial para iniciar a TUI ou `exec` em modo unsafe. `--skip-permissions-unsafe` permanece como alias compatível. Ambos mantêm as mesmas restrições, incluindo incompatibilidade com `--plan`.
