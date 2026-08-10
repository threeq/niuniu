package service

import "testing"

func TestEnvHasKey(t *testing.T) {
	cases := []struct {
		env    []string
		key    string
		expect bool
	}{
		{nil, "FOO", false},
		{[]string{"FOO=bar"}, "FOO", true},
		{[]string{"FOOBAR=1"}, "FOO", false},
		{[]string{"BAZ=1", "FOO=hi"}, "FOO", true},
		{[]string{"FOO="}, "FOO", true},
		{[]string{"foo=1"}, "FOO", false},
	}
	for i, c := range cases {
		got := EnvHasKey(c.env, c.key)
		if got != c.expect {
			t.Errorf("case %d (%v %q): got %v want %v", i, c.env, c.key, got, c.expect)
		}
	}
}
