package web

import (
	"strings"
	"testing"
	"time"
)

// Vetores da RFC 6238 (SHA-1, segredo "12345678901234567890"), nos 6 últimos dígitos.
func TestTOTPRFCVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471",
		1234567890: "005924", 2000000000: "279037", 20000000000: "353130",
	} {
		if got := totpCode(secret, ts/totpPeriod); got != want {
			t.Errorf("t=%d: %s, queria %s", ts, got, want)
		}
	}
}

func TestMatchTOTPWindowAndReplay(t *testing.T) {
	sec := newTOTPSecret()
	raw, _ := b32.DecodeString(sec)
	now := time.Unix(1_800_000_000, 0)
	step := now.Unix() / totpPeriod
	for d := int64(-1); d <= 1; d++ {
		if got := matchTOTP(sec, totpCode(raw, step+d), now, 0); got != step+d {
			t.Fatalf("intervalo %+d deveria valer (relógio fora até 30 s)", d)
		}
	}
	if matchTOTP(sec, totpCode(raw, step+2), now, 0) != -1 || matchTOTP(sec, totpCode(raw, step-2), now, 0) != -1 {
		t.Fatal("fora da janela não vale")
	}
	code := totpCode(raw, step)
	if matchTOTP(sec, code[:3]+" "+code[3:], now, 0) != step {
		t.Fatal("espaço no meio do código (como o app mostra) deveria valer")
	}
	if matchTOTP(sec, code, now, step) != -1 {
		t.Fatal("o mesmo código não pode valer duas vezes")
	}
	if matchTOTP(sec, "12345", now, 0) != -1 || matchTOTP("lixo!", code, now, 0) != -1 {
		t.Fatal("código curto ou segredo inválido")
	}
}

func TestRecoveryCodesSingleUse(t *testing.T) {
	plain, hashes := newRecoveryCodes()
	if len(plain) != recoveryN || len(hashes) != recoveryN || len(plain[0]) != 9 || plain[0][4] != '-' {
		t.Fatalf("códigos: %v", plain)
	}
	left, ok := useRecovery(hashes, strings.ToUpper(plain[3])) // sem diferenciar maiúscula
	if !ok || len(left) != recoveryN-1 {
		t.Fatal("código de recuperação deveria valer")
	}
	if _, ok := useRecovery(left, plain[3]); ok {
		t.Fatal("código de recuperação é de uso único")
	}
	if _, ok := useRecovery(left, strings.ReplaceAll(plain[4], "-", "")); !ok {
		t.Fatal("sem o hífen também vale")
	}
}

func TestOtpauthURI(t *testing.T) {
	u := otpauthURI("VPServer (meu-servidor)", "ana", "JBSWY3DPEHPK3PXP")
	if !strings.HasPrefix(u, "otpauth://totp/VPServer%20%28meu-servidor%29:ana?") || !strings.Contains(u, "secret=JBSWY3DPEHPK3PXP") ||
		!strings.Contains(u, "issuer=VPServer+%28meu-servidor%29") || !strings.Contains(u, "digits=6") {
		t.Fatalf("uri: %s", u)
	}
	if groupSecret("ABCDEFGHIJ") != "ABCD EFGH IJ" {
		t.Fatal("agrupar")
	}
}
