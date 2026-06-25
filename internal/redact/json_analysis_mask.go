package redact

import (
	"encoding/json"
	"strings"
)

func prepareJSONForAnalysis(text string, adapter ContentAdapter) string {
	if adapter == nil {
		adapter = unionContentAdapter{}
	}
	if !json.Valid([]byte(text)) {
		return text
	}

	runes := []rune(text)
	out := append([]rune(nil), runes...)
	var stack []jsonContainerState
	var path []string
	var pathPushed []bool
	var pendingKey string

	for i := 0; i < len(runes); {
		ch := runes[i]
		if isJSONWhitespace(ch) {
			i++
			continue
		}

		switch ch {
		case '{':
			pushed := pendingKey != ""
			if pushed {
				path = append(path, pendingKey)
				pendingKey = ""
			}
			stack = append(stack, jsonObjectKey)
			pathPushed = append(pathPushed, pushed)
			i++
		case '[':
			pushed := pendingKey != ""
			if pushed {
				path = append(path, pendingKey)
				pendingKey = ""
			}
			stack = append(stack, jsonArrayValue)
			pathPushed = append(pathPushed, pushed)
			i++
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if len(pathPushed) > 0 {
				if pathPushed[len(pathPushed)-1] && len(path) > 0 {
					path = path[:len(path)-1]
				}
				pathPushed = pathPushed[:len(pathPushed)-1]
			}
			markJSONValueSeen(&stack)
			i++
		case ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if len(pathPushed) > 0 {
				if pathPushed[len(pathPushed)-1] && len(path) > 0 {
					path = path[:len(path)-1]
				}
				pathPushed = pathPushed[:len(pathPushed)-1]
			}
			markJSONValueSeen(&stack)
			i++
		case ':':
			if len(stack) > 0 && stack[len(stack)-1] == jsonObjectColon {
				stack[len(stack)-1] = jsonObjectValue
			}
			i++
		case ',':
			if len(stack) > 0 {
				switch stack[len(stack)-1] {
				case jsonObjectCommaOrEnd:
					stack[len(stack)-1] = jsonObjectKey
				case jsonArrayCommaOrEnd:
					stack[len(stack)-1] = jsonArrayValue
				}
			}
			i++
		case '"':
			isKey := len(stack) > 0 && stack[len(stack)-1] == jsonObjectKey
			if isKey {
				keyStart := i
				i = copyOrMaskJSONString(runes, out, i, true)
				pendingKey, _ = readJSONString(runes, keyStart)
				stack[len(stack)-1] = jsonObjectColon
			} else {
				maskValue := shouldMaskJSONStringValue(path, pendingKey, adapter)
				i = copyOrMaskJSONString(runes, out, i, maskValue)
				pendingKey = ""
				markJSONValueSeen(&stack)
			}
		default:
			for i < len(runes) && !isJSONDelimiter(runes[i]) {
				i++
			}
			pendingKey = ""
			markJSONValueSeen(&stack)
		}
	}
	return string(out)
}

func readJSONString(runes []rune, start int) (string, int) {
	var b strings.Builder
	i := start + 1
	for i < len(runes) {
		switch runes[i] {
		case '\\':
			i++
			if i < len(runes) {
				b.WriteRune(runes[i])
				i++
			}
		case '"':
			return b.String(), i + 1
		default:
			b.WriteRune(runes[i])
			i++
		}
	}
	return b.String(), i
}

func shouldMaskJSONStringValue(path []string, key string, adapter ContentAdapter) bool {
	if isMetadataACLKey(key) {
		return true
	}
	if adapter.ScrubString(path, key) {
		return false
	}
	if key == "description" || key == "title" {
		return true
	}
	if key == "name" && (hasPathAncestor(path, "tools") || hasPathAncestor(path, "functions") || hasPathAncestor(path, "tool_calls")) {
		return true
	}
	if hasPathAncestor(path, "enum") {
		return true
	}
	for _, seg := range path {
		if jsonSchemaContainerKey(seg) {
			return true
		}
	}
	return jsonSchemaContainerKey(key)
}

func isMetadataACLKey(key string) bool {
	switch key {
	case "table_acl", "relacl", "column_acl", "acl",
		"privileges", "grants", "table_privileges", "column_privileges",
		"grantee", "defaclacl":
		return true
	default:
		return strings.HasSuffix(key, "_acl")
	}
}

func jsonSchemaContainerKey(seg string) bool {
	switch seg {
	case "input_schema", "parameters", "properties", "items", "definitions", "$defs",
		"schema", "anyOf", "oneOf", "allOf", "function", "functions":
		return true
	default:
		return false
	}
}
