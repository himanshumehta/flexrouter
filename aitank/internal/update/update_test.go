package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func tarball(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	tgz := tarball(t, "aitank", []byte("binary"))
	sum := sha256.Sum256(tgz)
	sums := []byte(fmt.Sprintf("%s  aitank_1.0.0_darwin_all.tar.gz\n", hex.EncodeToString(sum[:])))
	sig := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)))
	bin, err := Verify(pub, sums, sig, "aitank_1.0.0_darwin_all.tar.gz", tgz)
	if err != nil || string(bin) != "binary" {
		t.Fatalf("good release rejected: %v", err)
	}
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if _, err := Verify(otherPub, sums, sig, "aitank_1.0.0_darwin_all.tar.gz", tgz); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := Verify(pub, sums, sig, "aitank_1.0.0_darwin_all.tar.gz", append(tgz, 0)); err == nil {
		t.Fatal("tampered archive accepted")
	}
}

func TestLatestPicksNewestAitankRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v9.9.9"},{"tag_name":"aitank-v0.3.0","prerelease":true},{"tag_name":"aitank-v0.2.1"},{"tag_name":"aitank-v0.10.0"},{"tag_name":"aitank-v0.2.0"}]`)
	}))
	defer srv.Close()
	rel, err := Latest(context.Background(), srv.URL)
	if err != nil || rel.Version != "0.10.0" {
		t.Fatalf("got %+v %v", rel, err)
	}
	if !Newer("0.10.0", "0.9.9") || Newer("0.1.0", "0.1.0") || Newer("x", "0.1.0") {
		t.Fatal("Newer")
	}
}

func TestInstallRefusesWithoutKey(t *testing.T) {
	PublicKey = ""
	if _, err := Install(context.Background(), &Release{Version: "1.0.0"}); err == nil {
		t.Fatal("must not install without a verification key")
	}
}
