// Command relsign makes and uses the ed25519 key that signs aitank release
// checksums, which `aitank update` verifies before installing.
//
//	go run ./tools/relsign genkey            # prints PRIVATE and PUBLIC lines
//	AITANK_SIGNING_KEY=... go run ./tools/relsign sign checksums.txt > checksums.txt.sig
//	go run ./tools/relsign verify <public-key> checksums.txt checksums.txt.sig
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: relsign genkey | sign <file> | verify <pubkey> <file> <sig>")
	}
	switch os.Args[1] {
	case "genkey":
		pub, priv, err := ed25519.GenerateKey(nil)
		check(err)
		fmt.Println("PRIVATE (store as the AITANK_SIGNING_KEY secret, never commit):", base64.StdEncoding.EncodeToString(priv))
		fmt.Println("PUBLIC  (store as the AITANK_UPDATE_PUBKEY variable):", base64.StdEncoding.EncodeToString(pub))
	case "sign":
		if len(os.Args) != 3 {
			fail("usage: relsign sign <file>")
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("AITANK_SIGNING_KEY")))
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			fail("AITANK_SIGNING_KEY is missing or not a base64 ed25519 private key")
		}
		data, err := os.ReadFile(os.Args[2])
		check(err)
		fmt.Println(base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(raw), data)))
	case "verify":
		if len(os.Args) != 5 {
			fail("usage: relsign verify <pubkey> <file> <sig>")
		}
		pub, err := base64.StdEncoding.DecodeString(os.Args[2])
		check(err)
		data, err := os.ReadFile(os.Args[3])
		check(err)
		sigB64, err := os.ReadFile(os.Args[4])
		check(err)
		sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
		check(err)
		if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, data, sig) {
			fail("signature does NOT verify")
		}
		fmt.Println("signature OK")
	default:
		fail("unknown command " + os.Args[1])
	}
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
