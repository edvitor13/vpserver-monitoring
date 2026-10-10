package age

import (
	"errors"
	"strings"
)

// Bech32 (BIP 173) sem o limite de 90 caracteres, como o age usa nas chaves.

const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var gen = []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

func polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func hrpExpand(hrp string) []byte {
	h := []byte(strings.ToLower(hrp))
	var ret []byte
	for _, c := range h {
		ret = append(ret, c>>5)
	}
	ret = append(ret, 0)
	for _, c := range h {
		ret = append(ret, c&31)
	}
	return ret
}

func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var ret []byte
	acc, bits := uint32(0), uint(0)
	maxv := uint32(1<<to) - 1
	for _, b := range data {
		if uint32(b)>>from != 0 {
			return nil, errors.New("bech32: valor fora da faixa")
		}
		acc = acc<<from | uint32(b)
		bits += from
		for bits >= to {
			bits -= to
			ret = append(ret, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			ret = append(ret, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, errors.New("bech32: preenchimento inválido")
	}
	return ret, nil
}

func bech32Encode(hrp string, data []byte) (string, error) {
	values, err := convertBits(data, 8, 5, true)
	if err != nil {
		return "", err
	}
	hrp = strings.ToLower(hrp)
	pm := polymod(append(append(hrpExpand(hrp), values...), 0, 0, 0, 0, 0, 0)) ^ 1
	var sb strings.Builder
	sb.WriteString(hrp + "1")
	for _, v := range values {
		sb.WriteByte(charset[v])
	}
	for i := 0; i < 6; i++ {
		sb.WriteByte(charset[(pm>>uint(5*(5-i)))&31])
	}
	return sb.String(), nil
}

func bech32Decode(s string) (string, []byte, error) {
	if strings.ToLower(s) != s && strings.ToUpper(s) != s {
		return "", nil, errors.New("bech32: maiúsculas e minúsculas misturadas")
	}
	s = strings.ToLower(s)
	pos := strings.LastIndexByte(s, '1')
	if pos < 1 || pos+7 > len(s) {
		return "", nil, errors.New("bech32: formato inválido")
	}
	hrp := s[:pos]
	for _, c := range hrp {
		if c < 33 || c > 126 {
			return "", nil, errors.New("bech32: prefixo inválido")
		}
	}
	var values []byte
	for i := pos + 1; i < len(s); i++ {
		d := strings.IndexByte(charset, s[i])
		if d < 0 {
			return "", nil, errors.New("bech32: caractere inválido")
		}
		values = append(values, byte(d))
	}
	if polymod(append(hrpExpand(hrp), values...)) != 1 {
		return "", nil, errors.New("bech32: verificação falhou")
	}
	data, err := convertBits(values[:len(values)-6], 5, 8, false)
	return hrp, data, err
}
