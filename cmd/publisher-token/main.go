// publisher-token generates a strong service-to-service bearer token for
// the publisher's [tokens] config table.
//
// Why it exists: production tokens must be CSPRNG-sourced random strings.
// Hand-typed tokens are weak and accidentally reused across environments;
// pre-generated tokens land in secret managers (Vault / k8s Secret / AWS
// Secrets Manager) where they belong.
//
// Output is "PREFIX_<hex>". The prefix is purely human-readable audit
// flavor — publisher does not parse it. Defaults to "pubtok".
//
// Usage:
//
//	publisher-token                          # 32-byte hex, prefix pubtok
//	publisher-token -prefix portal -bytes 48 # 48-byte hex, prefix portal
//	publisher-token -count 4                 # generate 4 tokens at once
//
// One token per line; copy into the publisher config / secret store.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
)

func main() {
	prefix := flag.String("prefix", "pubtok", "human prefix written before the random portion")
	nbytes := flag.Int("bytes", 32, "random bytes drawn from CSPRNG (32 = 256 bits)")
	count := flag.Int("count", 1, "how many tokens to emit")
	flag.Parse()

	if *nbytes < 16 {
		fmt.Fprintln(os.Stderr, "refusing to generate token with <16 bytes of entropy")
		os.Exit(2)
	}

	for i := 0; i < *count; i++ {
		raw := make([]byte, *nbytes)
		if _, err := rand.Read(raw); err != nil {
			fmt.Fprintf(os.Stderr, "csprng read: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s_%s\n", *prefix, hex.EncodeToString(raw))
	}
}
