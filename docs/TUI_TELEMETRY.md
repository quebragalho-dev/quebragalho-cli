# Telemetria da TUI

A statusline mostra velocidade de geração, contexto, nível de effort, branch e diretório. O recibo de cada chamada mostra o modelo, o total de tokens e o custo. Os totais usam separador de milhar pt-BR, como `12.635 tok`. `/usage` mantém o detalhamento por chamada e conta.

## Contexto

Um `context_length` positivo recebido pelo discovery do provedor ativo tem prioridade. Quando o catálogo QG não fornece esse valor, a TUI aplica a política de fallback documentada abaixo, autorizada para o QG.

| ID do modelo QG | Fallback em tokens |
| --- | ---: |
| `grok-4.7` | 500.000 |
| Demais modelos QG | 1.000.000 |

A statusline mostra `(fallback/docs)` quando usa essa política. O painel `/usage` apresenta o valor da janela sem esse sufixo. Esses limites são referências locais de exibição, não valores live nem garantia da API. Não alteram o planejamento ou a compactação de contexto. Outros provedores sem informação continuam com contexto desconhecido.

O uso da janela considera os tokens de entrada medidos na chamada mais recente do mesmo modelo, incluindo cache uma única vez. Sem essa medição ou sem janela conhecida, a linha fica oculta. O relatório da conta não separa tokens por modelo nem informa custo; por isso o painel não calcula um gasto agregado usando o preço do modelo atual.

## Velocidade

`tk/s` usa tokens de saída reais reportados pelo provedor, divididos pelo tempo de streaming observado desde o primeiro conteúdo, reasoning ou argumento de ferramenta. O denominador tem piso de um segundo. Tokens de entrada e cache não entram na velocidade. Snapshots cumulativos substituem a medição anterior, sem somar chamadas nem gerar cobranças adicionais.

A velocidade só atualiza quando o provedor envia um snapshot de uso. Se ele envia uso apenas no fim, a taxa aparece apenas no fim. Sem tokens de saída ou duração mensurável, a taxa fica oculta. A TUI não deduz velocidade de caracteres ou chunks. Uma nova chamada limpa a medição anterior; retomar uma sessão não recria velocidade histórica.

## Effort

Um nível escolhido pelo usuário é uma preferência local. Um default anunciado pelo discovery aparece com `(API)`. Sem escolha ou default conhecido, a statusline mostra `n/d`.

A opção local `auto` omite `reasoning_effort` na requisição. O booleano `capabilities.reasoning` não confirma níveis individuais. Para modelos QG que anunciam reasoning, o painel permite a preferência local `low`, `medium`, `high`, `xhigh` ou `max`; o painel exibe apenas o nível escolhido, sem afirmar suporte da API.

## Permissões

`--dangerously-skip-permissions` é o nome oficial para iniciar a TUI ou `exec` em modo unsafe. `--skip-permissions-unsafe` permanece como alias compatível. Ambos mantêm as mesmas restrições, incluindo incompatibilidade com `--plan`.

## Statusline customizável

Sem config, a statusline é a embutida (modo, effort, contexto, branch, pasta).
Com `statusline` na config, o usuário escolhe os campos, a ordem e o lado (até
3 linhas), e pode apontar um comando externo cuja saída vira linhas do rodapé.
`/statusline` mostra o estado, `/statusline <pedido>` pede ao modelo para
montar uma nova (sempre com prévia: Enter aplica, Esc cancela),
`/statusline reset` volta ao padrão.

```json
{
  "statusline": {
    "lines": [{ "left": ["mode", "effort"], "right": ["context", "branch"] }],
    "separator": " │ ",
    "command": { "command": "meu-statusline" },
    "timeoutMs": 2000,
    "refreshMs": 5000
  }
}
```

Campos (ids estáveis, em inglês): `mode`, `effort`, `fast`, `voice`,
`background`, `goal`, `context`, `branch`, `update`, `cwd`, `cache` (taxa de
acerto do prompt-cache da sessão: `cache —` antes da 1ª chamada), `model`,
`speed`, `cost` (total da
sessão em R$, só chamadas precificadas em BRL), `time` (duração da sessão).
Ids desconhecidos são ignorados com aviso.

O layout (`lines`, `separator`) pode vir do projeto (`.qg/config.json`); o
`command` vale SÓ na config do usuário — projeto com comando é ignorado com
aviso. `timeoutMs` padrão 2000, `refreshMs` padrão 5000 (mínimo 1000); além do
intervalo, o comando re-roda ao fim de cada chamada.

O comando recebe no stdin um JSON só com fatos de exibição (nunca chaves ou
credenciais) e roda com ambiente reduzido (sem `QG_*`, sem variáveis de
credencial). As chaves estão SEMPRE presentes; valor desconhecido é null:

```json
{
  "session_id": "abc123", "cwd": "/repo", "model": "muse-spark-1.3",
  "provider": "qg", "effort": "high",
  "context": { "used": 161000, "window": 1000000, "pct": 16 },
  "cache": { "hit_pct": 75.0 },
  "cost_brl": 0.12, "branch": "main", "version": "0.9.0", "width": 120
}
```

`cache.hit_pct` usa a escala 0–100 de `context.pct` e é null antes da 1ª
chamada (nunca um zero falso).

A saída (stdout, até 3 linhas) aceita cores ANSI SGR; o resto dos escapes é
removido. A execução é assíncrona (nunca bloqueia o View), com no máximo uma
em voo; timeout mata e mantém o último valor bom com marca discreta. Scripts
aprovados pelo `/statusline` moram em `<config>/qg/statusline/`; o staging
(`<config>/qg/statusline/staging/`) é descartado ao cancelar.
