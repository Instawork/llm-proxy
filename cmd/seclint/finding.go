package main

type Finding struct {
	File     string
	Line     int
	Rule     string
	Message  string
	Severity string // error | warn
}
