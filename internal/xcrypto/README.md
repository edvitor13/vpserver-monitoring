# xcrypto

Cópia **só da parte genérica (Go puro, sem assembly)** de três pacotes do
[`golang.org/x/crypto`](https://pkg.go.dev/golang.org/x/crypto) (licença BSD, ver
`LICENSE`), tirada do `vendor/` do próprio Go 1.23 (o `crypto/tls` usa o mesmo código):

- `chacha20poly1305` (AEAD do formato `age`, usado nos backups)
- `chacha20` e `poly1305` (o que ele usa por dentro)
- `alias` (checagem de sobreposição de buffers)

E o cliente **SSH** (`ssh/`, da aba SSH pela tela) com o que ele usa (`curve25519/`,
`blowfish/` e `ssh/internal/bcrypt_pbkdf/`), copiados do módulo `golang.org/x/crypto`
**v0.39.0** (que pede Go 1.23, o mesmo do projeto). Só os arquivos de código, sem os testes;
`ssh/mlkem.go` só entra a partir do Go 1.24 (restrição de build dele mesmo). O `ssh` usa os
mesmos `chacha20` e `poly1305` de cima.

Por que copiado: o projeto não tem dependências externas (`go.mod` vazio). Mudanças em
relação ao original: caminhos de import, e as restrições de build tiradas para usar
sempre a versão genérica. Nada mais foi alterado; não edite à mão.
