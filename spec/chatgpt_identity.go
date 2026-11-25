package spec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

var ErrChatGPTIdentity = errors.New("invalid host ChatGPT identity token")

const chatGPTIdentityLimit = 128 << 10

// PresentChatGPTIdentity preserves the encoded header and complete payload of
// a host-selected ID token and replaces its signature with a non-authenticating
// placeholder. It does not verify the provider's signature or authorize an
// account; those decisions belong to the host before presentation.
func PresentChatGPTIdentity(token string) (string, error) {
	if len(token) > chatGPTIdentityLimit || strings.ContainsAny(token, " \t\r\n\v\f") {
		return "", ErrChatGPTIdentity
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrChatGPTIdentity
	}
	header, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil || !chatGPTIdentityObject(header) {
		return "", ErrChatGPTIdentity
	}
	var metadata map[string]json.RawMessage
	var algorithm string
	if json.Unmarshal(header, &metadata) != nil || json.Unmarshal(metadata["alg"], &algorithm) != nil || algorithm == "" || strings.EqualFold(algorithm, "none") {
		return "", ErrChatGPTIdentity
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || !chatGPTIdentityObject(payload) {
		return "", ErrChatGPTIdentity
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(signature) == 0 {
		return "", ErrChatGPTIdentity
	}
	placeholder := base64.RawURLEncoding.EncodeToString([]byte("sbx-identity-placeholder"))
	if parts[2] == placeholder {
		return "", ErrChatGPTIdentity
	}
	return parts[0] + "." + parts[1] + "." + placeholder, nil
}

func chatGPTIdentityObject(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') || !chatGPTIdentityMembers(decoder, true, 0) {
		return false
	}
	_, err = decoder.Token()
	return errors.Is(err, io.EOF)
}

// Duplicate names can make the host and client disagree on the same account.
// Walk without re-encoding so unknown claims, number precision and whitespace
// remain exactly as the provider supplied them.
func chatGPTIdentityMembers(decoder *json.Decoder, object bool, depth int) bool {
	if depth > 64 {
		return false
	}
	names := map[string]bool{}
	for decoder.More() {
		if object {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || names[name] {
				return false
			}
			names[name] = true
		}
		value, err := decoder.Token()
		if err != nil {
			return false
		}
		if delimiter, ok := value.(json.Delim); ok {
			if delimiter != '{' && delimiter != '[' {
				return false
			}
			if !chatGPTIdentityMembers(decoder, delimiter == '{', depth+1) {
				return false
			}
		}
	}
	end, err := decoder.Token()
	if object {
		return err == nil && end == json.Delim('}')
	}
	return err == nil && end == json.Delim(']')
}
