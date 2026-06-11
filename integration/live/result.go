package live

import (
	"fmt"
	"os"
	"strings"
)

type Status int

const (
	StatusPass Status = iota
	StatusSkip
	StatusFail
)

type Result struct {
	Suite   string
	Name    string
	Status  Status
	Detail  string
	Elapsed string
}

func (r Result) Label() string {
	switch r.Status {
	case StatusPass:
		return "PASS"
	case StatusSkip:
		return "SKIP"
	default:
		return "FAIL"
	}
}

func formatResultLine(r Result) string {
	line := fmt.Sprintf("%-4s  %s/%s", r.Label(), r.Suite, r.Name)
	if r.Detail != "" {
		line += " — " + r.Detail
	}
	if r.Elapsed != "" {
		line += " (" + r.Elapsed + ")"
	}
	return line
}

func PrintResult(r Result) {
	fmt.Fprintln(os.Stdout, formatResultLine(r))
}

func PrintResults(results []Result, alreadyPrinted bool) int {
	var pass, skip, fail int
	for _, r := range results {
		switch r.Status {
		case StatusPass:
			pass++
		case StatusSkip:
			skip++
		default:
			fail++
		}
		if !alreadyPrinted {
			PrintResult(r)
		}
	}
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 40))
	fmt.Fprintf(os.Stdout, "%d passed, %d skipped, %d failed\n", pass, skip, fail)
	if fail > 0 {
		return 1
	}
	return 0
}

func passResult(suite, name, detail, elapsed string) Result {
	return Result{Suite: suite, Name: name, Status: StatusPass, Detail: detail, Elapsed: elapsed}
}

func skipResult(suite, name, detail string) Result {
	return Result{Suite: suite, Name: name, Status: StatusSkip, Detail: detail}
}

func failResult(suite, name, detail string) Result {
	return Result{Suite: suite, Name: name, Status: StatusFail, Detail: detail}
}
