package model

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestLinkProtoRoundTrip(t *testing.T) {
	// Arrange
	link := Link{
		ID:        7,
		TaskID:    42,
		Relevance: "related PR",
		URL:       "https://github.com/o/r/pull/5",
		Title:     "Fix bug",
		Kind:      "owned-pr",
		Claim:     "pr-owner",
		CreatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	// Act
	got := LinkFromProto(link.Proto())

	// Assert
	assert.DeepEqual(t, *got, link)
}
