package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Instawork/llm-proxy/internal/integrationtoken"
)

func runIntegrationTokenCommand(args []string) {
	os.Exit(integrationTokenCommand(args, os.Stdout, os.Stderr))
}

// integrationTokenCommand mints an integrations API bearer token. It needs
// no config or AWS access: the proxy only ever stores the SHA-256, which the
// operator puts in the caller's token_sha256_env secret.
func integrationTokenCommand(args []string, stdout, stderr io.Writer) int {
	usage := func() {
		fmt.Fprintf(stderr, "Usage: llm-proxy-keys integration-token new -caller <id>\n\n")
		fmt.Fprintf(stderr, "Mints a bearer token for admin_dashboard.integrations.callers[<id>].\n")
		fmt.Fprintf(stderr, "The token is printed once; store only its SHA-256 in the proxy's environment.\n")
	}
	if len(args) == 0 || args[0] != "new" {
		usage()
		return 2
	}

	fs := flag.NewFlagSet("integration-token new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = usage
	caller := fs.String("caller", "", "Caller id from admin_dashboard.integrations.callers (e.g. init)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 || !integrationtoken.ValidCallerID(*caller) {
		fmt.Fprintf(stderr, "-caller must be 1-32 characters of a-z, 0-9, or '-'\n")
		return 2
	}

	token, sum, err := integrationtoken.New(*caller)
	if err != nil {
		fmt.Fprintf(stderr, "failed to generate token: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "This is the only time the token is displayed. Give it to the %s integration through a secrets manager.\n\n", *caller)
	fmt.Fprintf(stdout, "Token:  %s\n", token)
	fmt.Fprintf(stdout, "SHA256: %s\n\n", sum)
	fmt.Fprintf(stdout, "Set the SHA256 value in the env var named by the caller's token_sha256_env.\n")
	fmt.Fprintf(stdout, "To rotate, set \"<new-sha256>,<old-sha256>\" until the integration has switched, then drop the old one.\n")
	return 0
}
