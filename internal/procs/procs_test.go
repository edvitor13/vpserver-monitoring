package procs

import "testing"

func TestParseStatWithSpacesInName(t *testing.T) {
	line := "1195076 (python (worker)) S 1195052 1195076 1195076 0 -1 4194560 9787 0 0 0 699 162 0 0 20 0 7 0 12345 100000 2500 18446744073709551615"
	p, ticks, ok := ParseStat([]byte(line), 4096)
	if !ok || p.Name != "python (worker)" || p.State != "S" || ticks != 861 || p.Threads != 7 || p.RSS != 2500*4096 {
		t.Fatalf("stat: ok=%v %+v ticks=%d", ok, p, ticks)
	}
}

func TestCgroupLeaf(t *testing.T) {
	cases := map[string]string{
		"0::/system.slice/docker-abc.scope\n":              "docker-abc.scope",
		"0::/system.slice/ssh.service\n":                   "ssh.service",
		"0::/user.slice/user-1001.slice/session-5.scope\n": "user.slice",
		"0::/../docker-abc.scope\n":                        "docker-abc.scope",
		"0::/../../user.slice/user-1001.slice/x.scope\n":   "user.slice",
		"0::/init.scope\n":                                 "init.scope",
		"0::/\n":                                           "/",
	}
	for in, want := range cases {
		if got := CgroupLeaf(in); got != want {
			t.Errorf("%q → %q, queria %q", in, got, want)
		}
	}
}
