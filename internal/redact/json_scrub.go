package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

func shouldScrubJSONStringValue(path []string, key string) bool {
	return isUserContentJSONPath(path, key)
}

func (r *Redactor) scrubJSON(ctx context.Context, text string, reg *Registry, forceRedactMarkers bool) (Result, error) {
	var root any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return r.scrub(ctx, text, reg, forceRedactMarkers)
	}

	acc := Result{EntityCounts: map[string]int{}}
	if err := r.walkAndScrubJSON(ctx, root, nil, reg, forceRedactMarkers, &acc); err != nil {
		return Result{}, err
	}

	out, err := marshalJSONPreserveHTML(root)
	if err != nil {
		return Result{}, fmt.Errorf("redact: marshal scrubbed JSON: %w", err)
	}
	acc.Text = string(out)
	return acc, nil
}

func marshalJSONPreserveHTML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func (r *Redactor) walkAndScrubJSON(ctx context.Context, v any, path []string, reg *Registry, forceRedactMarkers bool, acc *Result) error {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			childPath := append(path, k)
			if s, ok := child.(string); ok && shouldScrubJSONStringValue(path, k) {
				sub, err := r.scrub(ctx, s, reg, forceRedactMarkers)
				if err != nil {
					return err
				}
				val[k] = sub.Text
				mergeScrubResult(acc, sub)
				continue
			}
			if err := r.walkAndScrubJSON(ctx, child, childPath, reg, forceRedactMarkers, acc); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range val {
			if err := r.walkAndScrubJSON(ctx, child, path, reg, forceRedactMarkers, acc); err != nil {
				return err
			}
		}
	}
	return nil
}

func mergeScrubResult(dst *Result, src Result) {
	for k, v := range src.EntityCounts {
		dst.EntityCounts[k] += v
	}
	if len(src.DetectedEntities) > 0 {
		dst.DetectedEntities = append(dst.DetectedEntities, src.DetectedEntities...)
	}
	if len(src.AllowedEntities) > 0 {
		dst.AllowedEntities = append(dst.AllowedEntities, src.AllowedEntities...)
	}
}
