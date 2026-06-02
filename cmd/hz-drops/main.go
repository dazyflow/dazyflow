// Command hz-drops signs Hazyflow marketplace artifacts (drops + integration
// manifests) so they install at the official/verified trust tier, and generates
// the signing keypair.
//
// Trust model (see daemon/trust.go): an artifact is signed with a detached
// Ed25519 signature stored next to it as "<file>.sig" — JSON {key_id, sig}. The
// daemon trusts a key by its PUBLIC half, configured out-of-band in
// HAZYFLOW_TRUSTED_KEYS as "id:tier:publisher:base64key". The private key never
// leaves the publisher and is never committed. The tier is derived from which
// trusted key verified the exact bytes — an artifact can't declare its own tier.
//
//	hz-drops keygen --id hazy-official --publisher "Hazyflow" --out ./keys
//	hz-drops sign   --key ./keys/hazy-official.key --id hazy-official drop.ts ...
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "sign":
		sign(os.Args[2:])
	case "bundle":
		bundle(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hz-drops — sign Hazyflow marketplace artifacts

  hz-drops keygen --id <id> --publisher <name> [--tier official|verified] [--out <dir>]
      Generate an Ed25519 signing keypair. Writes <out>/<id>.key (private,
      keep secret) and prints the HAZYFLOW_TRUSTED_KEYS entry (the public half).

  hz-drops sign --key <id>.key --id <id> <file>...
      Write a detached <file>.sig for each file, signing its exact bytes.

  hz-drops bundle [-o <out.js>] <entry.ts>
      Bundle a drop (its TS/JS + any npm/local imports) into ONE self-contained
      ESM file via esbuild — the authoring step before sign. The result is what
      the daemon runs (Node), so the signature attests to exactly the bundle.
`)
}

// bundle produces the single self-contained drop artifact: esbuild resolves and
// inlines every import (npm + relative) so the runtime never resolves modules.
// Platform=node marks Node built-ins external (the drop runs in Node). The
// drop's `export default { manifest, run }` is preserved as the module default.
func bundle(args []string) {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	out := fs.String("o", "", "output file (default: <entry without ext>.bundle.js)")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "bundle: exactly one entry file is required")
		os.Exit(2)
	}
	entry := fs.Arg(0)
	output := *out
	if output == "" {
		output = strings.TrimSuffix(entry, filepath.Ext(entry)) + ".bundle.js"
	}
	result := esbuild.Build(esbuild.BuildOptions{
		EntryPoints: []string{entry},
		Bundle:      true,
		Format:      esbuild.FormatESModule,
		Platform:    esbuild.PlatformNode,
		Target:      esbuild.ES2020,
		Write:       false,
		LogLevel:    esbuild.LogLevelSilent,
	})
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			loc := ""
			if e.Location != nil {
				loc = fmt.Sprintf(" (%s:%d)", e.Location.File, e.Location.Line)
			}
			fmt.Fprintf(os.Stderr, "bundle error: %s%s\n", e.Text, loc)
		}
		os.Exit(1)
	}
	if len(result.OutputFiles) != 1 {
		fmt.Fprintf(os.Stderr, "bundle: expected 1 output, got %d\n", len(result.OutputFiles))
		os.Exit(1)
	}
	if err := os.WriteFile(output, result.OutputFiles[0].Contents, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "bundle: write %s: %v\n", output, err)
		os.Exit(1)
	}
	fmt.Printf("bundled %s → %s (%d bytes)\n", entry, output, len(result.OutputFiles[0].Contents))
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	id := fs.String("id", "", "key id, e.g. hazy-official (matches the .sig key_id)")
	publisher := fs.String("publisher", "", "display name shown on the trust badge (no ':')")
	tier := fs.String("tier", "official", "trust tier this key confers: official or verified")
	out := fs.String("out", ".", "output directory for the key files")
	_ = fs.Parse(args)

	if *id == "" || *publisher == "" {
		fatal("keygen: --id and --publisher are required")
	}
	if strings.ContainsAny(*id, ": ") {
		fatal("keygen: --id must not contain ':' or spaces")
	}
	if strings.Contains(*publisher, ":") {
		fatal("keygen: --publisher must not contain ':'")
	}
	if *tier != "official" && *tier != "verified" {
		fatal("keygen: --tier must be official or verified")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal("keygen: %v", err)
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		fatal("keygen: %v", err)
	}
	keyPath := filepath.Join(*out, *id+".key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		fatal("keygen: write key: %v", err)
	}
	trusted := fmt.Sprintf("%s:%s:%s:%s", *id, *tier, *publisher, base64.StdEncoding.EncodeToString(pub))
	trustedPath := filepath.Join(*out, *id+".trustedkey")
	if err := os.WriteFile(trustedPath, []byte(trusted+"\n"), 0o644); err != nil {
		fatal("keygen: write trusted key: %v", err)
	}

	fmt.Printf("private key:  %s   (KEEP SECRET — never commit)\n", keyPath)
	fmt.Printf("trusted key:  %s\n\n", trustedPath)
	fmt.Printf("Add this to the daemon's HAZYFLOW_TRUSTED_KEYS (';'-separated):\n\n  %s\n", trusted)
}

// sigFile is the on-disk detached-signature shape. Field tags match
// daemon.Signature so the daemon reads it back verbatim; Sig is []byte → JSON
// base64.
type sigFile struct {
	KeyID string `json:"key_id"`
	Sig   []byte `json:"sig"`
}

func sign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "", "path to the private .key from keygen")
	id := fs.String("id", "", "key id; must match the trusted-key id the daemon knows")
	_ = fs.Parse(args)

	if *keyPath == "" || *id == "" {
		fatal("sign: --key and --id are required")
	}
	files := fs.Args()
	if len(files) == 0 {
		fatal("sign: pass one or more files to sign")
	}
	priv := loadPrivateKey(*keyPath)

	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			fatal("sign: read %s: %v", f, err)
		}
		payload, err := json.MarshalIndent(sigFile{KeyID: *id, Sig: ed25519.Sign(priv, content)}, "", "  ")
		if err != nil {
			fatal("sign: %v", err)
		}
		if err := os.WriteFile(f+".sig", append(payload, '\n'), 0o644); err != nil {
			fatal("sign: write %s.sig: %v", f, err)
		}
		fmt.Printf("signed %s -> %s.sig\n", f, f)
	}
}

func loadPrivateKey(path string) ed25519.PrivateKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		fatal("read key %s: %v", path, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		fatal("key %s: bad base64: %v", path, err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		fatal("key %s: want %d-byte Ed25519 private key, got %d", path, ed25519.PrivateKeySize, len(decoded))
	}
	return ed25519.PrivateKey(decoded)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
