package tlscert

import (
	"crypto/tls"
	"os"
	"strings"
	"testing"
)

func TestEnsureCreatesOnceAndReuses(t *testing.T) {
	dir := t.TempDir() + "/tls"
	c1, k1, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(c1, k1)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Leaf == nil || pair.Leaf.VerifyHostname("localhost") != nil || pair.Leaf.VerifyHostname("127.0.0.1") != nil {
		t.Errorf("certificate should cover localhost and 127.0.0.1")
	}
	if st, _ := os.Stat(k1); st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode().Perm())
	}
	fp1, _ := Fingerprint(c1)
	if _, _, err := Ensure(dir); err != nil {
		t.Fatal(err)
	}
	fp2, _ := Fingerprint(c1)
	if fp1 != fp2 || len(strings.Split(fp1, ":")) != 32 {
		t.Errorf("fingerprint changed or malformed: %s / %s", fp1, fp2)
	}
}
