package age

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBech32Vectors(t *testing.T) {
	// vetores válidos do BIP 173
	for _, s := range []string{"A12UEL5L", "a12uel5l", "abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw",
		"split1checkupstagehandshakeupstreamerranterredcaperred2y9e3w"} {
		if _, _, err := bech32Decode(s); err != nil {
			t.Errorf("%q deveria valer: %v", s, err)
		}
	}
	for _, s := range []string{"A12UEL5l", "a12uel5m", "1qzzfhee", "pzry9x0s0muk"} {
		if _, _, err := bech32Decode(s); err == nil {
			t.Errorf("%q não deveria valer", s)
		}
	}
	data := []byte{0, 1, 2, 250, 255}
	enc, _ := bech32Encode("teste", data)
	if hrp, got, err := bech32Decode(enc); err != nil || hrp != "teste" || !bytes.Equal(got, data) {
		t.Fatalf("ida e volta: %q %v %v", hrp, got, err)
	}
}

func TestKeys(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pub, priv := id.Recipient().String(), id.String()
	if !strings.HasPrefix(pub, "age1") || len(pub) != 62 || !strings.HasPrefix(priv, "AGE-SECRET-KEY-1") || strings.ToUpper(priv) != priv {
		t.Fatalf("formato das chaves: %s %s", pub, priv[:20])
	}
	r, err := ParseRecipient(pub)
	if err != nil || r.String() != pub {
		t.Fatalf("pública: %v", err)
	}
	id2, err := ParseIdentity("# created: hoje\n# public key: " + pub + "\n" + priv + "\n")
	if err != nil || id2.String() != priv {
		t.Fatalf("privada do arquivo do age-keygen: %v", err)
	}
	for _, bad := range []string{"", "age1abc", priv, strings.Replace(pub, "age1", "agf1", 1)} {
		if _, err := ParseRecipient(bad); err == nil {
			t.Errorf("pública inválida aceita: %q", bad)
		}
	}
	if _, err := ParseIdentity(pub); err == nil {
		t.Error("pública não serve como privada")
	}
}

func TestRoundTripSizes(t *testing.T) {
	id, _ := GenerateIdentity()
	for _, n := range []int{0, 1, 100, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17, 2 * chunkSize} {
		plain := make([]byte, n)
		rand.Read(plain)
		enc, err := EncryptBytes(plain, id.Recipient())
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecryptBytes(enc, id)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%d bytes: %v", n, err)
		}
		// tamanho exato do formato: cabeçalho + nonce + blocos com 16 bytes de tag
		chunks := (n + chunkSize - 1) / chunkSize
		if chunks == 0 {
			chunks = 1
		}
		if hdr := bytes.Index(enc, []byte("\n--- ")); hdr < 0 || len(enc)-(bytes.IndexByte(enc[hdr+1:], '\n')+hdr+2) != nonceLen+n+chunks*tagLen {
			t.Fatalf("%d bytes: tamanho do conteúdo cifrado fora do formato", n)
		}
	}
}

func TestStreamingWritesInPieces(t *testing.T) {
	id, _ := GenerateIdentity()
	plain := make([]byte, 5*chunkSize+123)
	rand.Read(plain)
	var buf bytes.Buffer
	w, _ := Encrypt(&buf, id.Recipient())
	for i := 0; i < len(plain); i += 7777 {
		w.Write(plain[i:min(i+7777, len(plain))])
	}
	w.Close()
	got, err := DecryptBytes(buf.Bytes(), id)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("escrita em pedaços: %v", err)
	}
}

func TestTamperAndWrongKey(t *testing.T) {
	id, _ := GenerateIdentity()
	other, _ := GenerateIdentity()
	plain := bytes.Repeat([]byte("dado importante "), 9000)
	enc, _ := EncryptBytes(plain, id.Recipient())
	if _, err := DecryptBytes(enc, other); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("outra chave: %v", err)
	}
	for _, at := range []int{30, len(enc) / 2, len(enc) - 1} {
		bad := append([]byte{}, enc...)
		bad[at] ^= 1
		if _, err := DecryptBytes(bad, id); err == nil {
			t.Fatalf("alterado no byte %d e abriu", at)
		}
	}
	if _, err := DecryptBytes(enc[:len(enc)-100], id); err == nil {
		t.Fatal("cortado no fim e abriu")
	}
	// tirar o último bloco inteiro (truncar na fronteira) também é pego
	hdrEnd := bytes.Index(enc, []byte("\n--- "))
	body := hdrEnd + bytes.IndexByte(enc[hdrEnd+1:], '\n') + 2 + nonceLen
	if _, err := DecryptBytes(enc[:body+chunkSize+tagLen], id); err == nil {
		t.Fatal("sem o bloco final e abriu")
	}
}

// Com a ferramenta oficial no PATH (age), confere que os dois lados se entendem.
func TestOfficialAgeInterop(t *testing.T) {
	bin, err := exec.LookPath("age")
	if err != nil {
		t.Skip("ferramenta age não instalada")
	}
	dir := t.TempDir()
	id, _ := GenerateIdentity()
	keyFile := filepath.Join(dir, "chave.txt")
	os.WriteFile(keyFile, []byte(id.String()+"\n"), 0o600)
	plain := make([]byte, 3*chunkSize+999)
	rand.Read(plain)

	enc, _ := EncryptBytes(plain, id.Recipient())
	encFile := filepath.Join(dir, "nosso.age")
	os.WriteFile(encFile, enc, 0o600)
	out, err := exec.Command(bin, "-d", "-i", keyFile, encFile).Output()
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("o age oficial não abriu o nosso arquivo: %v", err)
	}

	cmd := exec.Command(bin, "-r", id.Recipient().String())
	cmd.Stdin = bytes.NewReader(plain)
	theirs, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecryptBytes(theirs, id); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("não abrimos o arquivo do age oficial: %v", err)
	}
}
