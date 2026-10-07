// Package scripts só existe para testar os scripts do CI com o sh de verdade.
package scripts

import (
	"os/exec"
	"strings"
	"testing"
)

func TestNextVersion(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sem sh nesta máquina")
	}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{""}, "1.0.0"},
		{nil, "1.0.0"},
		{[]string{"v1.4.2"}, "1.4.3"},
		{[]string{"v1.4.2", "bug"}, "1.4.3"},
		{[]string{"v1.4.2", "enhancement"}, "1.5.0"},
		{[]string{"v1.4.2", "bug", "enhancement"}, "1.5.0"},
		{[]string{"v1.4.2", "breaking", "enhancement"}, "2.0.0"},
		{[]string{"v1.4.2", "enhancement", "breaking"}, "2.0.0"},
		{[]string{"v0.9.12", "enhancement"}, "0.10.0"},
		{[]string{"v1.9.9"}, "1.9.10"},
	}
	for _, c := range cases {
		out, err := exec.Command(sh, append([]string{"next-version.sh"}, c.args...)...).CombinedOutput()
		if got := strings.TrimSpace(string(out)); err != nil || got != c.want {
			t.Errorf("%v → %q %v (esperava %s)", c.args, got, err, c.want)
		}
	}
	for _, bad := range []string{"v1.4", "va.b.c", "v1.4.2-rc1", "latest"} {
		if out, err := exec.Command(sh, "next-version.sh", bad).CombinedOutput(); err == nil {
			t.Errorf("%q deveria falhar (veio %q)", bad, out)
		}
	}
}
