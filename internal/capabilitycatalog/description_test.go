package capabilitycatalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDescriptionsPreserveLiteralMarkdownInCode(t *testing.T) {
	specURL := repository + "/blob/" + tag + "/docs/spec/capabilities/com.docker.sandbox/port@1.md"
	for _, code := range []string{"`[port](port@1.md)`", "``[port](port@1.md) with ` inside``"} {
		input := "Use " + code + " literally; see [port](port@1.md)."
		got, err := rewriteDescriptionLinks(input, specURL)
		require.NoError(t, err)
		require.Equal(t, "Use "+code+" literally; see [port]("+specURL+").", got)
		require.NoError(t, validateDescription(got, specURL))
	}
}

func TestDescriptionsRejectThematicBreaks(t *testing.T) {
	specURL := repository + "/blob/" + tag + "/docs/spec/capabilities/com.docker.sandbox/port@1.md"
	for _, separator := range []string{"***", "___", "---", "* * *", "_ _ _", "- - -", "****", "____"} {
		require.Error(t, validateDescription(separator, specURL), separator)
	}
}

func TestDescriptionExtractionRejectsInterruptingBlocks(t *testing.T) {
	id := "com.docker.sandbox/port@1"
	specURL := repository + "/blob/" + tag + "/docs/spec/capabilities/" + id + ".md"
	for _, block := range []string{"***", "_ _ _", "---", "===", "## Config", "- item", "1) item", "```go"} {
		page := "# `" + id + "`\n\nIntro text.\n" + block + "\n\n## Config\n"
		_, err := descriptionFromPage([]byte(page), id, specURL)
		require.Error(t, err, block)
	}
}
