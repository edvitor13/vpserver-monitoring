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
  **português (pt-BR)**.
- **Go 1.23 só com a biblioteca padrão** (o `go.mod` não tem dependências).
  Front em **HTML/CSS/JS puros**, sem build, embutidos no binário
  (`internal/web/static`). Biblioteca de terceiros só embutida no repositório,
  com licença permissiva (MIT, BSD, Apache), e citada no README (hoje: uPlot e
  qrcode-generator).
- **Permissões** valem na API (`need(...)` em `server.go`), não só na tela:
  administrador pode tudo; "ações" pausa/retoma; "usuários" gerencia
  não-administradores. Toda rota nova declara quem pode chamar.
- **Erro da API:** `{"error": {"code": "...", "message": "..."}}`. O `code` é
  estável (a tela decide por ele); a `message` vai direto para a tela.
- **POST** só com `X-Requested-With: vpmon` e da mesma origem (`sameOrigin`).
- **Cores só por variável CSS** (`app.css`), com tema claro e escuro. Status
  sempre com ícone + texto.
- **Toda tela funciona no celular** (390 px de largura, menu embaixo).
- **Testes:** todo comportamento novo tem teste em Go; mudanças de tela são
  conferidas com captura de tela (computador e celular, claro e escuro).

## O painel e o servidor

- O painel convive com apps em produção: tudo fica em `/opt/vpserver-monitoring`,
  projeto Compose `vpserver-monitoring`, nomes `vpserver-*`, **sem porta
  publicada** (acesso só pelo túnel da Cloudflare). Nunca `prune` global,
  `compose down -v` nem comando em outro projeto.
- **Docker só pelo proxy** (`vpserver-dockerproxy`): leitura e, de escrita,
  apenas pausar/retomar contêiner (`POST /containers/<id>/pause|unpause`) e as
  duas limpezas seguras (`POST /build/prune`, `/images/prune` só com
  `dangling=true`). O próprio painel nunca pode ser pausado. Acrescentar qualquer
  outra escrita é decisão do dono do projeto, não da IA.
- **Limpeza** (`internal/cleanup`): só o que não afeta as apps; nunca volume,
  contêiner, rede, imagem com nome ou em uso. Logs só pelo `vpserver-cleaner`
  (sem rede, só IDs de 64 hex, só `*-json.log*`). A IA **não roda limpeza** em
  servidor de verdade para testar: use o Docker falso e o script com `sh`.
- Pausar é escolha, não problema: contêiner pausado não vira alerta.
- Testes que mexem em contêiner no servidor usam um contêiner descartável
  (`vpserver-pausetest`), nunca uma app de verdade.
- **Vários servidores** (`internal/fleet`): quem chama é sempre o servidor
  conectado; o central **nunca** chama nada nele. O que chega com token é dado de
  fora: limite tamanho, não confie, e o WhatsApp emprestado só manda para os
  destinos do central (nunca para números vindos do outro servidor).

## Comandos

```bash
gofmt -l .                              # tem que sair vazio
go vet ./... && go test ./...
node --check internal/web/static/app.js
python scripts/server.py                # estado do servidor (precisa de scripts/server.conf)
python scripts/server.py deploy         # deploy manual (o normal é o merge na master)
```
