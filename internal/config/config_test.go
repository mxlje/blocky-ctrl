package config

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`
instances:
  - name: Home DNS
    url: http://10.0.0.2:4000
  - name: " Backup #2 "
    url: https://user:pw@dns2.lan
    readonly: true
  - name: Replica
    url: http://10.0.0.4:4000
    readonly: true
    label: Load balanced
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Instances) != 3 {
		t.Fatalf("got %d instances", len(c.Instances))
	}
	a, b, r := c.Instances[0], c.Instances[1], c.Instances[2]
	if a.ID != "home-dns" || a.ReadOnly || a.Label != "" {
		t.Errorf("first: %+v", a)
	}
	if b.ID != "backup-2" || b.Name != "Backup #2" || !b.ReadOnly || b.Label != "readonly" {
		t.Errorf("second: %+v", b)
	}
	if !r.ReadOnly || r.Label != "Load balanced" {
		t.Errorf("third: %+v", r)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{
		{``, "EOF"},
		{`instances: []`, "no instances"},
		{"instances:\n  - url: http://a:4000", "name is required"},
		{"instances:\n  - name: '!!'\n    url: http://a:4000", "letter or digit"},
		{"instances:\n  - name: A\n    url: a:4000", "url must"},
		{"instances:\n  - name: A\n    url: ftp://a", "url must"},
		{"instances:\n  - name: A b\n    url: http://a\n  - name: a-B\n    url: http://b", "same name"},
		{"instances:\n  - name: A\n    url: http://a\n    readonyl: true", "readonyl"},
		{"instances:\n  - name: A\n    url: http://a\n    label: Backup", "label needs readonly"},
	} {
		_, err := Parse([]byte(tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) = %v, want error containing %q", tc.yaml, err, tc.want)
		}
	}
}
