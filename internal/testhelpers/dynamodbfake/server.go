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
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	tables map[string]map[string]any // table -> pk -> item
	failOn map[string]error          // op -> next-call error
}

// New starts a fake DynamoDB server and registers a t.Cleanup hook that
// closes the underlying httptest.Server when the test finishes.
func New(t *testing.T) *Server {
	t.Helper()
	f := &Server{
		t:      t,
		tables: make(map[string]map[string]any),
		failOn: make(map[string]error),
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
	f.tables[table][pk] = item
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
		// Pretend the table always exists.  Skips the CreateTable +
		// TableExistsWaiter dance during store constructors.
		_, _ = w.Write([]byte(`{"Table":{"TableStatus":"ACTIVE","TableName":"x"}}`))
	case "CreateTable":
		_, _ = w.Write([]byte(`{"TableDescription":{"TableStatus":"ACTIVE"}}`))
	case "PutItem":
		tableName, _ := input["TableName"].(string)
		item, _ := input["Item"].(map[string]any)
		storageKey := storageKeyFromAttrs(item)
		if f.tables[tableName] == nil {
			f.tables[tableName] = make(map[string]any)
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
		item, exists := f.tables[tableName][storageKey]
		if strings.Contains(conditionExpr, "attribute_exists") && !exists {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException"}`))
			return
		}
		if exists {
			itemMap, _ := item.(map[string]any)
			if vals, ok := input["ExpressionAttributeValues"].(map[string]any); ok {
				if role := extractAttrValueString(vals, ":role"); role != "" {
					itemMap["role"] = map[string]any{"S": role}
				}
				if updated := extractAttrValueString(vals, ":updated_at"); updated != "" {
					itemMap["updated_at"] = map[string]any{"S": updated}
				}
			}
			f.tables[tableName][storageKey] = itemMap
		}
		_, _ = w.Write([]byte(`{}`))
	case "Query", "Scan":
		tableName, _ := input["TableName"].(string)
		filterExpr, _ := input["FilterExpression"].(string)
		attrValues, _ := input["ExpressionAttributeValues"].(map[string]any)
		var items []any
		for _, it := range f.tables[tableName] {
			item, _ := it.(map[string]any)
			if filterExpr != "" && strings.Contains(filterExpr, "sk =") {
				wantSK := extractAttrValueString(attrValues, ":profile")
				if wantSK != "" && ExtractDDBString(item, "sk") != wantSK {
					continue
				}
			}
			if filterExpr != "" && strings.Contains(filterExpr, "begins_with") {
				pkVal := ExtractDDBString(item, "pk")
				pfx := extractAttrValueString(attrValues, ":pfx")
				if pfx != "" && !strings.HasPrefix(pkVal, pfx) {
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
