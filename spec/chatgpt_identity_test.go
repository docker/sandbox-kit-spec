package spec

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPresentChatGPTIdentityPreservesCompletePayload(t *testing.T) {
	header := `{"alg":"EdDSA","typ":"JWT","kid":"test-key","future_header":{"v":2}}`
	payload := `{ "email":"person@example.test", "https://api.openai.com/auth":{"chatgpt_account_id":"account-1","new_routing_metadata":{"region":"test"}}, "future_claim":[true,null,{"number":9007199254740993,"text":"日本語"}], "exp":2000000000 }`
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	signature := ed25519.Sign(key, []byte(unsigned))
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	require.True(t, ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(unsigned), signature))

	presented, err := PresentChatGPTIdentity(token)
	require.NoError(t, err)
	parts := strings.Split(presented, ".")
	require.Len(t, parts, 3)
	require.Equal(t, unsigned, parts[0]+"."+parts[1])
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	require.Equal(t, payload, string(decoded))
	placeholder, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	require.NotEmpty(t, placeholder)
	require.False(t, ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(unsigned), placeholder))
	require.NotContains(t, presented, base64.RawURLEncoding.EncodeToString(signature))
	_, err = PresentChatGPTIdentity(presented)
	require.ErrorIs(t, err, ErrChatGPTIdentity)
}

func TestPresentChatGPTIdentityRejectsAmbiguousOrMalformedTokens(t *testing.T) {
	encode := func(header, payload string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString([]byte("provider-signature-fixture"))
	}
	valid := strings.Split(encode(`{"alg":"RS256"}`, `{}`), ".")
	replace := func(index int, part string) string {
		parts := append([]string(nil), valid...)
		parts[index] = part
		return strings.Join(parts, ".")
	}
	for name, token := range map[string]string{
		"empty":                "",
		"two parts":            "e30.e30",
		"four parts":           "e30.e30.c2ln.extra",
		"invalid header":       replace(0, "!"),
		"invalid payload":      replace(1, "!"),
		"empty signature":      replace(2, ""),
		"invalid signature":    replace(2, "!"),
		"header newline":       replace(0, valid[0][:2]+"\n"+valid[0][2:]),
		"payload newline":      replace(1, valid[1][:2]+"\r\n"+valid[1][2:]),
		"signature newline":    replace(2, valid[2]+"\n"),
		"header space":         replace(0, valid[0]+" "),
		"missing algorithm":    encode(`{}`, `{}`),
		"wrong algorithm case": encode(`{"ALG":"RS256"}`, `{}`),
		"unsigned":             encode(`{"alg":"none"}`, `{}`),
		"unsigned case":        encode(`{"alg":"NONE"}`, `{}`),
		"null algorithm":       encode(`{"alg":null}`, `{}`),
		"header array":         encode(`[]`, `{}`),
		"payload array":        encode(`{"alg":"RS256"}`, `[]`),
		"payload null":         encode(`{"alg":"RS256"}`, `null`),
		"trailing JSON":        encode(`{"alg":"RS256"}`, `{} {}`),
		"duplicate header":     encode(`{"alg":"RS256","alg":"none"}`, `{}`),
		"duplicate payload":    encode(`{"alg":"RS256"}`, `{"account":"one","account":"two"}`),
		"nested duplicate":     encode(`{"alg":"RS256"}`, `{"accounts":[{"id":"one","id":"two"}]}`),
		"escaped duplicate":    encode(`{"alg":"RS256"}`, `{"id":"one","\u0069d":"two"}`),
		"invalid UTF-8":        encode(`{"alg":"RS256"}`, "{\"value\":\"\xff\"}"),
		"deep JSON":            encode(`{"alg":"RS256"}`, `{"nested":`+strings.Repeat("[", 66)+"0"+strings.Repeat("]", 66)+"}"),
		"oversized":            strings.Repeat("x", chatGPTIdentityLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			presented, err := PresentChatGPTIdentity(token)
			require.ErrorIs(t, err, ErrChatGPTIdentity)
			require.Empty(t, presented)
			require.Equal(t, ErrChatGPTIdentity.Error(), err.Error())
		})
	}
}
