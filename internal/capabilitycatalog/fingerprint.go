package capabilitycatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// schemaContractHash covers the shared capability constraints, this type's
// conditional mapping, and the JSON Schema fragments reachable through local
// references. It deliberately excludes other types' conditional mappings.
func schemaContractHash(read readFile, rootSchema []byte, conditionIndex int) (string, error) {
	var root map[string]any
	if err := decodeSchemaJSON(rootSchema, &root); err != nil {
		return "", fmt.Errorf("decode root schema: %w", err)
	}
	definitions, ok := root["definitions"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("root schema has no definitions")
	}
	capability, ok := definitions["capability"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("root schema has no capability definition")
	}
	conditions, ok := capability["allOf"].([]any)
	if !ok || conditionIndex >= len(conditions) {
		return "", fmt.Errorf("root schema has no capability condition %d", conditionIndex)
	}
	common := make(map[string]any, len(capability))
	for key, value := range capability {
		if key != "allOf" {
			common[key] = value
		}
	}
	walk := referenceWalk{read: read, documents: map[string]any{"schema/kit.schema.json": root}, seen: make(map[string]bool), targets: make(map[string]any)}
	if err := walk.visit(common, "schema/kit.schema.json"); err != nil {
		return "", err
	}
	if err := walk.visit(conditions[conditionIndex], "schema/kit.schema.json"); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(struct {
		Common     any            `json:"common"`
		Condition  any            `json:"condition"`
		References map[string]any `json:"references"`
	}{common, conditions[conditionIndex], walk.targets})
	if err != nil {
		return "", err
	}
	return hash(canonical), nil
}

type referenceWalk struct {
	read      readFile
	documents map[string]any
	seen      map[string]bool
	targets   map[string]any
}

func (w *referenceWalk) visit(node any, document string) error {
	switch value := node.(type) {
	case map[string]any:
		if raw, exists := value["$ref"]; exists {
			ref, ok := raw.(string)
			if !ok {
				return fmt.Errorf("%s: non-string $ref", document)
			}
			if err := w.follow(document, ref); err != nil {
				return err
			}
		}
		for _, child := range value {
			if err := w.visit(child, document); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := w.visit(child, document); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *referenceWalk) follow(document, ref string) error {
	location, fragment, hasFragment := strings.Cut(ref, "#")
	if strings.Contains(location, ":") || strings.Contains(location, "\\") || path.IsAbs(location) || strings.HasPrefix(location, "//") {
		return fmt.Errorf("%s: unsupported schema reference %q", document, ref)
	}
	targetDocument := document
	if location != "" {
		targetDocument = path.Clean(path.Join(path.Dir(document), location))
		if !strings.HasPrefix(targetDocument, "schema/") || !strings.HasSuffix(targetDocument, ".json") {
			return fmt.Errorf("%s: schema reference escapes schema tree: %q", document, ref)
		}
	}
	if hasFragment && fragment != "" && !strings.HasPrefix(fragment, "/") {
		return fmt.Errorf("%s: unsupported schema fragment %q", document, ref)
	}
	key := targetDocument + "#" + fragment
	if w.seen[key] {
		return nil
	}
	w.seen[key] = true
	source, ok := w.documents[targetDocument]
	if !ok {
		data, err := w.read(targetDocument)
		if err != nil {
			return fmt.Errorf("read schema reference %s: %w", targetDocument, err)
		}
		if err := decodeSchemaJSON(data, &source); err != nil {
			return fmt.Errorf("decode schema reference %s: %w", targetDocument, err)
		}
		w.documents[targetDocument] = source
	}
	target := source
	if fragment != "" {
		for _, rawToken := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
			token := strings.ReplaceAll(strings.ReplaceAll(rawToken, "~1", "/"), "~0", "~")
			var err error
			target, err = schemaPointerChild(target, token)
			if err != nil {
				return fmt.Errorf("%s: reference %q: %w", document, ref, err)
			}
		}
	}
	w.targets[key] = target
	return w.visit(target, targetDocument)
}

func decodeSchemaJSON(data []byte, target any) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid JSON schema")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func schemaPointerChild(node any, token string) (any, error) {
	switch value := node.(type) {
	case map[string]any:
		if child, ok := value[token]; ok {
			return child, nil
		}
	case []any:
		index, err := strconv.Atoi(token)
		if err == nil && index >= 0 && index < len(value) && strconv.Itoa(index) == token {
			return value[index], nil
		}
	}
	return nil, fmt.Errorf("unresolved JSON Pointer token %q", token)
}
