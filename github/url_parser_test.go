package github

import (
	"testing"
)

func TestParseActionsURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantOwner   string
		wantRepo    string
		wantRunID   int64
		wantJobID   int64
		wantIsJob   bool
		wantErr     bool
		errContains string
	}{
		{
			name:      "run URL",
			url:       "https://github.com/denysvitali/gps-tracker-tr003-v2/actions/runs/21662021288",
			wantOwner: "denysvitali",
			wantRepo:  "gps-tracker-tr003-v2",
			wantRunID: 21662021288,
			wantJobID: 0,
			wantIsJob: false,
			wantErr:   false,
		},
		{
			name:      "job URL",
			url:       "https://github.com/denysvitali/gps-tracker-tr003-v2/actions/runs/21662021288/job/62449039965",
			wantOwner: "denysvitali",
			wantRepo:  "gps-tracker-tr003-v2",
			wantRunID: 21662021288,
			wantJobID: 62449039965,
			wantIsJob: true,
			wantErr:   false,
		},
		{
			name:      "run URL with trailing slash",
			url:       "https://github.com/denysvitali/gps-tracker-tr003-v2/actions/runs/21662021288/",
			wantOwner: "denysvitali",
			wantRepo:  "gps-tracker-tr003-v2",
			wantRunID: 21662021288,
			wantJobID: 0,
			wantIsJob: false,
			wantErr:   false,
		},
		{
			name:      "job URL with trailing slash",
			url:       "https://github.com/denysvitali/gps-tracker-tr003-v2/actions/runs/21662021288/job/62449039965/",
			wantOwner: "denysvitali",
			wantRepo:  "gps-tracker-tr003-v2",
			wantRunID: 21662021288,
			wantJobID: 62449039965,
			wantIsJob: true,
			wantErr:   false,
		},
		{
			name:        "invalid URL",
			url:         "https://example.com/something",
			wantErr:     true,
			errContains: "unsupported GitHub Actions URL format",
		},
		{
			name:        "empty string",
			url:         "",
			wantErr:     true,
			errContains: "unsupported GitHub Actions URL format",
		},
		{
			name:        "not a URL",
			url:         "just-some-text",
			wantErr:     true,
			errContains: "unsupported GitHub Actions URL format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseActionsURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseActionsURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				if err == nil || !contains(err.Error(), tt.errContains) {
					t.Errorf("ParseActionsURL() error = %v, should contain %v", err, tt.errContains)
				}
				return
			}
			if got.Owner != tt.wantOwner {
				t.Errorf("ParseActionsURL() Owner = %v, want %v", got.Owner, tt.wantOwner)
			}
			if got.Repo != tt.wantRepo {
				t.Errorf("ParseActionsURL() Repo = %v, want %v", got.Repo, tt.wantRepo)
			}
			if got.RunID != tt.wantRunID {
				t.Errorf("ParseActionsURL() RunID = %v, want %v", got.RunID, tt.wantRunID)
			}
			if got.JobID != tt.wantJobID {
				t.Errorf("ParseActionsURL() JobID = %v, want %v", got.JobID, tt.wantJobID)
			}
			if got.IsJobURL() != tt.wantIsJob {
				t.Errorf("ParseActionsURL() IsJobURL() = %v, want %v", got.IsJobURL(), tt.wantIsJob)
			}
		})
	}
}

func TestIsActionsURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "valid run URL",
			url:  "https://github.com/owner/repo/actions/runs/123456",
			want: true,
		},
		{
			name: "valid job URL",
			url:  "https://github.com/owner/repo/actions/runs/123456/job/789012",
			want: true,
		},
		{
			name: "invalid URL",
			url:  "https://example.com/something",
			want: false,
		},
		{
			name: "empty string",
			url:  "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsActionsURL(tt.url); got != tt.want {
				t.Errorf("IsActionsURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActionsURLString(t *testing.T) {
	tests := []struct {
		name string
		url  *ActionsURL
		want string
	}{
		{
			name: "run URL",
			url:  &ActionsURL{Owner: "owner", Repo: "repo", RunID: 123456},
			want: "https://github.com/owner/repo/actions/runs/123456",
		},
		{
			name: "job URL",
			url:  &ActionsURL{Owner: "owner", Repo: "repo", RunID: 123456, JobID: 789012},
			want: "https://github.com/owner/repo/actions/runs/123456/job/789012",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.url.String(); got != tt.want {
				t.Errorf("ActionsURL.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseRunID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		want    int64
		wantErr bool
	}{
		{
			name: "valid ID",
			id:   "21662021288",
			want: 21662021288,
		},
		{
			name:    "invalid ID",
			id:      "not-a-number",
			wantErr: true,
		},
		{
			name: "ID with whitespace",
			id:   "  21662021288  ",
			want: 21662021288,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRunID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRunID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseRunID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
