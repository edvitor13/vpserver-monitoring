# Guia para agentes de IA

Instruções para quem trabalha neste repositório com IA (Claude Code e afins).
Leia junto com o [README](README.md). Este arquivo diz **o que não se deduz do
código**.

---

## Fluxo de toda alteração (obrigatório)

Nada é alterado sem issue. Pedidos que chegam no meio do trabalho entram na
mesma entrega quando são do mesmo assunto; assunto novo ganha issue própria.

1. **Issue no GitHub antes de mexer**, em português: contexto, **Pedido**,
   **Como fazer** (o plano, por partes) e **Pronto quando** (lista de
   verificação). Use os rótulos `enhancement`, `bug` ou `documentation`.
   **A etiqueta do PR decide a versão** que o CI cria no merge: `breaking` →
   maior, `enhancement` → menor, qualquer outra → correção. Ponha no PR a
   mesma etiqueta da issue (e `breaking` se mudar algo incompatível).
2. **Branch** a partir da `master`: `feat/<assunto>`, `fix/<assunto>` ou
   `docs/<assunto>`.
3. **Um commit por parte**, em inglês, minúsculo e curto no título
   (`add totp and recovery codes`), com **corpo detalhado**: o que mudou, por
   quê e como foi testado. Todos com `Refs #N`; o último com `Closes #N`.
   **Cada commit compila e passa nos testes sozinho.** Termine com a linha de
   coautoria da IA.
4. **PR** em português, com as seções: Resumo, O que mudou (por parte), Como
   testar, Segurança e riscos, Commits (a lista) e `Closes #N`.
5. **Merge** com `gh pr merge <N> --merge --delete-branch`. O push na `master`
   testa, cria a versão (tag + Release), publica a imagem no ghcr.io e faz o
   deploy sozinho (GitHub Actions). Nunca crie tag ou Release à mão.
6. **Conferir em produção:** o CI verde (test, version, image, deploy), a
   versão nova no `X-VPMon-Version`, o `/healthz` e a função nova funcionando de
   verdade. Diga o que foi conferido e o que não.

## O repositório é público

- **Nenhum dado real** em código, teste, doc, issue, PR ou commit: IP,
  domínio, nome de projetos e servidores de terceiros, token, senha, e-mail,
  número de telefone ou linha real de log. Exemplos sempre fictícios: apps
  `loja`, `portal`, `blog`; servidor `meu-servidor`; IPs `203.0.113.x`;
  domínio `exemplo.com`.
- **Segredos** só no `.env` do servidor e nos segredos do GitHub Actions
  (`VPSERVER_HOST` e `VPSERVER_URL` são segredos de propósito: o GitHub os
  esconde nos logs, que são públicos).
- **Antes de cada push**, varra o que vai subir:
  `git diff --cached | grep -inE "token|senha|password|secret|sk-|eyJ|@[a-z0-9-]+\.[a-z]|[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+"`.
- `scripts/server.conf` (servidor do deploy por pacote) fica fora do Git.

## Convenções

- **Código em inglês**; comentários, documentação e texto de tela em
  **português (pt-BR)**. A tela também fala inglês: todo texto de tela vai
  dentro de `T('…')` (português, valores como `{0}`), e a tradução entra em
  `internal/web/static/i18n/en.js` **no mesmo commit** (o `go test` falha se
  faltar, sobrar, perder `{0}`/tag ou se um texto com acento ficar fora do
  `T()`). Não use `t` como nome da função: `t` já é variável em vários lugares.
  No Go, o texto de tela continua em português e a tradução entra em
  `internal/i18n/en.json` no mesmo commit (o `go test ./internal/i18n/` lista o
  que falta). Texto que a tela usa como código (área, nível, chave) não pode ir
  para o catálogo; prefira `fmt.Sprintf` com o texto inteiro a frases montadas em
  pedaços.
- **Go 1.23 só com a biblioteca padrão** (o `go.mod` não tem dependências).
  Front em **HTML/CSS/JS puros**, sem build, embutidos no binário
  (`internal/web/static`). Biblioteca de terceiros só embutida no repositório,
  com licença permissiva (MIT, BSD, Apache), e citada no README (hoje: uPlot,
  qrcode-generator e a cópia do `golang.org/x/crypto` em `internal/xcrypto`).
- **Permissões** valem na API (`need(...)` em `server.go`), não só na tela:
  administrador pode tudo; "ações" pausa/retoma; "usuários" gerencia
  não-administradores. Toda rota nova declara quem pode chamar.
- **Erro da API:** `{"error": {"code": "...", "message": "..."}}`. O `code` é
  estável (a tela decide por ele); a `message` vai direto para a tela.
- **POST** só com `X-Requested-With: vpmon` e da mesma origem (`sameOrigin`).
- **Cores só por variável CSS** (`app.css`), com tema claro e escuro. Status
  sempre com ícone + texto.
- **Toda tela funciona no celular** (390 px de largura, menu embaixo).
- **Grid de uma coluna sempre com a coluna base** (`grid-template-columns:
  minmax(0, 1fr)`; a lista fica no fim do `app.css`). Sem ela, a coluna
  implícita cresce até o conteúdo mais largo e a página inteira passa da tela
  do celular (aconteceu na Limpeza). Texto longo: `min-width: 0` no filho de
  flex e reticências ou quebra. Antes de entregar tela, meça no celular (390 px)
  com dados longos: `document.documentElement.scrollWidth` igual à largura.
- **Testes:** todo comportamento novo tem teste em Go; mudanças de tela são
  conferidas com captura de tela (computador e celular, claro e escuro).

## O painel e o servidor

- O painel convive com apps em produção: tudo fica em `/opt/vpserver-monitoring`,
  projeto Compose `vpserver-monitoring`, nomes `vpserver-*`, **sem porta
  publicada** (acesso só pelo túnel da Cloudflare). Nunca `prune` global,
  `compose down -v` nem comando em outro projeto.
- **Docker só pelo proxy** (`vpserver-dockerproxy`): leitura e, de escrita,
  apenas pausar/retomar contêiner (`POST /containers/<id>/pause|unpause`), as
  duas limpezas seguras (`POST /build/prune`, `/images/prune` só com
  `dangling=true`) e o `exec` dos backups (`POST /containers/<id>/exec`,
  `/exec/<id>/start`, `GET /exec/<id>/json`; liberado por decisão do dono em
  09/10/2026, para ter backup sem contêiner ajudante). Nunca `inspect` de
  contêiner (mostraria o ambiente). O próprio painel nunca pode ser pausado. Acrescentar qualquer
  outra escrita é decisão do dono do projeto, não da IA.
- **Limpeza** (`internal/cleanup`): só o que não afeta as apps; nunca volume,
  contêiner, rede, imagem com nome ou em uso. Logs só pelo `vpserver-cleaner`
  (sem rede, só IDs de 64 hex, só `*-json.log*`). A IA **não roda limpeza** em
  servidor de verdade para testar: use o Docker falso e o script com `sh`.
- Pausar é escolha, não problema: contêiner pausado não vira alerta.
- Testes que mexem em contêiner no servidor usam um contêiner descartável
  (`vpserver-pausetest`), nunca uma app de verdade.
- **Vários servidores** (`internal/fleet`): quem chama é sempre o servidor
  conectado; o central **nunca** abre conexão com ele (ver/controlar à distância
  desce pelo pedido aberto que ele mesmo deixa). O que chega com token é dado de
  fora: limite tamanho, não confie, e o WhatsApp emprestado só manda para os
  destinos do central (nunca para números vindos do outro servidor).
- **Backups** (`internal/backup`): só administradores. Os comandos de dump são
  fixos (`engines.go`) e rodam com `sh -c` dentro do contêiner do banco; nada
  digitado na tela entra no texto do comando (o nome do banco vai como
  `VPMON_DB`) e senha vai pelo ambiente, não como argumento. O servidor só tem
  a chave pública; a privada aparece uma vez na tela e nunca é guardada nem
  logada. Mudou a cifra? O teste com a ferramenta oficial `age` tem de passar
  (`internal/age`, `TestOfficialAgeInterop`). `internal/xcrypto` é cópia: não
  edite à mão. Nunca rode backup em servidor de verdade para testar: use o
  Docker e o bucket falsos.
- **SSH pela tela** (`internal/sshchat`, `web/ssh.go`): desligado por padrão e
  ligar é decisão do dono, no servidor dele (decisão de 10/10/2026: usuário
  próprio que vira root com `sudo` e a senha; ligar pela tela; código do 2FA para
  ligar e para abrir cada sessão). Só administradores com 2FA; código de
  recuperação não abre SSH; erro de código é 400 (401 derruba o login da tela).
  A senha digitada no campo de senha nunca vai para o registro nem para a IA; o
  que vai para a IA passa pelo `monitor.Redact`. Abrir a tela de ativação não
  tenta login (só a troca de chaves): login de teste só no botão Verificar. A IA
  **nunca liga o SSH nem roda comandos por ele em servidor de verdade** para
  testar: use o servidor falso (`sshchat/sshtest`), o `TestRealSSHD` (OpenSSH de
  verdade, sem root) e contêineres descartáveis com OpenSSH.
- **Ver/controlar à distância:** só com o que o dono do servidor conectado liberou
  (ver, logs, controle total), na lista fechada `RemotePaths`/`RemoteWrites`
  conferida nos dois lados. Usuários, senhas/2FA, IA, WhatsApp, SSH e a conexão
  **nunca** entram nessa lista.

## Comandos

```bash
gofmt -l .                              # tem que sair vazio
go vet ./... && go test ./...
node --check internal/web/static/app.js
python scripts/server.py                # estado do servidor (precisa de scripts/server.conf)
python scripts/server.py deploy         # deploy manual (o normal é o merge na master)
```
