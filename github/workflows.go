package github

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/google/go-github/v89/github"
)

type Workflow struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	State string `json:"state"`
}

func (c *Client) GetWorkflows(ctx context.Context) ([]*Workflow, error) {
	workflows, _, err := c.ListWorkflowsPage(ctx, c.perPageLimit, 1)
	return workflows, err
}

// ListWorkflowsPage returns one page of workflows and the next GitHub page,
// or zero when there are no more pages.
func (c *Client) ListWorkflowsPage(ctx context.Context, perPage, page int) ([]*Workflow, int, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = c.perPageLimit
	}
	if page <= 0 {
		page = 1
	}
	workflows, response, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: perPage, Page: page})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list workflows: %w", err)
	}

	result := make([]*Workflow, len(workflows.Workflows))
	for i, w := range workflows.Workflows {
		result[i] = &Workflow{
			ID:    w.GetID(),
			Name:  w.GetName(),
			Path:  w.GetPath(),
			State: w.GetState(),
		}
	}

	nextPage := 0
	if response != nil {
		nextPage = response.NextPage
	}
	return result, nextPage, nil
}

func (c *Client) TriggerWorkflow(ctx context.Context, workflowID string, ref string) error {
	// Use the shared helper to resolve workflow ID
	id, _, err := c.ResolveWorkflowID(ctx, workflowID)
	if err != nil {
		return fmt.Errorf("failed to trigger workflow %s: %w", workflowID, err)
	}

	_, _, err = c.gh.Actions.CreateWorkflowDispatchEventByID(ctx, c.owner, c.repo, id, github.CreateWorkflowDispatchEventRequest{
		Ref: ref,
	})
	if err != nil {
		return fmt.Errorf("failed to trigger workflow %s: %w", workflowID, err)
	}
	return nil
}

// ResolveWorkflowID resolves a workflow identifier (ID or name) to a numeric ID and name.
// Returns the workflow ID, name, and an error if the workflow is not found.
func (c *Client) ResolveWorkflowID(ctx context.Context, workflowID string) (int64, string, error) {
	// Try to parse as ID first
	if id, err := ParseWorkflowID(workflowID); err == nil {
		// Look up the workflow to get its name
		workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: c.perPageLimit})
		if err != nil {
			return 0, "", fmt.Errorf("failed to list workflows: %w", err)
		}
		for _, w := range workflows.Workflows {
			if w.GetID() == id {
				return id, w.GetName(), nil
			}
		}
		// ID exists but workflow not found - return ID as name
		return id, workflowID, nil
	}

	// Try by name - list workflows and find by name
	workflows, _, err := c.gh.Actions.ListWorkflows(ctx, c.owner, c.repo, &github.ListOptions{PerPage: c.perPageLimit})
	if err != nil {
		return 0, "", fmt.Errorf("failed to list workflows: %w", err)
	}

	for _, w := range workflows.Workflows {
		if workflowSelectorMatches(workflowID, w.GetName(), w.GetPath()) {
			return w.GetID(), w.GetName(), nil
		}
	}

	return 0, "", fmt.Errorf("workflow %s not found%s", workflowID, workflowLookupHint(workflows.Workflows))
}

// workflowSelectorMatches reports whether selector identifies a workflow by
// name, full path, or basename (e.g. "firmware-hil-test.yml").
func workflowSelectorMatches(selector, name, filePath string) bool {
	if selector == name || selector == filePath {
		return true
	}
	base := path.Base(filePath)
	return selector == base || strings.EqualFold(selector, base)
}

// workflowLookupHint lists a few known workflow names/paths so a miss is
// actionable instead of a bare "not found".
func workflowLookupHint(workflows []*github.Workflow) string {
	if len(workflows) == 0 {
		return ""
	}
	limit := 8
	if len(workflows) < limit {
		limit = len(workflows)
	}
	names := make([]string, 0, limit)
	for _, w := range workflows[:limit] {
		label := w.GetName()
		if p := w.GetPath(); p != "" {
			label = fmt.Sprintf("%s (%s)", label, path.Base(p))
		}
		names = append(names, label)
	}
	hint := "; known workflows: " + strings.Join(names, ", ")
	if len(workflows) > limit {
		hint += fmt.Sprintf(", … (%d more)", len(workflows)-limit)
	}
	return hint
}

// ParseWorkflowID parses a workflow ID string into an int64
func ParseWorkflowID(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}
