package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/denysvitali/gh-actions-mcp/github"
)

type monitorTarget struct {
	Version  int               `json:"v"`
	Owner    string            `json:"owner"`
	Repo     string            `json:"repo"`
	SHA      string            `json:"sha"`
	RunID    int64             `json:"run_id,omitempty"`
	Attempt  int               `json:"attempt,omitempty"`
	Expected []string          `json:"expected,omitempty"`
	Diagnose bool              `json:"diagnose,omitempty"`
	Expires  int64             `json:"expires"`
	Previous map[string]string `json:"previous,omitempty"`
}

func (s *MCPServer) newMonitorTarget(ctx context.Context, in verifyInput) (monitorTarget, *github.Client, error) { //nolint:nestif // Run and commit targets resolve through distinct authorities.
	if err := validateMonitorInput(in); err != nil {
		return monitorTarget{}, nil, err
	}
	client, owner, repo, err := s.clientFromInput(in.repoInput)
	t := monitorTarget{Version: 1, Owner: owner, Repo: repo, RunID: in.RunID, Attempt: in.ExpectedAttempt, Expected: in.ExpectedWorkflows, Diagnose: in.DiagnoseOnFailure, Expires: time.Now().Add(24 * time.Hour).Unix()}
	if err != nil {
		return t, nil, err
	}
	if in.RunID > 0 { //nolint:nestif // Run metadata establishes immutable SHA and attempt before monitoring.
		run, runErr := client.GetWorkflowRun(ctx, in.RunID)
		if runErr != nil {
			return t, nil, runErr
		}
		if run.RunAttempt < 1 || run.HeadSHA == "" {
			return t, nil, fmt.Errorf("run metadata lacks SHA or attempt; cannot pin monitor")
		}
		t.SHA = run.HeadSHA
		if t.Attempt == 0 {
			t.Attempt = run.RunAttempt
		}
	} else {
		if in.ExpectedAttempt != 0 {
			return t, nil, fmt.Errorf("expected_attempt requires run_id")
		}
		ref, refErr := s.resolveCheckRef(in.Ref, owner, repo)
		if refErr != nil {
			return t, nil, refErr
		}
		t.SHA, err = client.ResolveRef(ctx, ref)
	}
	return t, client, err
}

func (s *MCPServer) monitorSignature(data string) string {
	mac := hmac.New(sha256.New, s.cursorKey())
	_, _ = fmt.Fprintf(mac, "monitor-v1:%s:%s:%s", s.config.APIBaseURL, s.config.AuthUsername, data)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *MCPServer) encodeMonitorCursor(t monitorTarget) (string, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	if len(payload)+65 > 65536 {
		return "", fmt.Errorf("monitor state exceeds cursor budget; reduce expected_workflows")
	}
	return payload + "." + s.monitorSignature(payload), nil
}

func (s *MCPServer) decodeMonitorCursor(cursor string) (monitorTarget, error) {
	var t monitorTarget
	if len(cursor) > 65536 {
		return t, fmt.Errorf("monitor cursor too large")
	}
	payload, signature, ok := strings.Cut(cursor, ".")
	if !ok || !hmac.Equal([]byte(signature), []byte(s.monitorSignature(payload))) {
		return t, fmt.Errorf("invalid monitor cursor signature")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(data, &t) != nil || t.Version != 1 || t.Owner == "" || t.Repo == "" || t.SHA == "" {
		return t, fmt.Errorf("invalid monitor cursor")
	}
	if t.Expires < time.Now().Unix() {
		return t, fmt.Errorf("monitor cursor expired; verify the pinned SHA again")
	}
	return t, nil
}

func validateMonitorInput(in verifyInput) error {
	if in.RunID > 0 && (in.Ref != "" || len(in.ExpectedWorkflows) > 0) {
		return fmt.Errorf("run_id cannot be combined with ref or expected_workflows")
	}
	workflowBytes := 0
	for _, name := range in.ExpectedWorkflows {
		workflowBytes += len(name)
	}
	if workflowBytes > 8192 {
		return fmt.Errorf("expected_workflows exceeds 8192-byte aggregate limit")
	}

	return nil
}
