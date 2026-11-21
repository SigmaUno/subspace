package client

import (
	"context"

	"github.com/SigmaUno/subspace/internal/knowledge"
)

type knowledgeEntry struct {
	Notice string           `json:"notice"`
	Entry  *knowledge.Entry `json:"entry"`
}

func (m *MCP) knowledgeEntry(ctx context.Context, tool string, args any) (*knowledge.Entry, error) {
	var out knowledgeEntry
	if err := m.Call(ctx, tool, args, &out); err != nil {
		return nil, err
	}
	return out.Entry, nil
}

func (m *MCP) AddKnowledge(ctx context.Context, in knowledge.Input) (*knowledge.Entry, error) {
	return m.knowledgeEntry(ctx, "add_knowledge", in)
}

func (m *MCP) GetKnowledge(ctx context.Context, id string) (*knowledge.Entry, error) {
	return m.knowledgeEntry(ctx, "get_knowledge", map[string]any{"id": id})
}

func (m *MCP) SupersedeKnowledge(ctx context.Context, id, by string) (*knowledge.Entry, error) {
	return m.knowledgeEntry(ctx, "supersede_knowledge", map[string]any{"id": id, "by": by})
}

func (m *MCP) PinKnowledge(ctx context.Context, id string, pinned bool) (*knowledge.Entry, error) {
	return m.knowledgeEntry(ctx, "pin_knowledge", map[string]any{"id": id, "pinned": pinned})
}

func (m *MCP) SearchKnowledge(ctx context.Context, query, kind, briefID string, limit int) ([]*knowledge.Entry, error) {
	var out struct {
		Entries []*knowledge.Entry `json:"entries"`
	}
	err := m.Call(ctx, "search_knowledge", map[string]any{"query": query, "kind": kind, "brief_id": briefID, "limit": limit}, &out)
	return out.Entries, err
}
