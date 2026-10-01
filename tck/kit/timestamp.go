package kit

import (
	"regexp"
	"strings"
	"time"
)

// RFC 3339 §5.6 permits lowercase t/z and leap seconds. Its grammar also
// forbids forms time.Parse accepts, including one-digit hours, comma
// fractions, and timezone offsets with hours 24 or minutes 60.
var rfc3339TimestampPattern = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt]` +
		`(?:[01][0-9]|2[0-3]):[0-5][0-9]:(?:[0-5][0-9]|60)(?:\.[0-9]+)?` +
		`(?:[Zz]|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`,
)

func validRFC3339Timestamp(value string) bool {
	if !rfc3339TimestampPattern.MatchString(value) {
		return false
	}

	// Go's parser supplies calendar validation after the syntax check.
	// It cannot represent leap seconds, so use second 59 only for this
	// check; the annotation itself is neither parsed into an instant nor
	// rewritten. Validating syntax does not need a leap-second history.
	normalized := strings.ToUpper(value)
	if normalized[17:19] == "60" {
		normalized = normalized[:17] + "59" + normalized[19:]
	}
	_, err := time.Parse(time.RFC3339, normalized)
	return err == nil
}
