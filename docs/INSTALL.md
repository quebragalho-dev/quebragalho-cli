# Instalando o QG

Escolha o método que já faz parte da sua rotina. Nenhum deles é o "oficial".

| Método | Comando | Auto-update |
| --- | --- | --- |
| Script de instalação (Linux, macOS) | `curl -fsSL https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.sh \| bash` | `qg upgrade` |
| Script de instalação (Windows) | `irm https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.ps1 \| iex` | `qg upgrade` |
| npm | `npm install -g @quebragalho-dev/cli` | `qg upgrade` (roda npm) |
| Arquivo de release | baixe em [Releases](https://github.com/quebragalho-dev/quebragalho-cli/releases) | `qg upgrade` |
| mise (requer acesso) | `mise use -g ubi:quebragalho-dev/quebragalho-cli` | `mise upgrade` |
| `go install` (requer acesso) | `go install github.com/quebragalho-dev/quebragalho-cli/cmd/qg@latest` | rode de novo |
| Fonte (requer acesso) | `make build` | recompile |

`qg upgrade` segue quem é dono do binário, com uma única exceção que ele não
consegue detectar: delega ao npm quando a instalação é npm, recusa e manda no
`brew upgrade qg` para keg do Homebrew, e substitui o binário no lugar para
todo o resto — inclusive instalação gerenciada pelo mise (a exceção: use
`mise upgrade`).

Os arquivos de release são a base da maioria dos métodos acima: o script de
instalação baixa o arquivo da plataforma publicada no GitHub Release e verifica
o checksum antes de instalar.

## Instalação rápida (curl | sh) — Linux e macOS

```bash
curl -fsSL https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.sh | bash
```

O que o script faz:

1. detecta sua plataforma (`macos`/`linux`) e arquitetura (`x64`/`arm64`);
2. baixa o arquivo `qg-v<versão>-<plataforma>-<arq>.tar.gz` e o `.sha256`
   correspondente da release no GitHub;
3. verifica o checksum (SHA-256) antes de extrair;
4. instala o binário em **`/usr/local/bin/qg`** (modo 755) — ou no diretório
   que você indicar — junto com os helpers opcionais de sandbox;
5. cria o alias real **`/usr/local/bin/quebragalho`** (symlink relativo para
   `qg`);
6. roda `qg --version` para confirmar que o binário instalado executa nesta
   plataforma.

Depois é só:

```bash
qg --version
quebragalho --version   # alias — o mesmo binário
```

### Permissões e diretório alternativo

`/usr/local/bin` normalmente exige permissão de escrita de administrador. Se
o script falhar por permissão, escolha um dos caminhos:

```bash
# opção 1: com sudo
curl -fsSL https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.sh | sudo bash

# opção 2: diretório do usuário (sem sudo)
curl -fsSL https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.sh | QG_INSTALL_DIR="$HOME/.local/bin" bash
```

Flags úteis: `--version <versão>` fixa a release (padrão: `latest`),
`--repo <owner/repo>` muda o repositório, `--install-dir <caminho>` é o mesmo
que `QG_INSTALL_DIR`.

## PATH

O `qg` só responde de qualquer diretório se o diretório de instalação estiver
no `PATH`. `/usr/local/bin` já costuma estar; se não estiver:

```bash
export PATH="/usr/local/bin:$PATH"
```

Para persistir, acrescente a linha ao seu `~/.bashrc`, `~/.zshrc` ou
equivalente. Instalações em `~/.local/bin` também precisam dessa exportação.

O próprio script avisa quando o diretório instalado não está no `PATH`.

## Desinstalar

Remova o binário e o alias do diretório de instalação:

```bash
sudo rm -f /usr/local/bin/qg /usr/local/bin/quebragalho
# instalação em diretório do usuário:
rm -f "$HOME/.local/bin/qg" "$HOME/.local/bin/quebragalho"
```

Se a instalação copiou os helpers de sandbox (Linux), remova-os também:

```bash
sudo rm -f /usr/local/bin/qg-linux-sandbox /usr/local/bin/qg-seccomp
```

Sobram apenas arquivos locais, se você já rodou o `qg`:

```bash
rm -rf ~/.config/qg         # config, tema e plugins (some com as chaves salvas)
rm -rf ~/.local/share/qg    # sessões, skills, cron e tarefas (XDG_DATA_HOME)
rm -rf ~/.local/state/qg    # worktrees locais (XDG_STATE_HOME)
rm -rf ~/Library/Caches/qg  # cache (Linux: ~/.cache/qg)
```

Instalações via npm, mise ou `go install` devem ser removidas pelo próprio
gerenciador (`npm uninstall -g @quebragalho-dev/cli`, `mise uninstall …`).

## npm

```bash
npm install -g @quebragalho-dev/cli
qg
```

O pacote instala **os dois nomes de comando**: `qg` e o alias `quebragalho`.
Suporta Linux e macOS em x64 e arm64 (e Windows em x64). Não há scripts de
instalação nem downloads fora do registro npm. Se as dependências opcionais
forem puladas (`--omit=optional`), o wrapper baixa o binário da release
compatível com a sua plataforma, com verificação de checksum.

Requisitos: Node.js 18+ e acesso de rede ao npm.

## go install

O módulo está no repositório privado (requer acesso).

```bash
go install github.com/quebragalho-dev/quebragalho-cli/cmd/qg@latest
```

Instala como `qg` no seu diretório de binários do Go (`$GOBIN` ou
`~/go/bin`). O alias `quebragalho` não é criado por este método — crie se
quiser:

```bash
ln -s "$(command -v qg)" ~/.local/bin/quebragalho
```

Requer Go 1.26.10+.

## A partir do código-fonte

O clone usa o repositório privado (requer acesso).

Requer Go 1.26.10+.

```bash
git clone https://github.com/quebragalho-dev/quebragalho-cli.git
cd quebragalho-cli
make build     # gera ./qg e ./quebragalho (alias real, mesmos bytes)
./qg
```

`make build` é o caminho curto; o equivalente manual é
`go build -o qg ./cmd/qg && cp qg quebragalho`. Coloque um dos binários num
diretório do `PATH` (`~/.local/bin` é uma boa escolha) para usá-lo de
qualquer lugar.

No Linux, para sandbox nativo, build também os helpers:

```bash
go build -o qg-linux-sandbox ./cmd/qg-linux-sandbox
go build -o qg-seccomp ./cmd/qg-seccomp   # wrapper de compatibilidade opcional
```

Coloque `qg` e `qg-linux-sandbox` no mesmo diretório do `PATH`. No macOS não
é preciso binário extra. No Windows, o próprio `qg.exe` faz o papel de helper.

## Windows PowerShell Script

```powershell
irm https://raw.githubusercontent.com/quebragalho-dev/quebragalho-cli/main/scripts/install.ps1 | iex
```

## mise

Este método usa o repositório privado (requer acesso).

```bash
mise use -g ubi:quebragalho-dev/quebragalho-cli
```

## Formato dos arquivos de release

Os arquivos se chamam:

- `qg-v<versão>-macos-<arq>.tar.gz`
- `qg-v<versão>-linux-<arq>.tar.gz`
- `qg-v<versão>-windows-<arq>.zip`

Alvos suportados: `macos-x64`, `macos-arm64`, `linux-x64`, `linux-arm64`,
`windows-x64` (Windows em ARM roda o build x64 emulação).

Cada arquivo tem um `.sha256` correspondente — os scripts de instalação
baixam os dois, verificam o checksum e só então copiam o binário. Os arquivos
são produzidos localmente com:

```bash
make release   # cross-build dos 4 alvos unix + checksums + verify
```

Dentro do tarball estão o binário `qg`, o symlink `quebragalho` (alias real),
os helpers opcionais, o wrapper do npm, `README.md`, `package.json` e
`VERSION`.

## Atualizando

Confira se há release nova, baixe, verifique e substitua no lugar:

```bash
qg update --check
qg upgrade
```

Veja o [guia de atualização](UPDATE.md) para modos, flags e detalhes por
plataforma.

O `qg upgrade` comporta-se diferente conforme o dono do binário:

- **npm**: roda `npm install -g @quebragalho-dev/cli@latest` para você.
- **Homebrew**: recusa e manda no `brew upgrade qg` (substituir o binário do
  keg seria revertido no próximo `brew upgrade`).
- **todo o resto** (inclusive `/usr/local/bin` do script curl): baixa o
  arquivo de release verificado e substitui o binário no lugar.
