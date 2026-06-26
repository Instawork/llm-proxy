package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func requireProdAcknowledgement() error {
	if os.Getenv("LLM_PROXY_ALLOW_PROD") == "1" {
		return nil
	}

	accountID := "unknown"
	out, err := exec.Command("aws", "sts", "get-caller-identity", "--query", "Account", "--output", "text").Output()
	if err == nil {
		accountID = strings.TrimSpace(string(out))
	}

	prodAccountID := strings.TrimSpace(os.Getenv("LLM_PROXY_PROD_AWS_ACCOUNT_ID"))
	if prodAccountID != "" && accountID == prodAccountID {
		return fmt.Errorf(
			"refusing to run against production AWS account %s without LLM_PROXY_ALLOW_PROD=1",
			accountID,
		)
	}

	return fmt.Errorf(
		"production bump requires LLM_PROXY_ALLOW_PROD=1 (current AWS account: %s)",
		accountID,
	)
}
