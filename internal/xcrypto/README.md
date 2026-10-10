# xcrypto

Cópia **só da parte genérica (Go puro, sem assembly)** de três pacotes do
[`golang.org/x/crypto`](https://pkg.go.dev/golang.org/x/crypto) (licença BSD, ver
`LICENSE`), tirada do `vendor/` do próprio Go 1.23 (o `crypto/tls` usa o mesmo código):

- `chacha20poly1305` (AEAD do formato `age`, usado nos backups)
- `chacha20` e `poly1305` (o que ele usa por dentro)
- `alias` (checagem de sobreposição de buffers)

Por que copiado: o projeto não tem dependências externas (`go.mod` vazio). Mudanças em
relação ao original: caminhos de import, e as restrições de build tiradas para usar
sempre a versão genérica. Nada mais foi alterado; não edite à mão.
