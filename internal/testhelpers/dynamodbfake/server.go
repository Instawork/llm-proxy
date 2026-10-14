// Package dynamodbfake provides a tiny in-memory HTTP server that mimics the
// subset of the DynamoDB JSON-RPC surface our test suites need.  It exists so
// every package that talks to DynamoDB (apikeys, cost, etc.) can share a
// single fake instead of each test file forking its own copy and drifting.
//
// The server dispatches on the `X-Amz-Target` header (e.g. PutItem) and is
// intentionally NOT a full-fidelity fake — it returns canned shapes that are
// just accurate enough for the AWS SDK's marshalling layer to round-trip.
//
// Typical usage:
//
//	fake := dynamodbfake.New(t)
//	dynamodbfake.UseFakeDynamo(t, fake.URL())
//	store, err := NewStore(StoreConfig{TableName: "x", Region: "us-west-2"})
//	...
//	// optional: fake.FailOnce("PutItem", errors.New("ConditionalCheckFailedException"))
package dynamodbfake

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Server is an in-memory DynamoDB fake. Tests use a fresh Server per
// test via New(t); the t.Cleanup hook tears down the underlying
// httptest.Server when the test ends.
//
// httptest.Server runs handlers on multiple goroutines (each accepted
// connection becomes its own goroutine). The tables map is now guarded
// by mu so a single test that issues concurrent SDK calls (e.g. a
// goroutined CreateKey loop) does not race the map under -race. Prior
// to this every code path went through the same goroutine in the test
// body, but adding the race detector to CI exposes the issue
// immediately.
type Server struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	tables  map[string]map[string]any  // table -> pk -> item
	indexes map[string]map[string]bool // table -> gsi name -> present
	failOn  map[string]error           // op -> next-call error
}

// New starts a fake DynamoDB server and registers a t.Cleanup hook that
// closes the underlying httptest.Server when the test finishes.
func New(t *testing.T) *Server {
	t.Helper()
	f := &Server{
		t:       t,
		tables:  make(map[string]map[string]any),
		indexes: make(map[string]map[string]bool),
		failOn:  make(map[string]error),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// URL returns the base URL of the fake server.  Pass it to UseFakeDynamo
// (or set AWS_ENDPOINT_URL_DYNAMODB directly) so the AWS SDK targets the
// fake instead of real AWS.
func (f *Server) URL() string { return f.srv.URL }

// FailOnce queues a one-shot error for the next request whose
// X-Amz-Target operation suffix matches op.  After firing, the entry is
// removed; subsequent requests for the same op succeed normally.
//
// The error string is wrapped in DynamoDB's standard `__type` envelope so
// the AWS SDK classifies it as a service error (e.g. "ResourceNotFoundException").
func (f *Server) FailOnce(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[op] = err
}

// InjectItem stores a raw DynamoDB-encoded item directly so tests can
// drive error branches (disabled / expired entries) without going through
// the PutItem path.  Item values must already be in AWS-SDK attribute
// form (e.g. {"S": "value"}).
func (f *Server) InjectItem(table, pk string, item map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tables[table] == nil {
		f.tables[table] = make(map[string]any)
	}
	storageKey := storageKeyFromAttrs(item)
	if storageKey == "" {
		storageKey = pk
	}
	f.tables[table][storageKey] = item
}

func (f *Server) handle(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	op := target
	if i := strings.LastIndex(target, "."); i >= 0 {
		op = target[i+1:]
	}

	f.mu.Lock()
	if err, ok := f.failOn[op]; ok {
		delete(f.failOn, op)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#` + err.Error() + `"}`))
		return
	}
	f.mu.Unlock()

	body, _ := io.ReadAll(r.Body)
	defer r.Body.Close() //nolint:errcheck
	var input map[string]any
	_ = json.Unmarshal(body, &input)

	w.Header().Set("Content-Type", "application/x-amz-json-1.0")

	f.mu.Lock()
	defer f.mu.Unlock()

	switch op {
	case "DescribeTable":
		tableName, _ := input["TableName"].(string)
		_ = json.NewEncoder(w).Encode(f.describeTableResponse(tableName))
	case "CreateTable":
		tableName, _ := input["TableName"].(string)
		if f.indexes[tableName] == nil {
			f.indexes[tableName] = make(map[string]bool)
		}
		if gsis, ok := input["GlobalSecondaryIndexes"].([]any); ok {
			for _, raw := range gsis {
				gsi, _ := raw.(map[string]any)
				name, _ := gsi["IndexName"].(string)
				if name != "" {
					f.indexes[tableName][name] = true
				}
			}
		}
		_, _ = w.Write([]byte(`{"TableDescription":{"TableStatus":"ACTIVE"}}`))
	case "UpdateTable":
		tableName, _ := input["TableName"].(string)
		if f.indexes[tableName] == nil {
			f.indexes[tableName] = make(map[string]bool)
		}
		if updates, ok := input["GlobalSecondaryIndexUpdates"].([]any); ok {
			for _, raw := range updates {
				update, _ := raw.(map[string]any)
				if create, ok := update["Create"].(map[string]any); ok {
					name, _ := create["IndexName"].(string)
					if name != "" {
						f.indexes[tableName][name] = true
					}
				}
			}
		}
		_, _ = w.Write([]byte(`{"TableDescription":{"TableStatus":"ACTIVE"}}`))
	case "PutItem":
		tableName, _ := input["TableName"].(string)
		item, _ := input["Item"].(map[string]any)
		storageKey := storageKeyFromAttrs(item)
		if f.tables[tableName] == nil {
			f.tables[tableName] = make(map[string]any)
		}
		conditionExpr, _ := input["ConditionExpression"].(string)
		if strings.Contains(conditionExpr, "attribute_not_exists") && f.tables[tableName][storageKey] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException"}`))
			return
		}
		f.tables[tableName][storageKey] = item
		_, _ = w.Write([]byte(`{}`))
	case "GetItem":
		tableName, _ := input["TableName"].(string)
		key, _ := input["Key"].(map[string]any)
		storageKey := storageKeyFromAttrs(key)
		item := f.tables[tableName][storageKey]
		if item == nil {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Item": item})
	case "DeleteItem":
		tableName, _ := input["TableName"].(string)
		key, _ := input["Key"].(map[string]any)
		storageKey := storageKeyFromAttrs(key)
		conditionExpr, _ := input["ConditionExpression"].(string)
		if strings.Contains(conditionExpr, "attribute_exists") && f.tables[tableName][storageKey] == nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException"}`))
			return
		}
		if f.tables[tableName] != nil {
			delete(f.tables[tableName], storageKey)
		}
		_, _ = w.Write([]byte(`{}`))
	case "UpdateItem":
		tableName, _ := input["TableName"].(string)
		key, _ := input["Key"].(map[string]any)
		storageKey := storageKeyFromAttrs(key)
		conditionExpr, _ := input["ConditionExpression"].(string)
		if f.tables[tableName] == nil {
			f.tables[tableName] = make(map[string]any)
		}
		item, exists := f.tables[tableName][storageKey]
		if strings.Contains(conditionExpr, "attribute_exists") && !exists {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException"}`))
			return
		}
		itemMap := map[string]any{}
		if exists {
			itemMap, _ = item.(map[string]any)
		}
		if !evaluateUpdateCondition(conditionExpr, itemMap, input) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException"}`))
			return
		}
		applyUpdateExpression(itemMap, input)
		f.tables[tableName][storageKey] = itemMap
		returnValues, _ := input["ReturnValues"].(string)
		if returnValues == "ALL_NEW" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Attributes": itemMap})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	case "Query", "Scan":
		tableName, _ := input["TableName"].(string)
		filterExpr, _ := input["FilterExpression"].(string)
		keyCondExpr, _ := input["KeyConditionExpression"].(string)
		attrValues, _ := input["ExpressionAttributeValues"].(map[string]any)
		var items []any
		for _, it := range f.tables[tableName] {
			item, _ := it.(map[string]any)
			if keyCondExpr != "" && strings.Contains(keyCondExpr, "owner_email") {
				wantOwner := extractAttrValueString(attrValues, ":owner")
				if wantOwner != "" && ExtractDDBString(item, "owner_email") != wantOwner {
					continue
				}
				if strings.Contains(keyCondExpr, "provider =") {
					wantProvider := extractAttrValueString(attrValues, ":provider")
					if wantProvider != "" && !strings.EqualFold(ExtractDDBString(item, "provider"), wantProvider) {
						continue
					}
				}
			}
			if keyCondExpr != "" && strings.Contains(keyCondExpr, "provider =") && !strings.Contains(keyCondExpr, "owner_email") {
				wantProvider := extractAttrValueString(attrValues, ":provider")
				if wantProvider != "" && !strings.EqualFold(ExtractDDBString(item, "provider"), wantProvider) {
					continue
				}
			}
			if filterExpr != "" && strings.Contains(filterExpr, "sk =") {
				wantSK := extractAttrValueString(attrValues, ":profile")
				if wantSK != "" && ExtractDDBString(item, "sk") != wantSK {
					continue
				}
			}
			if filterExpr != "" && strings.Contains(filterExpr, "begins_with") {
				pkVal := ExtractDDBString(item, "pk")
				prefixes := []string{
					extractAttrValueString(attrValues, ":pfx"),
					extractAttrValueString(attrValues, ":skPfx"),
					extractAttrValueString(attrValues, ":legacyPfx"),
				}
				hasPrefixFilter := false
				matched := false
				for _, pfx := range prefixes {
					if pfx == "" {
						continue
					}
					hasPrefixFilter = true
					if strings.HasPrefix(pkVal, pfx) {
						matched = true
						break
					}
				}
				if hasPrefixFilter && !matched {
					continue
				}
			}
			if filterExpr != "" && strings.Contains(filterExpr, "share_api_key") {
				item, _ := it.(map[string]any)
				wantKey := extractAttrValueString(attrValues, ":key")
				if wantKey != "" && ExtractDDBString(item, "share_api_key") != wantKey {
					continue
				}
			}
			items = append(items, it)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "Count": len(items)})
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"unsupported_op:` + op + `"}`))
	}
}

// ExtractDDBString reads a top-level DynamoDB attribute encoded as
// `{"S": "value"}` and returns the underlying string, or "" if the
// attribute is missing or of a different shape.
func ExtractDDBString(m map[string]any, k string) string {
	if v, ok := m[k].(map[string]any); ok {
		if s, ok := v["S"].(string); ok {
			return s
		}
	}
	return ""
}

func storageKeyFromAttrs(m map[string]any) string {
	pk := ExtractDDBString(m, "pk")
	sk := ExtractDDBString(m, "sk")
	if sk != "" {
		return pk + "\x00" + sk
	}
	return pk
}

func extractAttrValueString(attrs map[string]any, key string) string {
	if attrs == nil {
		return ""
	}
	if v, ok := attrs[key].(map[string]any); ok {
		if s, ok := v["S"].(string); ok {
			return s
		}
	}
	return ""
}

func resolveAttrName(exprNames map[string]any, field string) string {
	field = strings.TrimSpace(field)
	if after, ok := strings.CutPrefix(field, "#"); ok {
		if exprNames == nil {
			return after
		}
		if mapped, ok := exprNames[field].(string); ok {
			return mapped
		}
	}
	return field
}

func evaluateUpdateCondition(conditionExpr string, item map[string]any, input map[string]any) bool {
	conditionExpr = strings.TrimSpace(conditionExpr)
	if conditionExpr == "" {
		return true
	}
	exprNames, _ := input["ExpressionAttributeNames"].(map[string]any)
	exprValues, _ := input["ExpressionAttributeValues"].(map[string]any)

	// A bare attribute_not_exists(field) fails when the stored item already
	// carries that attribute (write-once fields such as first_request_at).
	// Compound expressions (e.g. "... OR lease_expires_at < :now") are not
	// modelled and keep passing, as before.
	if rest, ok := strings.CutPrefix(conditionExpr, "attribute_not_exists("); ok && strings.HasSuffix(rest, ")") && !strings.Contains(rest, " ") {
		field := resolveAttrName(exprNames, strings.TrimSuffix(rest, ")"))
		if _, present := item[field]; present {
			return false
		}
	}

	if strings.Contains(conditionExpr, "#status = :") {
		statusField := resolveAttrName(exprNames, "#status")
		actual := ExtractDDBString(item, statusField)
		for valKey := range exprValues {
			if strings.Contains(conditionExpr, "= "+valKey) {
				expected := extractAttrValueString(exprValues, valKey)
				if actual != expected {
					return false
				}
			}
		}
	}
	return true
}

func applyUpdateExpression(item map[string]any, input map[string]any) {
	updateExpr, _ := input["UpdateExpression"].(string)
	exprNames, _ := input["ExpressionAttributeNames"].(map[string]any)
	exprValues, _ := input["ExpressionAttributeValues"].(map[string]any)
	if updateExpr == "" {
		return
	}

	for _, clause := range splitUpdateClauses(updateExpr) {
		switch clause.action {
		case "REMOVE":
			for field := range strings.SplitSeq(clause.body, ",") {
				delete(item, resolveAttrName(exprNames, strings.TrimSpace(field)))
			}
		case "SET":
			for assignment := range strings.SplitSeq(clause.body, ",") {
				parts := strings.SplitN(strings.TrimSpace(assignment), "=", 2)
				if len(parts) != 2 {
					continue
				}
				field := resolveAttrName(exprNames, strings.TrimSpace(parts[0]))
				if val, ok := exprValues[strings.TrimSpace(parts[1])].(map[string]any); ok {
					item[field] = val
				}
			}
		case "ADD", "DELETE":
			// Only string-set operands are modelled, which is all the stores use.
			fields := strings.Fields(clause.body)
			if len(fields) != 2 {
				continue
			}
			field := resolveAttrName(exprNames, fields[0])
			operand, _ := exprValues[fields[1]].(map[string]any)
			applySetOperation(item, field, clause.action, stringSetFrom(operand))
		}
	}
}

type updateClause struct {
	action string
	body   string
}

var updateActions = []string{"SET", "REMOVE", "ADD", "DELETE"}

// splitUpdateClauses splits "SET a = :a ADD b :b" into per-action clauses.
func splitUpdateClauses(expr string) []updateClause {
	words := strings.Fields(expr)
	var clauses []updateClause
	var cur *updateClause
	for _, w := range words {
		if slices.Contains(updateActions, strings.ToUpper(w)) {
			clauses = append(clauses, updateClause{action: strings.ToUpper(w)})
			cur = &clauses[len(clauses)-1]
			continue
		}
		if cur != nil {
			if cur.body != "" {
				cur.body += " "
			}
			cur.body += w
		}
	}
	return clauses
}

func stringSetFrom(av map[string]any) []string {
	raw, _ := av["SS"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if str, ok := v.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

func applySetOperation(item map[string]any, field, action string, operand []string) {
	current := map[string]struct{}{}
	if existing, ok := item[field].(map[string]any); ok {
		for _, v := range stringSetFrom(existing) {
			current[v] = struct{}{}
		}
	}
	for _, v := range operand {
		if action == "ADD" {
			current[v] = struct{}{}
		} else {
			delete(current, v)
		}
	}
	if len(current) == 0 {
		delete(item, field)
		return
	}
	merged := make([]any, 0, len(current))
	for v := range current {
		merged = append(merged, v)
	}
	item[field] = map[string]any{"SS": merged}
}

func (f *Server) describeTableResponse(tableName string) map[string]any {
	gsis := make([]any, 0)
	for name := range f.indexes[tableName] {
		gsis = append(gsis, map[string]any{
			"IndexName":   name,
			"IndexStatus": "ACTIVE",
		})
	}
	return map[string]any{
		"Table": map[string]any{
			"TableStatus":            "ACTIVE",
			"TableName":              tableName,
			"GlobalSecondaryIndexes": gsis,
		},
	}
}

// UseFakeDynamo points the AWS SDK at the given URL via env vars and
// neutralises the SDK's IMDS / config-file lookups so CI runs don't
// accidentally hit real AWS metadata.  All env-var changes are scoped
// to the test via t.Setenv.
func UseFakeDynamo(t *testing.T, url string) {
	t.Helper()
	t.Setenv("AWS_ENDPOINT_URL", url)
	t.Setenv("AWS_ENDPOINT_URL_DYNAMODB", url)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
}
