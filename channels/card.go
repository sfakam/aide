package channels

import "context"

// AdaptiveCard is an Adaptive Card (v1.2) payload for Webex.
// See https://adaptivecards.io for the full schema.
type AdaptiveCard struct {
	Type    string           `json:"type"`
	Schema  string           `json:"$schema"`
	Version string           `json:"version"`
	Body    []map[string]any `json:"body"`
}

// NewCard constructs a ready-to-send AdaptiveCard from body elements.
func NewCard(body ...map[string]any) AdaptiveCard {
	return AdaptiveCard{
		Type:    "AdaptiveCard",
		Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
		Version: "1.2",
		Body:    body,
	}
}

// TextBlock returns a TextBlock card element. Extra props override defaults.
func TextBlock(text string, props ...map[string]any) map[string]any {
	el := map[string]any{"type": "TextBlock", "text": text, "wrap": true}
	if len(props) > 0 {
		for k, v := range props[0] {
			el[k] = v
		}
	}
	return el
}

// Fact is a title+value pair used in a FactSet.
type Fact struct {
	Title string
	Value string
}

// FactSet returns a FactSet card element.
func FactSet(facts ...Fact) map[string]any {
	fs := make([]map[string]string, len(facts))
	for i, f := range facts {
		fs[i] = map[string]string{"title": f.Title, "value": f.Value}
	}
	return map[string]any{"type": "FactSet", "facts": fs}
}

// CardChannel is an optional interface an adapter may implement to post
// Adaptive Cards. The scheduler checks for it via type assertion; adapters
// that don't implement it receive a plain-text fallback via Send instead.
type CardChannel interface {
	SendCard(ctx context.Context, roomID string, card AdaptiveCard, fallback string) error
}
